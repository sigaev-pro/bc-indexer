// This overlay is compiled without upstream server/*_test.go initialization.
// Production source and native methods remain unchanged; only explicit gates delegate.
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

const fencePayload = "source-fence-payload"

type fenceEvent struct {
	Ordinal int    `json:"ordinal"`
	Name    string `json:"name"`
}

type fenceCase struct {
	t       *testing.T
	dir     string
	servers []*Server
	clients []*nats.Conn
	gates   []*fenceGate
	restore []func()
	mu      sync.Mutex
	events  []fenceEvent
	obs     map[string]any
	groups  map[string]string
}

func fenceCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fenceWrite(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	fenceCheck(t, err)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	fenceCheck(t, err)
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	fenceCheck(t, err)
	fenceCheck(t, closeErr)
}

func newFenceCase(t *testing.T, replicas int) *fenceCase {
	t.Helper()
	dir := os.Getenv("EXTRACTOR_FENCE_OUTPUT")
	if !filepath.IsAbs(dir) {
		t.Fatal("EXTRACTOR_FENCE_OUTPUT must be an owned absolute directory")
	}
	f := &fenceCase{t: t, dir: dir, obs: map[string]any{}, groups: map[string]string{}}
	// Register cleanup before starting any server. Gates always open first.
	t.Cleanup(f.finish)
	for i := 1; i <= replicas; i++ {
		name := fmt.Sprintf("fence-S-%d", i)
		store := filepath.Join(dir, "stores", name)
		fenceCheck(t, os.MkdirAll(store, 0700))
		config := fmt.Sprintf("listen: 127.0.0.1:-1\nserver_name: %s\njetstream { store_dir: %q, max_mem_store: 16777216, max_file_store: 67108864 }\n", name, store)
		if replicas == 3 {
			config += fmt.Sprintf("cluster { name: EXTRACTOR, listen: 127.0.0.1:%d, routes: [nats-route://127.0.0.1:19021,nats-route://127.0.0.1:19022,nats-route://127.0.0.1:19023] }\naccounts { $SYS { users: [{user: harness, password: local-fixture-only}] } }\n", 19020+i)
		}
		path := filepath.Join(dir, name+".conf")
		fenceCheck(t, os.WriteFile(path, []byte(config), 0600))
		opts, err := ProcessConfigFile(path)
		fenceCheck(t, err)
		opts.NoSigs = true // The owned test process, not each embedded server, owns signals.
		s, err := NewServer(opts)
		fenceCheck(t, err)
		f.servers = append(f.servers, s)
		go s.Start()
		if !s.ReadyForConnections(10 * time.Second) {
			t.Fatal("native server did not become ready")
		}
	}
	if replicas == 3 {
		f.wait("natural metadata election", 40*time.Second, func() bool {
			for _, s := range f.servers {
				js := s.getJetStream()
				if js != nil && js.getMetaGroup() != nil && js.getMetaGroup().Leader() {
					return true
				}
			}
			return false
		})
	}
	identities := []map[string]any{}
	for _, s := range f.servers {
		identities = append(identities, map[string]any{"server_name": s.Name(), "info_id": s.ID(), "native_member_id": getHash(s.Name()), "no_signals": s.getOpts().NoSigs})
	}
	fenceWrite(t, filepath.Join(dir, "identities.json"), identities)
	return f
}

func (f *fenceCase) finish() {
	for _, gate := range f.gates {
		gate.open()
	}
	for _, nc := range f.clients {
		nc.Close()
	}
	for _, restore := range f.restore {
		restore()
	}
	for _, s := range f.servers {
		s.Shutdown()
	}
	for _, s := range f.servers {
		s.WaitForShutdown()
	}
	outcome := "PASS"
	if f.t.Failed() {
		outcome = "FAIL"
	}
	fenceWrite(f.t, filepath.Join(f.dir, "report.json"), map[string]any{
		"schema_version": 1, "kind": "nats-source-conformance", "case": f.t.Name(), "outcome": outcome,
		"cleanup": map[string]bool{"shutdown_complete": true}, "events": f.events, "observations": f.obs,
	})
}

func (f *fenceCase) event(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fenceEvent{len(f.events) + 1, name})
}

func (f *fenceCase) wait(label string, duration time.Duration, condition func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("bounded native condition was not reached: " + label)
}

func (f *fenceCase) connect(s *Server) (*nats.Conn, nats.JetStreamContext) {
	nc, err := nats.Connect(s.ClientURL(), nats.NoReconnect(), nats.Timeout(10*time.Second))
	fenceCheck(f.t, err)
	f.clients = append(f.clients, nc)
	js, err := nc.JetStream(nats.MaxWait(45 * time.Second))
	fenceCheck(f.t, err)
	return nc, js
}

