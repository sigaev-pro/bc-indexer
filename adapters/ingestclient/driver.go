package ingestclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// DefaultPollEvery is the GetBatchState interval of a Config that names none.
const DefaultPollEvery = 250 * time.Millisecond

// cleanupStepKeys is the largest number of payload keys one bounded cleanup
// step removes (step 11).
const cleanupStepKeys = 1024

// announceBound is the largest number of announced batches the driver holds
// before Ready answers that its queue is full.
const announceBound = 1024

// Config is the gateway link of one driver.
type Config struct {
	// Endpoint is the gateway endpoint, config.LiveConfig.Endpoint:
	// https://host:port.
	Endpoint config.Endpoint
	// TLS is the mutual TLS of the link: CA file, certificate file, key file.
	TLS config.ResolvedTLS
	// Network is the network identity of the handshake.
	Network proto.NetworkIdentity
	// Binding is the binding of destination.binding_file.
	Binding destination.Binding
	// PollEvery is the GetBatchState interval while a batch waits for
	// NATS_ACKED. Zero selects DefaultPollEvery.
	PollEvery time.Duration
}

// DriverConfig configures one driver.
type DriverConfig struct {
	// Client is the gateway link.
	Client Config
	// Store is the producer journal.
	Store *producer.Store
	// Storage is the storage of that journal: the driver keeps
	// driver/meta/connection_generation under its own prefix.
	Storage storage.Storage
	// Reporter receives the log lines of the driver; nil disables them.
	Reporter *config.Reporter
}

// Driver moves the batches of one producer journal from READY to RETIRED
// through the gateway RPCs, one batch in flight, in batch sequence order.
// A driver runs once; a restart is a new driver over the same journal.
type Driver struct {
	cfg    Config
	store  *producer.Store
	st     storage.Storage
	report *config.Reporter
	id     producer.Identity

	mu      sync.Mutex
	running bool
	ended   bool
	// queue holds the announced batch sequences.
	queue map[uint64]struct{}
	// wake carries the signal of an announcement.
	wake chan struct{}
}

// NewDriver validates its input and performs no I/O.
func NewDriver(cfg DriverConfig) (*Driver, error) {
	switch {
	case cfg.Store == nil:
		return nil, fmt.Errorf("%w: Store is nil", ErrConfig)
	case cfg.Storage == nil:
		return nil, fmt.Errorf("%w: Storage is nil", ErrConfig)
	case cfg.Client.PollEvery < 0:
		return nil, fmt.Errorf("%w: PollEvery %s is negative", ErrConfig, cfg.Client.PollEvery)
	}
	if err := checkLink(cfg.Client.Endpoint, cfg.Client.TLS); err != nil {
		return nil, err
	}
	if err := cfg.Client.Network.Validate(); err != nil {
		return nil, fmt.Errorf("%w: network: %v", ErrConfig, err)
	}
	if err := cfg.Client.Binding.Validate(); err != nil {
		return nil, fmt.Errorf("%w: binding: %v", ErrConfig, err)
	}
	if cfg.Client.Network != cfg.Client.Binding.Network {
		return nil, fmt.Errorf("%w: the network differs from the network of the binding", ErrConfig)
	}
	// One destination for both owners: the journal saved the binding the
	// driver talks to, or nothing is sent.
	if saved := cfg.Store.BindingDigest(); saved != cfg.Client.Binding.DescriptorDigest {
		return nil, fmt.Errorf("%w: binding %x differs from the binding the producer journal saved %x",
			ErrConfig, cfg.Client.Binding.DescriptorDigest, saved)
	}
	if cfg.Client.PollEvery == 0 {
		cfg.Client.PollEvery = DefaultPollEvery
	}
	return &Driver{
		cfg:    cfg.Client,
		store:  cfg.Store,
		st:     cfg.Storage,
		report: cfg.Reporter,
		id:     cfg.Store.Identity(),
		queue:  make(map[uint64]struct{}),
		wake:   make(chan struct{}, 1),
	}, nil
}

