package eth

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Local aliases that keep the balance model import in one place.
type (
	effectAlias        = balance.BalanceEffect
	balanceEffectValue = balance.BalanceEffect
)

// balanceEngine rebuilds the engine enum from its wire value.
func balanceEngine(v uint8) balance.Engine { return balance.Engine(v) }

// blobStorer moves one byte value into the import path and returns its
// wire reference. The producer-side implementation streams the value as a
// TagBytes capture field; the collector streams it into the byte store.

// wireBytesOf encodes one byte value for the wire: inline below the
// threshold, stored above it.
func wireBytesOf(b []byte, store blobStorer) (wireBytes, error) {
	if len(b) < BlobInlineMax {
		return wireBytes{Hex: encodeHex(b)}, nil
	}
	return store(b)
}

// observation rebuilds the typed block observation of a block event,
// reading the header blobs through resolver.
func (w *wireBlock) observation(resolver blobResolver) (BlockObservation, error) {
	fail := func(err error) (BlockObservation, error) {
		return BlockObservation{}, fmt.Errorf("%w: %v", ErrImport, err)
	}
	hash, err := parseHash(w.Hash)
	if err != nil {
		return fail(err)
	}
	h := &w.Header
	header := NativeHeader{Number: h.Number, GasLimit: h.GasLimit, GasUsed: h.GasUsed, Time: h.Time}
	if header.ParentHash, err = parseHash(h.ParentHash); err != nil {
		return fail(err)
	}
	if header.UncleHash, err = parseHash(h.UncleHash); err != nil {
		return fail(err)
	}
	if header.Beneficiary, err = parseAddr(h.Beneficiary); err != nil {
		return fail(err)
	}
	if header.StateRoot, err = parseHash(h.StateRoot); err != nil {
		return fail(err)
	}
	if header.TransactionsRoot, err = parseHash(h.TransactionsRoot); err != nil {
		return fail(err)
	}
	if header.ReceiptsRoot, err = parseHash(h.ReceiptsRoot); err != nil {
		return fail(err)
	}
	bloom, err := decodeFixedHex(h.LogsBloom, 256)
	if err != nil {
		return fail(err)
	}
	copy(header.LogsBloom[:], bloom)
	if header.Difficulty, err = parseU256(h.Difficulty); err != nil {
		return fail(err)
	}
	if header.MixDigest, err = parseHash(h.MixDigest); err != nil {
		return fail(err)
	}
	nonce, err := decodeFixedHex(h.Nonce, 8)
	if err != nil {
		return fail(err)
	}
	copy(header.Nonce[:], nonce)
	if h.BaseFee != nil {
		v, err := parseU256(*h.BaseFee)
		if err != nil {
			return fail(err)
		}
		header.BaseFee = Some(v)
	}
	rootFields := []struct {
		text *string
		dst  *Optional[Hash32]
	}{
		{h.WithdrawalsRoot, &header.WithdrawalsRoot},
		{h.ParentBeaconRoot, &header.ParentBeaconBlockRoot},
		{h.RequestsHash, &header.RequestsHash},
		{h.BlockAccessHash, &header.BlockAccessListHash},
	}
	for _, f := range rootFields {
		if f.text == nil {
			continue
		}
		v, err := parseHash(*f.text)
		if err != nil {
			return fail(err)
		}
		*f.dst = Some(v)
	}
	if h.BlobGasUsed != nil {
		header.BlobGasUsed = Some(*h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		header.ExcessBlobGas = Some(*h.ExcessBlobGas)
	}
	if h.SlotNumber != nil {
		header.SlotNumber = Some(*h.SlotNumber)
	}
	if header.ExtraData, err = resolver(h.ExtraData); err != nil {
		return fail(err)
	}
	if header.SourceRLP, err = resolver(h.SourceRLP); err != nil {
		return fail(err)
	}
	if len(header.SourceRLP) == 0 {
		return fail(fmt.Errorf("empty header source_rlp"))
	}
	return BlockObservation{Hash: hash, Number: w.Number, Header: header, TransactionCount: w.TxCount}, nil
}

// observation rebuilds the typed outside-tx observation.
func (w *wireOutside) observation() OutsideTxObservation {
	out := OutsideTxObservation{Kind: OptionalRecordKind(w.Kind), NativeOrder: w.NativeOrder}
	for _, e := range w.Effects {
		addr, err := parseAddr(e.Address)
		before, err2 := parseU256(e.Before)
		after, err3 := parseU256(e.After)
		if err != nil || err2 != nil || err3 != nil {
			continue
		}
		out.Effects = append(out.Effects, SystemBalanceEffect{Address: addr, Before: before, After: after})
	}
	return out
}

// observation rebuilds the typed transaction observation, reading every
// referenced blob through resolver and re-checking it against its content
// key.
func (w *wireTx) observation(resolver blobResolver) (TxObservation, error) {
	fail := func(err error) (TxObservation, error) {
		return TxObservation{}, fmt.Errorf("%w: %v", ErrImport, err)
	}
	obs := TxObservation{NativeType: w.Type}
	hash, err := parseHash(w.Hash)
	if err != nil {
		return fail(err)
	}
	obs.Inclusion = Inclusion{NativePosition: w.Position, TransactionHash: hash}

	for _, raw := range w.Raw {
		data, err := resolver(raw.Data)
		if err != nil {
			return fail(err)
		}
		obs.RawEncodings = append(obs.RawEncodings, RawEncoding{Kind: RawEncodingKind(raw.Kind), Bytes: data})
	}

	p := &w.Projection
	sender, err := parseAddr(p.Sender)
	if err != nil {
		return fail(err)
	}
	obs.Projection = EvmMessage{
		Sender:   Effective[Address20]{Value: sender, Provenance: Provenance(p.SenderProv)},
		Value:    Effective[UInt256]{Provenance: Provenance(p.ValueProv)},
		Input:    Effective[[]byte]{Provenance: Provenance(p.InputProv)},
		Nonce:    Effective[uint64]{Value: p.Nonce, Provenance: Provenance(p.NonceProv)},
		GasLimit: p.GasLimit,
	}
	if obs.Projection.Value.Value, err = parseU256(p.Value); err != nil {
		return fail(err)
	}
	if obs.Projection.Input.Value, err = resolver(p.Input); err != nil {
		return fail(err)
	}
	if p.Target != nil {
		target, err := parseAddr(*p.Target)
		if err != nil {
			return fail(err)
		}
		obs.Projection.Target = Some(Effective[Address20]{Value: target, Provenance: Provenance(p.TargetProv)})
	}
	if obs.Projection.EffectiveGasPrice, err = parseU256(p.EffectivePrice); err != nil {
		return fail(err)
	}

	r := &w.Receipt
	receipt := Receipt{Result: Result(r.Result), CumulativeGasUsed: r.Cumulative}
	if r.Status != nil {
		receipt.Status = Some(*r.Status)
	}
	if r.PostRoot != nil {
		root, err := parseHash(*r.PostRoot)
		if err != nil {
			return fail(err)
		}
		receipt.PostStateRoot = Some(root)
	}
	if r.GasUsed != nil {
		receipt.GasUsed = Some(*r.GasUsed)
	}
	if r.EffectivePrice != nil {
		v, err := parseU256(*r.EffectivePrice)
		if err != nil {
			return fail(err)
		}
		receipt.EffectiveGasPrice = Some(v)
	}
	if r.BlobGasUsed != nil {
		receipt.BlobGasUsed = Some(*r.BlobGasUsed)
	}
	if r.BlobGasPrice != nil {
		v, err := parseU256(*r.BlobGasPrice)
		if err != nil {
			return fail(err)
		}
		receipt.BlobGasPrice = Some(v)
	}
	bloom, err := decodeFixedHex(r.Bloom, 256)
	if err != nil {
		return fail(err)
	}
	copy(receipt.LogsBloom[:], bloom)
	if r.Attempted != nil {
		v, err := parseAddr(*r.Attempted)
		if err != nil {
			return fail(err)
		}
		receipt.AttemptedCreateAddress = &v
	}
	if r.Created != nil {
		v, err := parseAddr(*r.Created)
		if err != nil {
			return fail(err)
		}
		receipt.CreatedContract = &v
	}
	for _, wl := range r.Logs {
		addr, err := parseAddr(wl.Address)
		if err != nil {
			return fail(err)
		}
		log := Log{Address: addr, TxLogIndex: wl.TxIndex, BlockLogIndex: wl.BlockIndex}
		for _, t := range wl.Topics {
			topic, err := parseHash(t)
			if err != nil {
				return fail(err)
			}
			log.Topics = append(log.Topics, topic)
		}
		if log.Data, err = resolver(wl.Data); err != nil {
			return fail(err)
		}
		receipt.Logs = append(receipt.Logs, log)
	}
	obs.Receipt = receipt
	if w.SourceKey != nil {
		data, err := resolver(*w.SourceKey)
		if err != nil {
			return fail(err)
		}
		kind := KindTypedConsensusEnvelope
		nt := w.Type
		if w.Type == 0 {
			kind = KindLegacyConsensusRLP
		}
		obs.ReplaceSource = true
		obs.SourceEncoding = SourceReceiptEncoding{Kind: kind, NativeType: Some(nt), Bytes: data}
	}

	for _, so := range w.ScopeOps {
		op := ScopeOp{Kind: ScopeOpKind(so.Kind), Parent: so.Parent, Scope: so.Scope}
		if so.Owner != nil {
			v := *so.Owner
			op.OwnerFrame = &v
		}
		obs.ScopeOps = append(obs.ScopeOps, op)
	}
	for _, wf := range w.Frames {
		frame, err := wf.input(resolver)
		if err != nil {
			return fail(err)
		}
		obs.Frames = append(obs.Frames, frame)
	}
	for _, wt := range w.Transfers {
		t, err := wt.observation(resolver)
		if err != nil {
			return fail(err)
		}
		obs.Transfers = append(obs.Transfers, t)
	}
	for _, ws := range w.SelfDestruct {
		s, err := ws.observation(resolver)
		if err != nil {
			return fail(err)
		}
		obs.SelfDestruct = append(obs.SelfDestruct, s)
	}
	f := &w.Fee
	fee := FeeObservation{GasLimit: f.GasLimit, GasUsed: f.GasUsed}
	if fee.Payer, err = parseAddr(f.Payer); err != nil {
		return fail(err)
	}
	if fee.Recipient, err = parseAddr(f.Recipient); err != nil {
		return fail(err)
	}
	if fee.EffectiveGasPrice, err = parseU256(f.Price); err != nil {
		return fail(err)
	}
	if fee.BaseFee, err = parseU256(f.BaseFee); err != nil {
		return fail(err)
	}
	if fee.PayerBalanceBefore, err = parseU256(f.PayerBefore); err != nil {
		return fail(err)
	}
	if fee.RecipientBalanceBefore, err = parseU256(f.RecipientBefore); err != nil {
		return fail(err)
	}
	if f.PayerAtReturn != nil {
		v, err := parseU256(*f.PayerAtReturn)
		if err != nil {
			return fail(err)
		}
		fee.PayerBalanceAtReturn = Some(v)
	}
	if f.BlobUsed != nil {
		fee.BlobGasUsed = Some(*f.BlobUsed)
	}
	if f.BlobPrice != nil {
		v, err := parseU256(*f.BlobPrice)
		if err != nil {
			return fail(err)
		}
		fee.BlobGasPrice = Some(v)
	}
	obs.Fee = fee
	for _, wo := range w.Ops {
		obs.Ops = append(obs.Ops, TxOp{
			Kind:            TxOpKind(wo.Kind),
			FrameIndex:      wo.Frame,
			TransferIndex:   wo.Transfer,
			SelfDestructIdx: wo.SelfDestruct,
		})
	}
	for _, wa := range w.State {
		account, err := wa.observation(resolver)
		if err != nil {
			return fail(err)
		}
		obs.State = append(obs.State, account)
	}
	for _, wt := range w.Authorizations {
		t := AuthorizationTuple{Nonce: wt.Nonce, YParity: wt.Parity}
		if t.Address, err = parseAddr(wt.Address); err != nil {
			return fail(err)
		}
		rBytes, err := decodeFixedHex(wt.R, 32)
		if err != nil {
			return fail(err)
		}
		copy(t.R[:], rBytes)
		sBytes, err := decodeFixedHex(wt.S, 32)
		if err != nil {
			return fail(err)
		}
		copy(t.S[:], sBytes)
		if wt.ChainID != nil {
			v, err := parseU256(*wt.ChainID)
			if err != nil {
				return fail(err)
			}
			t.ChainID = Some(v)
		}
		obs.Authorizations = append(obs.Authorizations, t)
	}
	for _, wo := range w.AuthOutcomes {
		authority, err := parseAddr(wo.Authority)
		if err != nil {
			return fail(err)
		}
		obs.AuthOutcomes = append(obs.AuthOutcomes, AuthorizationTupleOutcome{
			TupleIndex: wo.Tuple, Applied: wo.Applied, Authority: authority, NonceAfter: wo.NonceAfter,
		})
	}
	return obs, nil
}

// input rebuilds one frame input.
func (w *wireFrame) input(resolver blobResolver) (FrameInput, error) {
	in := FrameInput{ParentIndex: w.Parent, Kind: FrameKind(w.Kind), Engine: balanceEngine(w.Engine),
		LocalOutcome: Result(w.Outcome)}
	var err error
	addr := func(p *string, dst **Address20) error {
		if p == nil {
			return nil
		}
		v, err := parseAddr(*p)
		if err != nil {
			return err
		}
		*dst = &v
		return nil
	}
	if err := addr(w.Caller, &in.Caller); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.Context, &in.Context); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.Code, &in.Code); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.MsgSender, &in.MsgSender); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.Target, &in.Target); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.Attempted, &in.AttemptedCreate); err != nil {
		return FrameInput{}, err
	}
	if err := addr(w.Created, &in.CreatedCreate); err != nil {
		return FrameInput{}, err
	}
	if w.Value != nil {
		v, err := parseU256(*w.Value)
		if err != nil {
			return FrameInput{}, err
		}
		in.Value = Some(v)
	}
	if w.Gas != nil {
		in.Gas = Some(*w.Gas)
	}
	if w.GasUsed != nil {
		in.GasUsed = Some(*w.GasUsed)
	}
	if w.Error != nil {
		in.ErrorCode = Some(*w.Error)
	}
	if in.Input, err = resolver(w.Input); err != nil {
		return FrameInput{}, err
	}
	if in.Output, err = resolver(w.Output); err != nil {
		return FrameInput{}, err
	}
	if in.RevertData, err = resolver(w.Revert); err != nil {
		return FrameInput{}, err
	}
	return in, nil
}