func (f *fenceCase) stream(replicas int) (nats.JetStreamContext, *stream) {
	_, js := f.connect(f.servers[0])
	_, err := js.AddStream(&nats.StreamConfig{Name: "FENCE", Subjects: []string{"fence.input"}, Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Discard: nats.DiscardNew, MaxMsgs: 100, MaxBytes: 1048576, Replicas: replicas,
		DenyDelete: true, DenyPurge: true})
	fenceCheck(f.t, err)
	var mset *stream
	f.wait("native stream leader", 20*time.Second, func() bool {
		for _, s := range f.servers {
			candidate, err := s.globalAccount().lookupStream("FENCE")
			if err == nil && candidate.IsLeader() {
				mset = candidate
				return true
			}
		}
		return false
	})
	return js, mset
}

func (f *fenceCase) message(js nats.JetStreamContext) {
	message, err := js.GetMsg("FENCE", 1)
	fenceCheck(f.t, err)
	f.obs["message"] = map[string]any{"subject": message.Subject, "sequence": message.Sequence, "data": message.Data}
}

func fenceLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	return string(line), err
}

func (f *fenceCase) seal(js nats.JetStreamContext) *nats.StreamInfo {
	info, err := js.StreamInfo("FENCE")
	fenceCheck(f.t, err)
	config := info.Config
	config.Sealed = true
	sealed, err := js.UpdateStream(&config)
	fenceCheck(f.t, err)
	return sealed
}

type fenceGate struct {
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	openOnce sync.Once
	released atomic.Bool
	f        *fenceCase
	event    string
}

func (f *fenceCase) gate(event string) *fenceGate {
	g := &fenceGate{entered: make(chan struct{}), release: make(chan struct{}), f: f, event: event}
	f.gates = append(f.gates, g)
	return g
}

func (g *fenceGate) block() error {
	g.once.Do(func() { g.f.event(g.event); close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("native fault gate exceeded its bound")
	}
}

func (g *fenceGate) await() {
	select {
	case <-g.entered:
	case <-time.After(15 * time.Second):
		g.f.t.Fatal("native fault gate was not reached")
	}
}

func (g *fenceGate) open() { g.openOnce.Do(func() { g.released.Store(true); close(g.release) }) }

type fenceStore struct {
	StreamStore
	gate *fenceGate
	raw  bool
}

func (s *fenceStore) StoreMsg(subject string, hdr, msg []byte, ttl int64) (uint64, int64, error) {
	if !s.raw && subject == "fence.input" {
		if err := s.gate.block(); err != nil {
			return 0, 0, err
		}
	}
	return s.StreamStore.StoreMsg(subject, hdr, msg, ttl)
}

func (s *fenceStore) StoreRawMsg(subject string, hdr, msg []byte, seq uint64, ts int64, ttl int64, discard bool) error {
	if s.raw && subject == "fence.input" {
		if err := s.gate.block(); err != nil {
			return err
		}
	}
	return s.StreamStore.StoreRawMsg(subject, hdr, msg, seq, ts, ttl, discard)
}

type fenceProposal struct {
	RaftNode
	gate *fenceGate
}

func (n *fenceProposal) Propose(term uint64, entry []byte) error {
	if bytes.Contains(entry, []byte(fencePayload)) {
		if err := n.gate.block(); err != nil {
			return err
		}
	}
	return n.RaftNode.Propose(term, entry)
}

func fencePublish(js nats.JetStreamContext) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := js.Publish("fence.input", []byte(fencePayload)); done <- err }()
	return done
}

func (f *fenceCase) published(done <-chan error) {
	select {
	case err := <-done:
		fenceCheck(f.t, err)
	case <-time.After(15 * time.Second):
		f.t.Fatal("native publish did not finish")
	}
}

func (f *fenceCase) binding(node RaftNode) map[string]any {
	native, ok := node.(*raft)
	if !ok {
		f.t.Fatal("expected native stream Raft node before instrumentation")
	}
	native.RLock()
	account := native.accName
	native.RUnlock()
	f.groups[node.Group()] = account
	voters := map[string]string{}
	for _, s := range f.servers {
		voters[s.Name()] = getHash(s.Name())
	}
	return map[string]any{"account": account, "group": node.Group(), "traffic_account": node.GetTrafficAccountName(),
		"system_account": node.IsSystemAccount(), "voters": voters}
}

func (f *fenceCase) collect(group string) map[string]*RaftzStatus {
	account, ok := f.groups[group]
	if !ok || account == "" {
		f.t.Fatal("native account/group binding was not frozen")
	}
	rows := map[string]*RaftzStatus{}
	for _, s := range f.servers {
		rows[s.Name()] = s.Raftz(&RaftzOptions{AccountFilter: account, GroupFilter: group})
	}
	return rows
}