// Run drives every batch from READY to RETIRED, in batch sequence order, one
// batch in flight, until ctx ends. It answers nil when ctx ended and a
// *Failure when an answer or a local error stopped it; every byte is kept.
func (d *Driver) Run(ctx context.Context) error {
	d.mu.Lock()
	if d.ended {
		d.mu.Unlock()
		return ErrDriverStopped
	}
	if d.running {
		d.mu.Unlock()
		return ErrDriverRunning
	}
	d.running = true
	d.mu.Unlock()

	err := d.run(ctx)
	if err != nil && ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		err = nil // the run ended because its context did
	}

	d.mu.Lock()
	d.running = false
	d.ended = true
	d.queue = nil
	d.mu.Unlock()
	if err == nil {
		return nil
	}
	failure := newFailure(err)
	if d.report != nil {
		d.report.Failure(failure)
	}
	return failure
}

// Ready implements the collector's Delivery seam: it announces a READY batch
// and never blocks. An error means the queue is unavailable.
func (d *Driver) Ready(seq uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ended {
		return ErrDriverStopped
	}
	if _, queued := d.queue[seq]; !queued {
		if len(d.queue) >= announceBound {
			return fmt.Errorf("%w: %d batches are announced, batch %d is not", ErrQueueFull, len(d.queue), seq)
		}
		d.queue[seq] = struct{}{}
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}

// run is the loop of one driver run.
func (d *Driver) run(ctx context.Context) error {
	c := newClient(d.cfg, d.st, d.store)
	if err := c.connect(ctx); err != nil {
		return err
	}
	defer c.close()

	// watch holds every batch the driver has to look at again. A full scan
	// walks the journal at the start of the run and then whenever no
	// announcement arrived for one poll interval, so a READY batch that no
	// announcement named is still found; an announcement alone costs one
	// point read.
	watch := make(map[uint64]bool)
	rescan := true
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := d.releaseTurn(ctx, c); err != nil {
			return err
		}
		d.takeAnnouncements(watch)
		if rescan {
			rescan = false
			if err := d.scan(watch); err != nil {
				return err
			}
		}
		seq, found, err := d.pick(watch)
		if err != nil {
			return err
		}
		if !found {
			if d.wait(ctx) {
				return nil
			}
			rescan = true
			continue
		}
		if err := d.deliver(ctx, c, seq); err != nil {
			return err
		}
	}
}

// takeAnnouncements moves the announced sequences into the watch list.
func (d *Driver) takeAnnouncements(watch map[uint64]bool) {
	d.mu.Lock()
	for seq := range d.queue {
		watch[seq] = true
	}
	d.queue = make(map[uint64]struct{})
	d.mu.Unlock()
}

// scan adds every batch the journal holds that is not RETIRED.
func (d *Driver) scan(watch map[uint64]bool) error {
	pending, err := d.store.Pending()
	if err != nil {
		return err
	}
	for _, info := range pending {
		watch[info.Seq] = true
	}
	return nil
}

// pick answers the lowest batch of the watch list that is READY or SEALED:
// the next one the driver moves. INTENT and STAGING belong to the collector,
// and an ACKED_CLEANUP batch waits in the outbox for the turn the loop gives
// it before the next pick. A RETIRED batch leaves the list.
func (d *Driver) pick(watch map[uint64]bool) (seq uint64, found bool, err error) {
	seqs := make([]uint64, 0, len(watch))
	for seq := range watch {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, seq := range seqs {
		info, err := d.store.Batch(seq)
		if errors.Is(err, producer.ErrNotFound) {
			// Announced before it existed, or gone: the next announcement
			// and the next scan are its chance.
			delete(watch, seq)
			continue
		}
		if err != nil {
			return 0, false, err
		}
		switch info.State {
		case producer.StateRetired:
			delete(watch, seq)
		case producer.StateReady, producer.StateSealed:
			return seq, true, nil
		}
	}
	return 0, false, nil
}

// wait blocks until an announcement arrives, one poll interval passes or ctx
// ended. It reports whether the run is over.
func (d *Driver) wait(ctx context.Context) (over bool) {
	timer := time.NewTimer(d.cfg.PollEvery)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-d.wake:
		return false
	case <-timer.C:
		return false
	}
}