// observation rebuilds one transfer observation.
func (w *wireTransfer) observation(resolver blobResolver) (TransferObservation, error) {
	t := TransferObservation{FrameID: w.Frame, Scope: w.Scope, Committed: w.Committed}
	var err error
	if t.From, err = parseAddr(w.From); err != nil {
		return TransferObservation{}, err
	}
	if t.To, err = parseAddr(w.To); err != nil {
		return TransferObservation{}, err
	}
	if t.Amount, err = parseU256(w.Amount); err != nil {
		return TransferObservation{}, err
	}
	for i, we := range w.Effects {
		e, err := we.effect()
		if err != nil {
			return TransferObservation{}, err
		}
		e.EffectIndex = uint64(i)
		t.Effects = append(t.Effects, e)
	}
	return t, nil
}

// observation rebuilds one selfdestruct observation.
func (w *wireSelfDestruct) observation(resolver blobResolver) (SelfDestructObservation, error) {
	s := SelfDestructObservation{FrameID: w.Frame, DeletionEligible: w.Eligible, Scope: w.Scope,
		RolledBack: w.RolledBack, AccountDeleted: w.Deleted, MovementPosition: MovementPosition(w.Position)}
	var err error
	if s.Contract, err = parseAddr(w.Contract); err != nil {
		return SelfDestructObservation{}, err
	}
	if s.Beneficiary, err = parseAddr(w.Beneficiary); err != nil {
		return SelfDestructObservation{}, err
	}
	if s.BalanceAtEntry, err = parseU256(w.Balance); err != nil {
		return SelfDestructObservation{}, err
	}
	for i, we := range w.Effects {
		e, err := we.effect()
		if err != nil {
			return SelfDestructObservation{}, err
		}
		e.EffectIndex = uint64(i)
		s.MovementEffects = append(s.MovementEffects, e)
	}
	return s, nil
}