func stableFenceRows(rows map[string]*RaftzStatus, account, group string) bool {
	var leader string
	var term, index, pterm uint64
	for _, raw := range rows {
		if raw == nil {
			return false
		}
		row, ok := (*raw)[account][group]
		if !ok || row.Leader == "" || row.Paused || row.CatchingUp || row.Overrun || row.Observer || row.WALError != nil ||
			row.PIndex != row.Committed || row.PIndex != row.Applied || row.IPQPropLen != 0 || row.IPQEntryLen != 0 || row.IPQApplyLen != 0 || row.IPQRespLen != 0 {
			return false
		}
		if leader == "" {
			leader, term, index, pterm = row.Leader, row.Term, row.PIndex, row.PTerm
		}
		if leader != row.Leader || term != row.Term || index != row.PIndex || pterm != row.PTerm {
			return false
		}
	}
	return len(rows) == 3
}

func (f *fenceCase) stable(group string) map[string]*RaftzStatus {
	var rows map[string]*RaftzStatus
	f.wait("native stream frontier", 20*time.Second, func() bool { rows = f.collect(group); return stableFenceRows(rows, f.groups[group], group) })
	return rows
}

func TestExtractorFenceStandalone(t *testing.T) {
	f := newFenceCase(t, 1)
	js, mset := f.stream(1)
	gate := f.gate("storage_entered")
	mset.mu.Lock()
	original := mset.store
	wrapped := &fenceStore{StreamStore: original, gate: gate}
	mset.store = wrapped
	mset.mu.Unlock()
	f.restore = append(f.restore, func() {
		mset.mu.Lock()
		defer mset.mu.Unlock()
		if mset.store == wrapped {
			mset.store = original
		}
	})
	published := fencePublish(js)
	gate.await()
	locked := !mset.mu.TryLock()
	if !locked {
		mset.mu.Unlock()
		t.Fatal("native storage no longer holds the stream lock")
	}
	f.obs["mutex_locked"] = locked
	started := make(chan struct{})
	type sealResult struct {
		info     *nats.StreamInfo
		err      error
		released bool
	}
	done := make(chan sealResult, 1)
	// Use the already known complete configuration; StreamInfo itself takes the held lock.
	config := nats.StreamConfig{Name: "FENCE", Subjects: []string{"fence.input"}, Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Discard: nats.DiscardNew, MaxMsgs: 100, MaxBytes: 1048576, Replicas: 1,
		DenyDelete: true, DenyPurge: true, Sealed: true}
	go func() {
		f.event("seal_started")
		close(started)
		info, err := js.UpdateStream(&config)
		done <- sealResult{info, err, gate.released.Load()}
	}()
	<-started
	f.event("storage_released")
	gate.open()
	f.published(published)
	select {
	case result := <-done:
		fenceCheck(t, result.err)
		f.obs["stream"] = result.info
		f.obs["seal_after_release"] = result.released
	case <-time.After(15 * time.Second):
		t.Fatal("native seal did not finish")
	}
	f.event("seal_completed")
	f.message(js)
}

func TestExtractorFencePreProposal(t *testing.T) {
	f := newFenceCase(t, 3)
	js, mset := f.stream(3)
	gate := f.gate("proposal_entered")
	mset.mu.Lock()
	original := mset.node
	wrapped := &fenceProposal{RaftNode: original, gate: gate}
	mset.node = wrapped
	mset.mu.Unlock()
	f.restore = append(f.restore, func() {
		mset.mu.Lock()
		defer mset.mu.Unlock()
		if mset.node == wrapped {
			mset.node = original
		}
	})
	f.obs["binding"] = f.binding(original)
	published := fencePublish(js)
	gate.await()
	f.obs["stream"] = f.seal(js)
	f.event("seal_completed")
	f.event("proposal_released")
	gate.open()
	f.published(published)
	f.event("publish_completed")
	f.obs["recovered"] = f.stable(original.Group())
	f.message(js)
}

func TestExtractorFenceDelayedApply(t *testing.T) {
	f := newFenceCase(t, 3)
	js, mset := f.stream(3)
	gate := f.gate("apply_entered")
	mset.mu.Lock()
	node := mset.node
	original := mset.store
	wrapped := &fenceStore{StreamStore: original, gate: gate, raw: true}
	mset.store = wrapped
	mset.mu.Unlock()
	f.restore = append(f.restore, func() {
		mset.mu.Lock()
		defer mset.mu.Unlock()
		if mset.store == wrapped {
			mset.store = original
		}
	})
	f.obs["binding"] = f.binding(node)
	published := fencePublish(js)
	gate.await()
	var blocked map[string]*RaftzStatus
	f.wait("dequeued committed native entry", 10*time.Second, func() bool {
		blocked = f.collect(node.Group())
		for _, raw := range blocked {
			if raw == nil {
				continue
			}
			row := (*raw)[f.groups[node.Group()]][node.Group()]
			if row.Committed > row.Applied && row.IPQApplyLen == 0 && row.IPQPropLen == 0 && row.IPQEntryLen == 0 {
				return true
			}
		}
		return false
	})
	f.obs["blocked"] = blocked
	f.event("blocked_sample")
	f.event("apply_released")
	gate.open()
	f.published(published)
	f.obs["recovered_before"] = f.stable(node.Group())
	f.obs["recovered_after"] = f.collect(node.Group())
	f.event("recovered_sample")
}