// deliver moves one batch as far as the gateway lets it: a READY batch to
// SEALED, a SEALED batch to ACKED_CLEANUP. The release waits for the turn
// of the batch.
func (d *Driver) deliver(ctx context.Context, c *client, seq uint64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.store.Batch(seq)
		if err != nil {
			return err
		}
		switch info.State {
		case producer.StateReady:
			err = d.prepare(ctx, c, seq, info)
		case producer.StateSealed:
			err = d.sealed(ctx, c, seq, info)
		default:
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// releaseTurn releases every acknowledged batch whose turn has come, in
// batch sequence order: batch N is released only after batch N-1 is RETIRED.
// A batch that is not next stays in the outbox until its turn.
func (d *Driver) releaseTurn(ctx context.Context, c *client) error {
	for {
		entry, found, err := d.nextRelease()
		if err != nil || !found {
			return err
		}
		if err := d.release(ctx, c, entry); err != nil {
			return err
		}
	}
}

// nextRelease answers the outbox entry of exactly the next batch, if the
// journal holds one.
func (d *Driver) nextRelease() (producer.OutboxEntry, bool, error) {
	entries, err := d.store.Outbox()
	if err != nil {
		return producer.OutboxEntry{}, false, err
	}
	next := d.store.RetiredPrefix() + 1
	for _, entry := range entries {
		if entry.Seq == next {
			return entry, true, nil
		}
	}
	return producer.OutboxEntry{}, false, nil
}

// release finishes the payload cleanup of an acknowledged batch (step 11),
// asks the gateway for the ordered release (step 12), checks the ReleaseAck
// and records it (step 13).
func (d *Driver) release(ctx context.Context, c *client, entry producer.OutboxEntry) error {
	for {
		done, err := d.store.Cleanup(entry.Seq, cleanupStepKeys)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	ack, err := c.release(ctx, requestID, d.batch(entry.Seq), entry.Ack.ManifestDigest, entry.Ack.PlanDigest)
	if err != nil {
		return err
	}
	got := ack.GetBatch()
	if got == nil || string(got.GetProducerId()) != string(d.id.ProducerID[:]) ||
		string(got.GetRunId()) != string(d.id.RunID[:]) || got.GetBatchSeq() != entry.Seq {
		return fmtError(ErrProtocol, "Release: the acknowledgement does not name batch %d", entry.Seq)
	}
	if ack.GetRetiredPrefix() < entry.Seq {
		return fmtError(ErrProtocol, "Release: acknowledgement prefix %d is below batch %d",
			ack.GetRetiredPrefix(), entry.Seq)
	}
	if err := d.store.OnReleaseAck(entry.Seq); err != nil {
		return err
	}
	if d.report != nil {
		d.report.RetiredPrefix("producer", d.store.RetiredPrefix())
	}
	return nil
}

// batch is the wire identity of one batch.
func (d *Driver) batch(seq uint64) *ingest.BatchId {
	return &ingest.BatchId{ProducerId: d.id.ProducerID[:], RunId: d.id.RunID[:], BatchSeq: seq}
}

// binding is the destination binding digest of the driver.
func (d *Driver) binding() proto.Hash32 { return d.cfg.Binding.DescriptorDigest }

// isCode reports whether err is a typed answer of the registry code.
func isCode(err error, code ingest.ErrorCode) bool {
	var wire *WireError
	return errors.As(err, &wire) && wire.Code == code
}

// notDelivery refuses an answer that claims a batch this journal does not
// hold as acknowledged: it is never taken as a delivery
// (DELIVERY_STATE_MACHINE, "Release и потеря Gateway store").
func (d *Driver) notDelivery(seq uint64, what string) error {
	return fmt.Errorf("%w: batch %d: %s; ALREADY_RETIRED is not a delivery", ErrBlocked, seq, what)
}

// prepare moves a READY batch to SEALED: it repeats the first step the
// durable state allows, from BeginPrepare (step 3) to Seal (step 6).
func (d *Driver) prepare(ctx context.Context, c *client, seq uint64, info producer.BatchInfo) error {
	ready := *info.Ready
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	// A plan an earlier run opened: ask the gateway whether the
	// reservation it was built on survived.
	_, seedBytes, layoutDigest, header, err := d.store.Plan(seq)
	switch {
	case err == nil:
		planDigest := PlanDigest(seedBytes)
		seed, err := checkSeed(seedBytes, planDigest[:], seq, ready, d.id, d.binding())
		if err != nil {
			return err
		}
		if string(header) != d.cfg.Binding.Stream {
			return fmt.Errorf("%w: batch %d: the open plan was begun for stream %q, the binding names %q",
				ErrBlocked, seq, header, d.cfg.Binding.Stream)
		}
		resp, err := c.batchState(ctx, requestID, d.batch(seq), PlanDigest(seedBytes))
		if err != nil {
			return err
		}
		switch resp.GetState() {
		case ingest.BatchState_PREPARED, ingest.BatchState_RECEIVING, ingest.BatchState_CLOSED,
			ingest.BatchState_NATS_ACKED, ingest.BatchState_RELEASING:
			// The reservation survived: the plan continues in its generation.
			return d.sealPlan(seq, ready, seed, layoutDigest)
		case ingest.BatchState_RETIRED:
			return d.notDelivery(seq, "the gateway retired a batch this journal holds as READY")
		case ingest.BatchState_NOT_FOUND, ingest.BatchState_PREPARING:
			// DECISION (contract reference: DELIVERY_STATE_MACHINE,
			// "Prepare и повторные попытки": an unsealed plan is built in a
			// new plan generation after the durable refusal of the old
			// unpublished one, with the READY fragments unchanged): the
			// gateway lost the reservation of a plan that was not sealed.
			// The plan is refused for good and the batch prepared again,
			// which allocates a new reservation.
			if err := d.store.AbandonPlan(seq); err != nil {
				return err
			}
		default:
			return fmtError(ErrProtocol, "GetBatchState: state %s is not a batch state", resp.GetState())
		}
	case errors.Is(err, producer.ErrNotFound):
		// No plan was begun yet.
	default:
		return err
	}
	seed, err := d.prepareNormal(ctx, c, seq, ready, requestID)
	if err != nil {
		return err
	}
	return d.sealPlan(seq, ready, seed, [32]byte{})
}

// readEvents streams the manifest of the READY attempt, checks it against
// the READY marker and binds its event ids to the sequence range the journal
// reserved: a manifest that names other events never reaches the gateway.
func (d *Driver) readEvents(seq uint64, ready producer.Ready) (manifest []byte, events []manifestEvent, err error) {
	manifest, err = d.readManifest(seq, ready)
	if err != nil {
		return nil, nil, err
	}
	events, err = decodeManifest(manifest)
	if err != nil {
		return nil, nil, err
	}
	info, err := d.store.Batch(seq)
	if err != nil {
		return nil, nil, err
	}
	if err := d.checkEventIDs(events, seq, info.Intent.EventSequenceBase); err != nil {
		return nil, nil, err
	}
	return manifest, events, nil
}

// prepareNormal runs steps 3 to 5 for a READY batch in the normal mode.
//
// DECISION (contract reference: CONTROL_WIRE_CONTRACT, the BeginPrepare row
// of the RPC table; gateway/journal/prepare.go, FinalizePrepare): the
// gateway compares descriptor_bytes_total with the covered length of the
// manifest chunk stream and hashes that whole stream against the manifest
// digest, so the chunk stream is the whole manifest and the total is its
// full length, the header of the domain, the zero byte and the event count
// included.
func (d *Driver) prepareNormal(ctx context.Context, c *client, seq uint64, ready producer.Ready, requestID []byte) (Seed, error) {
	manifest, _, err := d.readEvents(seq, ready)
	if err != nil {
		return Seed{}, err
	}
	resp, err := c.beginPrepare(ctx, requestID, &ingest.BeginPrepareRequest{
		Batch: d.batch(seq), ManifestDigest: ready.ManifestDigest[:], EventCount: ready.EventCount,
		DescriptorBytesTotal: uint64(len(manifest)),
	})
	if err != nil {
		if isCode(err, ingest.ErrorCode_ALREADY_RETIRED) {
			return Seed{}, d.notDelivery(seq, "BeginPrepare answered ALREADY_RETIRED for a batch this journal holds as READY")
		}
		return Seed{}, err
	}
	if reservation := resp.GetReservation(); reservation != nil {
		// The gateway is PREPARED: it answers the saved reservation, and
		// the same batch and manifest digest never allocate a second one.
		return checkSeed(reservation.GetSeed(), reservation.GetPlanDigest(), seq, ready, d.id, d.binding())
	}
	if err := c.streamManifest(ctx, requestID, d.batch(seq), ready.ManifestDigest, manifest, resp.GetResumeOffset()); err != nil {
		return Seed{}, err
	}
	fin, err := c.finalizePrepare(ctx, requestID, d.batch(seq), ready.ManifestDigest)
	if err != nil {
		return Seed{}, err
	}
	return checkSeed(fin.GetSeed(), fin.GetPlanDigest(), seq, ready, d.id, d.binding())
}

// importSealed replays a SEALED batch whose gateway record is gone under
// its original reservation: the manifest again in PREPARE_IMPORT_SEALED
// mode with the stored seed, then the stored frames and SealImport.
//
// DECISION (contract reference: CONTROL_WIRE_CONTRACT, the IMPORT_SEALED
// paragraph of the service requests; DELIVERY_STATE_MACHINE, "Prepare и
// повторные попытки": a SEALED plan always replays with its epoch,
// sequence, layout, headers and bytes and allocates no sequence): the
// producer journal refuses AbandonPlan on a SEALED batch, so a lost record
// of a sealed batch is not re-prepared. The reservation, the sequence base
// and the frames do not change.
func (d *Driver) importSealed(ctx context.Context, c *client, seq uint64, ready producer.Ready, seedBytes []byte, requestID []byte) error {
	manifest, err := d.readManifest(seq, ready)
	if err != nil {
		return err
	}
	resp, err := c.beginPrepare(ctx, requestID, &ingest.BeginPrepareRequest{
		Batch: d.batch(seq), ManifestDigest: ready.ManifestDigest[:], EventCount: ready.EventCount,
		DescriptorBytesTotal: uint64(len(manifest)), Mode: ingest.PrepareMode_PREPARE_IMPORT_SEALED,
		ImportSeed: seedBytes,
	})
	if err != nil {
		return err
	}
	if reservation := resp.GetReservation(); reservation != nil {
		if !bytes.Equal(reservation.GetSeed(), seedBytes) {
			return fmt.Errorf("%w: batch %d: BeginPrepare answered another reservation than the imported seed", ErrBlocked, seq)
		}
		return nil
	}
	if err := c.streamManifest(ctx, requestID, d.batch(seq), ready.ManifestDigest, manifest, resp.GetResumeOffset()); err != nil {
		return err
	}
	fin, err := c.finalizePrepare(ctx, requestID, d.batch(seq), ready.ManifestDigest)
	if err != nil {
		return err
	}
	if !bytes.Equal(fin.GetSeed(), seedBytes) {
		return fmt.Errorf("%w: batch %d: FinalizePrepare answered another reservation than the imported seed", ErrBlocked, seq)
	}
	return nil
}

// sealed moves a SEALED batch to ACKED_CLEANUP (steps 7 to 11): it asks the
// gateway where the batch stands, publishes the sealed plan, closes the
// batch and records the acknowledgement once all six fields equal the
// producer's records.
func (d *Driver) sealed(ctx context.Context, c *client, seq uint64, info producer.BatchInfo) error {
	_, seedBytes, _, _, err := d.store.Plan(seq)
	if err != nil {
		return err
	}
	seed, err := checkSeed(seedBytes, info.PlanDigest[:], seq, *info.Ready, d.id, d.binding())
	if err != nil {
		return err
	}
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.batchState(ctx, requestID, d.batch(seq), info.PlanDigest)
		if err != nil {
			return err
		}
		imported := false
		switch resp.GetState() {
		case ingest.BatchState_CLOSED, ingest.BatchState_NATS_ACKED, ingest.BatchState_RELEASING:
			acked, err := d.awaitAck(ctx, c, seq, info, seed, requestID)
			if err != nil || acked {
				return err
			}
			continue
		case ingest.BatchState_RETIRED:
			return d.notDelivery(seq, "the gateway retired a batch this journal holds as SEALED")
		case ingest.BatchState_NOT_FOUND, ingest.BatchState_PREPARING:
			// DECISION (contract reference: the RPC table of
			// CONTROL_WIRE_CONTRACT): NOT_FOUND is a value of GetBatchState,
			// not of BeginPublish. The driver asks GetBatchState before it
			// decides, so a lost record is seen whatever code BeginPublish
			// would answer. A PREPARING record is an import an earlier run
			// did not finish.
			imported = true
			if err := d.importSealed(ctx, c, seq, *info.Ready, seedBytes, requestID); err != nil {
				return err
			}
		case ingest.BatchState_PREPARED, ingest.BatchState_RECEIVING:
		default:
			return fmtError(ErrProtocol, "GetBatchState: state %s is not a batch state", resp.GetState())
		}
		frames, entryCount, err := d.publishable(seq, info, seed)
		if err != nil {
			return err
		}
		err = c.publishFrames(ctx, requestID, publishRequest{
			batch: d.batch(seq), seed: seedBytes, planDigest: info.PlanDigest,
			layoutDigest: info.LayoutDigest, entryCount: entryCount, frames: frames,
		})
		if isCode(err, ingest.ErrorCode_ALREADY_RETIRED) {
			return d.notDelivery(seq, "the gateway answered ALREADY_RETIRED for a batch this journal holds as SEALED")
		}
		var wire *WireError
		if errors.As(err, &wire) && wire.Step == "BeginPublish" && wire.Code == ingest.ErrorCode_IDENTITY_MISMATCH {
			// The record left the gateway between the probe and this call:
			// the next pass probes again.
			continue
		}
		if err != nil {
			return err
		}
		if err := c.closeBatch(ctx, requestID, d.batch(seq), info.PlanDigest,
			info.Ready.ManifestDigest, info.LayoutDigest, imported); err != nil {
			return err
		}
	}
}

// awaitAck polls the gateway for the acknowledgement of a closed batch
// (step 10) and records it (step 11). The poll stops when the answer carries
// an IngestAck; all six fields are checked against the producer's records
// before anything is recorded. CLOSED without an acknowledgement is not a
// delivery. acked is false when the gateway lost the record of the batch:
// it is still unacknowledged here and is published again.
func (d *Driver) awaitAck(ctx context.Context, c *client, seq uint64, info producer.BatchInfo, seed Seed, requestID []byte) (acked bool, err error) {
	for {
		resp, err := c.batchState(ctx, requestID, d.batch(seq), info.PlanDigest)
		if err != nil {
			return false, err
		}
		switch resp.GetState() {
		case ingest.BatchState_NATS_ACKED, ingest.BatchState_RELEASING:
			ack := resp.GetAck()
			if ack == nil {
				return false, fmtError(ErrProtocol, "GetBatchState: %s without an IngestAck", resp.GetState())
			}
			got := producer.IngestAck{
				ManifestDigest: [32]byte(hash32Of(ack.GetManifestDigest())),
				PlanDigest:     [32]byte(hash32Of(ack.GetPlanDigest())),
				LayoutDigest:   [32]byte(hash32Of(ack.GetLayoutManifestDigest())),
				BindingDigest:  [32]byte(hash32Of(ack.GetBindingDigest())),
				IssuerEpoch:    id16Of(ack.GetIssuerStoreEpoch()),
			}
			want := producer.IngestAck{ManifestDigest: info.Ready.ManifestDigest, PlanDigest: info.PlanDigest,
				LayoutDigest: info.LayoutDigest, BindingDigest: [32]byte(d.binding()),
				IssuerEpoch: [16]byte(seed.IssuerEpoch)}
			batch := ack.GetBatch()
			if got != want || batch == nil || batch.GetBatchSeq() != seq ||
				string(batch.GetProducerId()) != string(d.id.ProducerID[:]) ||
				string(batch.GetRunId()) != string(d.id.RunID[:]) {
				return false, fmt.Errorf("%w: batch %d: got %+v", ErrIngestAckMismatch, seq, got)
			}
			if err := d.store.OnIngestAck(seq, got); err != nil {
				return false, err
			}
			if d.report != nil {
				d.report.DeliveryBatch(seq, config.StateDeliveryAckedCleanup)
			}
			return true, nil
		case ingest.BatchState_CLOSED:
			// Published, not yet acknowledged by NATS: poll again.
		case ingest.BatchState_NOT_FOUND, ingest.BatchState_PREPARING,
			ingest.BatchState_PREPARED, ingest.BatchState_RECEIVING:
			return false, nil
		case ingest.BatchState_RETIRED:
			return false, d.notDelivery(seq, "the gateway retired a batch this journal holds as SEALED")
		default:
			return false, fmtError(ErrProtocol, "GetBatchState: state %s is not a batch state", resp.GetState())
		}
		if err := d.poll(ctx); err != nil {
			return false, err
		}
	}
}

// poll waits one poll interval or until ctx ends.
func (d *Driver) poll(ctx context.Context) error {
	timer := time.NewTimer(d.cfg.PollEvery)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readManifest streams the manifest of the READY attempt into memory and
// checks it against the READY marker. The visitor copies each chunk: a
// chunk belongs to the store and the visitor runs under the store mutex.
func (d *Driver) readManifest(seq uint64, ready producer.Ready) ([]byte, error) {
	var manifest []byte
	err := d.store.AttemptManifest(seq, ready.Attempt, func(_ uint64, chunk []byte) error {
		manifest = append(manifest, chunk...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := verifyManifest(manifest, ready); err != nil {
		return nil, err
	}
	return manifest, nil
}

// readAttemptData streams the attempt data of the READY attempt into memory.
func (d *Driver) readAttemptData(seq uint64, ready producer.Ready) ([]byte, error) {
	var data []byte
	err := d.store.AttemptData(seq, ready.Attempt, func(_ uint64, chunk []byte) error {
		data = append(data, chunk...)
		return nil
	})
	return data, err
}

// sealPlan runs step 6: the layout of the reservation, the frames over the
// immutable attempt data, and the plan chunks of one generation up to the
// closing Seal. knownLayout is the layout digest of a plan an earlier run
// opened, zero for a new one. Nothing is sealed before the frames exist, so
// a frame the budget cannot carry leaves the batch READY.
func (d *Driver) sealPlan(seq uint64, ready producer.Ready, seed Seed, knownLayout [32]byte) error {
	_, events, err := d.readEvents(seq, ready)
	if err != nil {
		return err
	}
	data, err := d.readAttemptData(seq, ready)
	if err != nil {
		return err
	}
	plan, err := layout(events, seed, d.cfg.Binding)
	if err != nil {
		return err
	}
	layoutDigest, err := proto.LayoutManifestDigest(PlanDigest(seed.raw), plan.Entries)
	if err != nil {
		return err
	}
	if knownLayout != ([32]byte{}) && knownLayout != layoutDigest {
		return fmt.Errorf("%w: batch %d: the open plan carries layout %x, the reservation gives %x",
			ErrBlocked, seq, knownLayout, layoutDigest)
	}
	frames, err := buildFrames(events, plan, data, seed, d.cfg.Binding)
	if err != nil {
		return err
	}
	payload := encodePlan(frames)
	generation, _, err := d.store.BeginPlan(seq, seed.raw, layoutDigest, []byte(d.cfg.Binding.Stream))
	if err != nil {
		return err
	}
	// The journal answers a conflicting rewrite and a hole; an identical
	// chunk of an earlier run is a no-op, so every chunk is written again.
	for offset := 0; offset < len(payload); offset += producer.ChunkBytes {
		end := min(offset+producer.ChunkBytes, len(payload))
		if err := d.store.WritePlanChunk(seq, generation, uint64(offset), payload[offset:end]); err != nil {
			return err
		}
	}
	if _, err := d.store.Seal(seq, generation, uint64(len(payload))); err != nil {
		return err
	}
	if d.report != nil {
		d.report.DeliveryBatch(seq, config.StateDeliverySealed)
	}
	return nil
}

// checkEventIDs requires that every event of the manifest carries the event
// id the journal reserved for its ordinal: after READY the event identity is
// immutable.
func (d *Driver) checkEventIDs(events []manifestEvent, seq, eventSequenceBase uint64) error {
	for i := range events {
		eventSequence := eventSequenceBase + events[i].Ordinal
		if eventSequence < eventSequenceBase {
			return fmt.Errorf("%w: batch %d: event sequence overflow", producer.ErrCounterOverflow, seq)
		}
		if want := (proto.EventID)(d.store.EventID(eventSequence)); events[i].EventID != want {
			return fmt.Errorf("%w: batch %d: event %d carries another event id than its ordinal reserved",
				ErrJournalData, seq, events[i].Ordinal)
		}
	}
	return nil
}

// publishable reads the spooled plan of a sealed batch, checks it against
// the manifest, the reservation and the binding, and answers the frames to
// publish with the entry count the gateway declared.
func (d *Driver) publishable(seq uint64, info producer.BatchInfo, seed Seed) ([]planFrame, uint64, error) {
	_, events, err := d.readEvents(seq, *info.Ready)
	if err != nil {
		return nil, 0, err
	}
	plan, err := layout(events, seed, d.cfg.Binding)
	if err != nil {
		return nil, 0, err
	}
	var payload []byte
	err = d.store.PlanChunks(seq, info.PlanGeneration, func(_ uint64, chunk []byte) error {
		payload = append(payload, chunk...)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	stored, err := decodePlan(payload)
	if err != nil {
		return nil, 0, err
	}
	frames, err := checkPlan(stored, events, plan, seed, d.cfg.Binding)
	if err != nil {
		return nil, 0, err
	}
	return frames, uint64(len(plan.Entries)), nil
}

// hash32Of reads a 32-byte digest of the wire.
func hash32Of(b []byte) proto.Hash32 {
	var h proto.Hash32
	if len(b) == len(h) {
		copy(h[:], b)
	}
	return h
}

// id16Of reads a 16-byte identity of the wire.
func id16Of(b []byte) proto.ID16 {
	var id proto.ID16
	if len(b) == len(id) {
		copy(id[:], b)
	}
	return id
}