// effect rebuilds one observed balance mutation.
func (w *wireEffect) effect() (balanceEffectValue, error) {
	addr, err := parseAddr(w.Address)
	if err != nil {
		return balanceEffectValue{}, err
	}
	before, err := parseU256(w.Before)
	if err != nil {
		return balanceEffectValue{}, err
	}
	after, err := parseU256(w.After)
	if err != nil {
		return balanceEffectValue{}, err
	}
	return balanceEffectValue{Address: addr, Before: before, After: after}, nil
}

// observation rebuilds one account observation.
func (w *wireAccount) observation(resolver blobResolver) (AccountObservation, error) {
	out := AccountObservation{StorageReset: w.Reset}
	var err error
	if out.Address, err = parseAddr(w.Address); err != nil {
		return AccountObservation{}, err
	}
	if out.Before, err = w.Before.point(resolver); err != nil {
		return AccountObservation{}, err
	}
	if out.After, err = w.After.point(resolver); err != nil {
		return AccountObservation{}, err
	}
	return out, nil
}

// point rebuilds one account boundary point.
func (w *wirePoint) point(resolver blobResolver) (AccountPoint, error) {
	p := AccountPoint{Exists: w.Exists, Nonce: w.Nonce}
	var err error
	if p.Balance, err = parseU256(w.Balance); err != nil {
		return AccountPoint{}, err
	}
	if p.CodeHash, err = parseHash(w.CodeHash); err != nil {
		return AccountPoint{}, err
	}
	if p.Code, err = resolver(w.Code); err != nil {
		return AccountPoint{}, err
	}
	p.Storage = make(map[Hash32]Hash32, len(w.Storage))
	for k, v := range w.Storage {
		key, err := parseHash(k)
		if err != nil {
			return AccountPoint{}, err
		}
		value, err := parseHash(v)
		if err != nil {
			return AccountPoint{}, err
		}
		p.Storage[key] = value
	}
	return p, nil
}