func TestExtractorFenceLostAck(t *testing.T) {
	f := newFenceCase(t, 3)
	js, mset := f.stream(3)
	mset.mu.RLock()
	node := mset.node
	mset.mu.RUnlock()
	f.obs["binding"] = f.binding(node)
	address, err := url.Parse(f.servers[0].ClientURL())
	fenceCheck(t, err)
	conn, err := net.DialTimeout("tcp", address.Host, 10*time.Second)
	fenceCheck(t, err)
	defer conn.Close()
	fenceCheck(t, conn.SetDeadline(time.Now().Add(15*time.Second)))
	reader := bufio.NewReaderSize(conn, 65536)
	line, err := fenceLine(reader)
	fenceCheck(t, err)
	if !strings.HasPrefix(line, "INFO ") || len(line) > 65536 {
		t.Fatal("missing native INFO")
	}
	_, err = io.WriteString(conn, "CONNECT {\"verbose\":false,\"pedantic\":true}\r\nSUB _INBOX.EXTRACTOR 1\r\nPING\r\n")
	fenceCheck(t, err)
	for {
		line, err = fenceLine(reader)
		fenceCheck(t, err)
		if line == "PONG\r\n" {
			break
		}
		if len(line) > 65536 || strings.HasPrefix(line, "-ERR") {
			t.Fatal("native publisher setup rejected")
		}
	}
	frame := []byte(fmt.Sprintf("PUB fence.input _INBOX.EXTRACTOR %d\r\n%s\r\n", len(fencePayload), fencePayload))
	written, err := conn.Write(frame)
	fenceCheck(t, err)
	if written != len(frame) {
		t.Fatal("publication frame was only partially forwarded")
	}
	f.obs["request_frame"] = frame
	f.event("request_forwarded")
	line, err = fenceLine(reader)
	fenceCheck(t, err)
	fields := strings.Fields(line)
	if len(fields) != 4 || fields[0] != "MSG" || fields[1] != "_INBOX.EXTRACTOR" || fields[2] != "1" {
		t.Fatal("uncorrelated native response")
	}
	size, err := strconv.Atoi(fields[3])
	fenceCheck(t, err)
	if size < 1 || size > 65536 {
		t.Fatal("native response exceeds bound")
	}
	body := make([]byte, size+2)
	_, err = io.ReadFull(reader, body)
	fenceCheck(t, err)
	f.obs["response_frame"] = append([]byte(line), body...)
	f.event("response_observed")
	// This explicit test ingress observes the server response before suppressing
	// delivery to the application channel. No retry acknowledgment is substituted.
	application := make(chan []byte)
	f.obs["ack_forwarded"] = false
	f.event("response_dropped")
	select {
	case <-application:
		t.Fatal("suppressed acknowledgment reached the application")
	case <-time.After(20 * time.Millisecond):
		f.event("application_timeout")
	}
	f.message(js)
	f.obs["recovered"] = f.stable(node.Group())
}

func TestExtractorFenceLeaderChange(t *testing.T) {
	f := newFenceCase(t, 3)
	_, mset := f.stream(3)
	mset.mu.RLock()
	node := mset.node
	mset.mu.RUnlock()
	f.obs["binding"] = f.binding(node)
	initial := f.stable(node.Group())
	f.obs["initial"] = initial
	f.event("initial_sample")
	oldLeader := node.ID()
	preferred := ""
	for _, s := range f.servers {
		if identifier := getHash(s.Name()); identifier != oldLeader {
			preferred = identifier
			break
		}
	}
	f.obs["preferred_member"] = preferred
	fenceCheck(t, node.StepDown(preferred))
	f.event("stepdown_requested")
	var changed map[string]*RaftzStatus
	f.wait("different naturally elected stream leader", 30*time.Second, func() bool {
		changed = f.collect(node.Group())
		if !stableFenceRows(changed, f.groups[node.Group()], node.Group()) {
			return false
		}
		for _, raw := range changed {
			if (*raw)[f.groups[node.Group()]][node.Group()].Leader == oldLeader {
				return false
			}
		}
		return true
	})
	f.obs["changed"] = changed
	f.event("changed_sample")
	f.obs["stable"] = f.collect(node.Group())
	f.event("stable_sample")
}