// toWireBlock encodes the block observation for the wire.
func toWireBlock(obs BlockObservation, outside []OutsideTxObservation, store blobStorer) (wireBlock, error) {
	h := &obs.Header
	w := wireBlock{Hash: hashHex(obs.Hash), Number: obs.Number, TxCount: obs.TransactionCount}
	w.Header = wireHeader{
		ParentHash:       hashHex(h.ParentHash),
		UncleHash:        hashHex(h.UncleHash),
		Beneficiary:      addrHex(h.Beneficiary),
		StateRoot:        hashHex(h.StateRoot),
		TransactionsRoot: hashHex(h.TransactionsRoot),
		ReceiptsRoot:     hashHex(h.ReceiptsRoot),
		LogsBloom:        encodeHex(h.LogsBloom[:]),
		Difficulty:       u256Hex(h.Difficulty),
		Number:           h.Number,
		GasLimit:         h.GasLimit,
		GasUsed:          h.GasUsed,
		Time:             h.Time,
		MixDigest:        hashHex(h.MixDigest),
		Nonce:            encodeHex(h.Nonce[:]),
	}
	var err error
	if w.Header.ExtraData, err = wireBytesOf(h.ExtraData, store); err != nil {
		return wireBlock{}, err
	}
	if w.Header.SourceRLP, err = wireBytesOf(h.SourceRLP, store); err != nil {
		return wireBlock{}, err
	}
	if v, ok := h.BaseFee.Get(); ok {
		s := u256Hex(v)
		w.Header.BaseFee = &s
	}
	rootFields := []struct {
		src Optional[Hash32]
		dst **string
	}{
		{h.WithdrawalsRoot, &w.Header.WithdrawalsRoot},
		{h.ParentBeaconBlockRoot, &w.Header.ParentBeaconRoot},
		{h.RequestsHash, &w.Header.RequestsHash},
		{h.BlockAccessListHash, &w.Header.BlockAccessHash},
	}
	for _, f := range rootFields {
		if v, ok := f.src.Get(); ok {
			s := hashHex(v)
			*f.dst = &s
		}
	}
	if v, ok := h.BlobGasUsed.Get(); ok {
		w.Header.BlobGasUsed = &v
	}
	if v, ok := h.ExcessBlobGas.Get(); ok {
		w.Header.ExcessBlobGas = &v
	}
	if v, ok := h.SlotNumber.Get(); ok {
		w.Header.SlotNumber = &v
	}
	for i := range outside {
		wo := wireOutside{Kind: uint8(outside[i].Kind), NativeOrder: outside[i].NativeOrder}
		for _, e := range outside[i].Effects {
			wo.Effects = append(wo.Effects, wireSystemEffect{
				Address: addrHex(e.Address), Before: u256Hex(e.Before), After: u256Hex(e.After)})
		}
		w.Outside = append(w.Outside, wo)
	}
	return w, nil
}

// toWireTx encodes the transaction observation for the wire. sourceKey,
// when non-nil, carries the retained receipt source encoding (the D76
// spool path: the producer already streamed the consensus bytes into the
// store).
func toWireTx(obs TxObservation, store blobStorer, sourceKey *wireBytes) (wireTx, error) {
	w := wireTx{Position: obs.Inclusion.NativePosition, Hash: hashHex(obs.Inclusion.TransactionHash), Type: obs.NativeType}
	var err error
	wireOne := func(b []byte) (wireBytes, error) { return wireBytesOf(b, store) }
	for _, raw := range obs.RawEncodings {
		data, err := wireOne(raw.Bytes)
		if err != nil {
			return wireTx{}, err
		}
		w.Raw = append(w.Raw, wireRaw{Kind: uint8(raw.Kind), Data: data})
	}
	p := &w.Projection
	p.Sender = addrHex(obs.Projection.Sender.Value)
	p.SenderProv = uint8(obs.Projection.Sender.Provenance)
	p.Value = u256Hex(obs.Projection.Value.Value)
	p.ValueProv = uint8(obs.Projection.Value.Provenance)
	p.Input, err = wireOne(obs.Projection.Input.Value)
	if err != nil {
		return wireTx{}, err
	}
	p.InputProv = uint8(obs.Projection.Input.Provenance)
	p.Nonce = obs.Projection.Nonce.Value
	p.NonceProv = uint8(obs.Projection.Nonce.Provenance)
	p.GasLimit = obs.Projection.GasLimit
	p.EffectivePrice = u256Hex(obs.Projection.EffectiveGasPrice)
	if target, ok := obs.Projection.Target.Get(); ok {
		s := addrHex(target.Value)
		p.Target = &s
		p.TargetProv = uint8(target.Provenance)
	}

	r := &obs.Receipt
	wr := &w.Receipt
	if v, ok := r.Status.Get(); ok {
		wr.Status = &v
	}
	if v, ok := r.PostStateRoot.Get(); ok {
		s := hashHex(v)
		wr.PostRoot = &s
	}
	wr.Result = uint8(r.Result)
	wr.Cumulative = r.CumulativeGasUsed
	if v, ok := r.GasUsed.Get(); ok {
		wr.GasUsed = &v
	}
	if v, ok := r.EffectiveGasPrice.Get(); ok {
		s := u256Hex(v)
		wr.EffectivePrice = &s
	}
	if v, ok := r.BlobGasUsed.Get(); ok {
		wr.BlobGasUsed = &v
	}
	if v, ok := r.BlobGasPrice.Get(); ok {
		s := u256Hex(v)
		wr.BlobGasPrice = &s
	}
	wr.Bloom = encodeHex(r.LogsBloom[:])
	if r.AttemptedCreateAddress != nil {
		s := addrHex(*r.AttemptedCreateAddress)
		wr.Attempted = &s
	}
	if r.CreatedContract != nil {
		s := addrHex(*r.CreatedContract)
		wr.Created = &s
	}
	for _, log := range r.Logs {
		wl := wireLog{Address: addrHex(log.Address), TxIndex: log.TxLogIndex, BlockIndex: log.BlockLogIndex}
		for _, t := range log.Topics {
			wl.Topics = append(wl.Topics, hashHex(t))
		}
		if wl.Data, err = wireOne(log.Data); err != nil {
			return wireTx{}, err
		}
		wr.Logs = append(wr.Logs, wl)
	}
	w.SourceKey = sourceKey

	for _, op := range obs.ScopeOps {
		wso := wireScopeOp{Kind: uint8(op.Kind), Parent: op.Parent, Scope: op.Scope}
		if op.OwnerFrame != nil {
			v := *op.OwnerFrame
			wso.Owner = &v
		}
		w.ScopeOps = append(w.ScopeOps, wso)
	}
	for _, f := range obs.Frames {
		wf := wireFrame{Parent: f.ParentIndex, Kind: uint8(f.Kind), Engine: uint8(f.Engine), Outcome: uint8(f.LocalOutcome)}
		setAddr := func(p *Address20, dst **string) {
			if p != nil {
				s := addrHex(*p)
				*dst = &s
			}
		}
		setAddr(f.Caller, &wf.Caller)
		setAddr(f.Context, &wf.Context)
		setAddr(f.Code, &wf.Code)
		setAddr(f.MsgSender, &wf.MsgSender)
		setAddr(f.Target, &wf.Target)
		setAddr(f.AttemptedCreate, &wf.Attempted)
		setAddr(f.CreatedCreate, &wf.Created)
		if v, ok := f.Value.Get(); ok {
			s := u256Hex(v)
			wf.Value = &s
		}
		if v, ok := f.Gas.Get(); ok {
			wf.Gas = &v
		}
		if v, ok := f.GasUsed.Get(); ok {
			wf.GasUsed = &v
		}
		if v, ok := f.ErrorCode.Get(); ok {
			wf.Error = &v
		}
		if wf.Input, err = wireOne(f.Input); err != nil {
			return wireTx{}, err
		}
		if wf.Output, err = wireOne(f.Output); err != nil {
			return wireTx{}, err
		}
		if wf.Revert, err = wireOne(f.RevertData); err != nil {
			return wireTx{}, err
		}
		w.Frames = append(w.Frames, wf)
	}
	for _, t := range obs.Transfers {
		wt := wireTransfer{Frame: t.FrameID, From: addrHex(t.From), To: addrHex(t.To),
			Amount: u256Hex(t.Amount), Scope: t.Scope, Committed: t.Committed}
		wt.Effects = toWireEffects(t.Effects)
		w.Transfers = append(w.Transfers, wt)
	}
	for _, s := range obs.SelfDestruct {
		ws := wireSelfDestruct{Frame: s.FrameID, Contract: addrHex(s.Contract), Beneficiary: addrHex(s.Beneficiary),
			Balance: u256Hex(s.BalanceAtEntry), Eligible: s.DeletionEligible, Scope: s.Scope,
			RolledBack: s.RolledBack, Deleted: s.AccountDeleted, Position: uint8(s.MovementPosition)}
		ws.Effects = toWireEffects(s.MovementEffects)
		w.SelfDestruct = append(w.SelfDestruct, ws)
	}
	f := &obs.Fee
	wf := &w.Fee
	wf.Payer = addrHex(f.Payer)
	wf.GasLimit = f.GasLimit
	wf.GasUsed = f.GasUsed
	wf.Price = u256Hex(f.EffectiveGasPrice)
	wf.Recipient = addrHex(f.Recipient)
	wf.BaseFee = u256Hex(f.BaseFee)
	wf.PayerBefore = u256Hex(f.PayerBalanceBefore)
	wf.RecipientBefore = u256Hex(f.RecipientBalanceBefore)
	if v, ok := f.PayerBalanceAtReturn.Get(); ok {
		s := u256Hex(v)
		wf.PayerAtReturn = &s
	}
	if v, ok := f.BlobGasUsed.Get(); ok {
		wf.BlobUsed = &v
	}
	if v, ok := f.BlobGasPrice.Get(); ok {
		s := u256Hex(v)
		wf.BlobPrice = &s
	}
	for _, op := range obs.Ops {
		w.Ops = append(w.Ops, wireOp{Kind: uint8(op.Kind), Frame: op.FrameIndex, Transfer: op.TransferIndex,
			SelfDestruct: op.SelfDestructIdx})
	}
	for _, a := range obs.State {
		wa := wireAccount{Address: addrHex(a.Address), Reset: a.StorageReset}
		if wa.Before, err = toWirePoint(a.Before, store); err != nil {
			return wireTx{}, err
		}
		if wa.After, err = toWirePoint(a.After, store); err != nil {
			return wireTx{}, err
		}
		w.State = append(w.State, wa)
	}
	for _, t := range obs.Authorizations {
		wt := wireAuthorization{Address: addrHex(t.Address), Nonce: t.Nonce, Parity: t.YParity,
			R: encodeHex(t.R[:]), S: encodeHex(t.S[:])}
		if v, ok := t.ChainID.Get(); ok {
			s := u256Hex(v)
			wt.ChainID = &s
		}
		w.Authorizations = append(w.Authorizations, wt)
	}
	for _, o := range obs.AuthOutcomes {
		w.AuthOutcomes = append(w.AuthOutcomes, wireAuthOutcome{Tuple: o.TupleIndex, Applied: o.Applied,
			Authority: addrHex(o.Authority), NonceAfter: o.NonceAfter})
	}
	return w, nil
}

func toWireEffects(effects []effectAlias) []wireEffect {
	out := make([]wireEffect, 0, len(effects))
	for _, e := range effects {
		out = append(out, wireEffect{Address: addrHex(e.Address), Before: u256Hex(e.Before), After: u256Hex(e.After)})
	}
	return out
}

func toWirePoint(p AccountPoint, store blobStorer) (wirePoint, error) {
	wp := wirePoint{Exists: p.Exists, Nonce: p.Nonce, Balance: u256Hex(p.Balance), CodeHash: hashHex(p.CodeHash)}
	var err error
	if wp.Code, err = wireBytesOf(p.Code, store); err != nil {
		return wirePoint{}, err
	}
	if len(p.Storage) > 0 {
		wp.Storage = make(map[string]string, len(p.Storage))
		for k, v := range p.Storage {
			wp.Storage[hashHex(k)] = hashHex(v)
		}
	}
	return wp, nil
}
