// The TransportFrame wrapper v2: pinned field numbers 1..12 with the
// complete/part oneof (fields 7/8), its canonical encoding and its exact
// encoded size. The wrapper carries one bounded RecordFragment whole or in
// parts; the fragment bytes inside are immutable and are never re-serialized
// (MESSAGE_CONTRACT section 6, D73; CONTROL_WIRE_CONTRACT, "TransportFrame
// layout v2").
package proto

import "fmt"

// TransportFrame field numbers of the pinned v2 table.
const (
	fieldTransportVersion   = 1  // varint u32 = 2
	fieldNetwork            = 2  // message NetworkIdentity
	fieldEventID            = 3  // bytes, exactly 40
	fieldRecordDigest       = 4  // bytes, exactly 32
	fieldPublicationEpoch   = 5  // bytes, exactly 16
	fieldChainSequence      = 6  // varint u64
	fieldComplete           = 7  // message CompleteFragment (oneof)
	fieldPart               = 8  // message FragmentPart (oneof)
	fieldFragmentIndex      = 9  // varint u64
	fieldFragmentCount      = 10 // varint u64
	fieldFragmentDigest     = 11 // bytes, exactly 32
	fieldRecordFormatNumber = 12 // varint u32 = 1
)

// CompleteFragment carries one whole RecordFragment (field 1, 1..65536
// bytes).
type CompleteFragment struct {
	FragmentBytes []byte
}

// Validate rejects a fragment outside the 1..65536 byte bound.
func (c *CompleteFragment) Validate() error {
	if len(c.FragmentBytes) == 0 {
		return fmt.Errorf("%w: complete fragment carries zero bytes", ErrInvalidValue)
	}
	if len(c.FragmentBytes) > FragmentMaxBytes {
		return fmt.Errorf("%w: complete fragment %d bytes exceeds %d",
			ErrInvalidValue, len(c.FragmentBytes), FragmentMaxBytes)
	}
	return nil
}

func appendCompleteFragment(b []byte, c *CompleteFragment) []byte {
	return appendBytesField(b, 1, c.FragmentBytes)
}

func decodeCompleteFragment(b []byte) (*CompleteFragment, error) {
	r := &wireReader{b: b}
	var seen fieldSet
	var fragment CompleteFragment
	if err := decodeFields("CompleteFragment", r, 1, 1, nil, func(field uint32, wire byte) error {
		if wire != wireBytes {
			return fmt.Errorf("%w: CompleteFragment field 1 wire type %d", ErrMalformed, wire)
		}
		data, err := r.readBytes()
		if err != nil {
			return err
		}
		fragment.FragmentBytes = data
		return nil
	}, &seen); err != nil {
		return nil, err
	}
	if err := fragment.Validate(); err != nil {
		return nil, err
	}
	return &fragment, nil
}

// FragmentPart carries one transport part of a RecordFragment: field 1
// part_index, field 2 part_count, field 3 fragment_total_bytes (1..65536)
// and field 4 data. Every named scalar is present, including part_index 0.
type FragmentPart struct {
	PartIndex uint64
	PartCount uint64
	// FragmentTotalBytes is the assembled size of the whole fragment.
	FragmentTotalBytes uint64
	Data               []byte
}

// Validate rejects a part whose indices, total or data break the assembly
// rules: indices inside the count, total and data inside the fragment bound,
// data never longer than the fragment it belongs to.
func (p *FragmentPart) Validate() error {
	if p.PartIndex >= p.PartCount {
		return fmt.Errorf("%w: part index %d outside count %d", ErrInvalidValue, p.PartIndex, p.PartCount)
	}
	if p.FragmentTotalBytes == 0 || p.FragmentTotalBytes > FragmentMaxBytes {
		return fmt.Errorf("%w: fragment total %d outside 1..%d",
			ErrInvalidValue, p.FragmentTotalBytes, FragmentMaxBytes)
	}
	if len(p.Data) == 0 {
		return fmt.Errorf("%w: part carries zero data bytes", ErrInvalidValue)
	}
	if uint64(len(p.Data)) > FragmentMaxBytes {
		return fmt.Errorf("%w: part data %d bytes exceeds %d", ErrInvalidValue, len(p.Data), FragmentMaxBytes)
	}
	if uint64(len(p.Data)) > p.FragmentTotalBytes {
		return fmt.Errorf("%w: part data %d bytes longer than fragment total %d",
			ErrInvalidValue, len(p.Data), p.FragmentTotalBytes)
	}
	return nil
}

func appendFragmentPart(b []byte, p *FragmentPart) []byte {
	b = appendVarintField(b, 1, p.PartIndex)
	b = appendVarintField(b, 2, p.PartCount)
	b = appendVarintField(b, 3, p.FragmentTotalBytes)
	return appendBytesField(b, 4, p.Data)
}

func decodeFragmentPart(b []byte) (*FragmentPart, error) {
	r := &wireReader{b: b}
	var seen fieldSet
	var part FragmentPart
	if err := decodeFields("FragmentPart", r, 1, 4, nil, func(field uint32, wire byte) error {
		switch field {
		case 1, 2, 3:
			if wire != wireVarint {
				return fmt.Errorf("%w: FragmentPart field %d wire type %d", ErrMalformed, field, wire)
			}
			v, err := r.readVarint()
			if err != nil {
				return err
			}
			switch field {
			case 1:
				part.PartIndex = v
			case 2:
				part.PartCount = v
			case 3:
				part.FragmentTotalBytes = v
			}
			return nil
		default: // field 4
			if wire != wireBytes {
				return fmt.Errorf("%w: FragmentPart field 4 wire type %d", ErrMalformed, wire)
			}
			data, err := r.readBytes()
			if err != nil {
				return err
			}
			part.Data = data
			return nil
		}
	}, &seen); err != nil {
		return nil, err
	}
	if err := part.Validate(); err != nil {
		return nil, err
	}
	return &part, nil
}

// TransportFrame is the small transport wrapper of one (event ordinal,
// fragment_index) pair in one form: one complete fragment or one part of it.
// All frames of one record share event_id, record_digest and chain_sequence;
// fragment_index separates record-structure messages, part_index separates
// transport parts of one such message.
type TransportFrame struct {
	TransportVersion    uint32
	Network             NetworkIdentity
	EventID             EventID
	RecordDigest        Hash32
	PublicationEpoch    ID16
	ChainSequence       uint64
	Complete            *CompleteFragment
	Part                *FragmentPart
	FragmentIndex       uint64
	FragmentCount       uint64
	FragmentDigest      Hash32
	RecordFormatVersion uint32
}

// Validate rejects a frame that breaks the pinned table: wrong versions,
// unknown network family, unset identities, a chain sequence below the first
// counter value, a missing or doubled oneof alternative, or fragment
// bounds. Each rejection names the offending field.
func (f *TransportFrame) Validate() error {
	if f.TransportVersion != TransportVersion {
		return fmt.Errorf("%w: transport version %d, want %d",
			ErrVersionUnsupported, f.TransportVersion, TransportVersion)
	}
	if f.RecordFormatVersion != RecordFormatVersion {
		return fmt.Errorf("%w: record format version %d, want %d",
			ErrVersionUnsupported, f.RecordFormatVersion, RecordFormatVersion)
	}
	if err := f.Network.Validate(); err != nil {
		return err
	}
	if err := f.EventID.Validate(); err != nil {
		return err
	}
	if f.PublicationEpoch.IsZero() {
		return fmt.Errorf("%w: publication epoch is zero", ErrInvalidValue)
	}
	if f.ChainSequence == 0 {
		return fmt.Errorf("%w: chain sequence 0, counters start at 1", ErrInvalidValue)
	}
	if (f.Complete == nil) == (f.Part == nil) {
		return fmt.Errorf("%w: exactly one of complete/part must be set", ErrInvalidValue)
	}
	if f.Complete != nil {
		if err := f.Complete.Validate(); err != nil {
			return err
		}
	}
	if f.Part != nil {
		if err := f.Part.Validate(); err != nil {
			return err
		}
	}
	if f.FragmentCount == 0 {
		return fmt.Errorf("%w: fragment count 0", ErrInvalidValue)
	}
	return nil
}

// Encode returns the canonical wrapper encoding v2 of the frame: ascending
// tag order over fields 1..12, minimal unsigned varints, length-delimited
// messages, every named scalar present, exactly one oneof alternative. The
// same frame always encodes to the same bytes.
func (f *TransportFrame) Encode() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	capacityHint := 512
	if f.Complete != nil {
		capacityHint += len(f.Complete.FragmentBytes)
	} else {
		capacityHint += len(f.Part.Data)
	}
	out := make([]byte, 0, capacityHint)
	out = appendVarintField(out, fieldTransportVersion, uint64(f.TransportVersion))
	out = appendMessageField(out, fieldNetwork, appendNetworkIdentity(nil, f.Network))
	out = appendBytesField(out, fieldEventID, f.EventID[:])
	out = appendBytesField(out, fieldRecordDigest, f.RecordDigest[:])
	out = appendBytesField(out, fieldPublicationEpoch, f.PublicationEpoch[:])
	out = appendVarintField(out, fieldChainSequence, f.ChainSequence)
	if f.Complete != nil {
		out = appendMessageField(out, fieldComplete, appendCompleteFragment(nil, f.Complete))
	} else {
		out = appendMessageField(out, fieldPart, appendFragmentPart(nil, f.Part))
	}
	out = appendVarintField(out, fieldFragmentIndex, f.FragmentIndex)
	out = appendVarintField(out, fieldFragmentCount, f.FragmentCount)
	out = appendBytesField(out, fieldFragmentDigest, f.FragmentDigest[:])
	out = appendVarintField(out, fieldRecordFormatNumber, uint64(f.RecordFormatVersion))
	return out, nil
}

// DecodeTransportFrame parses canonical wrapper bytes into a frame. It
// rejects unknown fields, duplicates, non-ascending tag order, non-minimal
// varints, missing named scalars (including required zeros), a doubled or
// missing oneof, wrong versions, wrong lengths and trailing bytes; every
// rejection names its cause. Decoded byte fields are copies.
func DecodeTransportFrame(b []byte) (*TransportFrame, error) {
	r := &wireReader{b: b}
	var frame TransportFrame
	var seen fieldSet
	var havePayload bool
	err := decodeFields("TransportFrame", r, fieldTransportVersion, fieldRecordFormatNumber,
		map[uint32]bool{fieldComplete: true, fieldPart: true},
		func(field uint32, wire byte) error {
			switch field {
			case fieldTransportVersion, fieldChainSequence, fieldFragmentIndex,
				fieldFragmentCount, fieldRecordFormatNumber:
				if wire != wireVarint {
					return fmt.Errorf("%w: TransportFrame field %d wire type %d", ErrMalformed, field, wire)
				}
				v, err := r.readVarint()
				if err != nil {
					return err
				}
				switch field {
				case fieldTransportVersion:
					if v != uint64(TransportVersion) {
						return fmt.Errorf("%w: transport version %d, want %d",
							ErrVersionUnsupported, v, TransportVersion)
					}
					frame.TransportVersion = uint32(v)
				case fieldChainSequence:
					frame.ChainSequence = v
				case fieldFragmentIndex:
					frame.FragmentIndex = v
				case fieldFragmentCount:
					frame.FragmentCount = v
				case fieldRecordFormatNumber:
					if v != uint64(RecordFormatVersion) {
						return fmt.Errorf("%w: record format version %d, want %d",
							ErrVersionUnsupported, v, RecordFormatVersion)
					}
					frame.RecordFormatVersion = uint32(v)
				}
				return nil
			case fieldNetwork:
				msg, err := r.readMessage(wire, field)
				if err != nil {
					return err
				}
				net, err := decodeNetworkIdentity(msg)
				if err != nil {
					return err
				}
				frame.Network = net
				return nil
			case fieldEventID:
				buf, err := r.readFixedBytes(wire, field, 40, "event_id")
				if err != nil {
					return err
				}
				copy(frame.EventID[:], buf)
				return nil
			case fieldRecordDigest:
				buf, err := r.readFixedBytes(wire, field, 32, "record_digest")
				if err != nil {
					return err
				}
				copy(frame.RecordDigest[:], buf)
				return nil
			case fieldPublicationEpoch:
				buf, err := r.readFixedBytes(wire, field, 16, "publication_epoch")
				if err != nil {
					return err
				}
				copy(frame.PublicationEpoch[:], buf)
				return nil
			case fieldFragmentDigest:
				buf, err := r.readFixedBytes(wire, field, 32, "fragment_digest")
				if err != nil {
					return err
				}
				copy(frame.FragmentDigest[:], buf)
				return nil
			case fieldComplete, fieldPart:
				if havePayload {
					return fmt.Errorf("%w: both oneof alternatives (fields 7 and 8) present", ErrInvalidValue)
				}
				havePayload = true
				msg, err := r.readMessage(wire, field)
				if err != nil {
					return err
				}
				if field == fieldComplete {
					complete, err := decodeCompleteFragment(msg)
					if err != nil {
						return err
					}
					frame.Complete = complete
				} else {
					part, err := decodeFragmentPart(msg)
					if err != nil {
						return err
					}
					frame.Part = part
				}
				return nil
			}
			return nil // unreachable: decodeFields filters the range
		}, &seen)
	if err != nil {
		return nil, err
	}
	for _, field := range []uint32{fieldTransportVersion, fieldNetwork, fieldEventID,
		fieldRecordDigest, fieldPublicationEpoch, fieldChainSequence,
		fieldFragmentIndex, fieldFragmentCount, fieldFragmentDigest, fieldRecordFormatNumber} {
		if err := seen.require(field); err != nil {
			return nil, err
		}
	}
	if !havePayload {
		return nil, fmt.Errorf("%w: oneof field 7 complete / 8 part", ErrMissingField)
	}
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	return &frame, nil
}

// decodeFields walks one message of the pinned table: it enforces ascending
// tag order, rejects unknown field numbers outside the table range
// [first,last], rejects duplicates and requires every number of the range to
// appear exactly once, except the members of the exclusive oneof optional
// set, whose presence the caller checks.
func decodeFields(messageName string, r *wireReader, first, last uint32,
	optional map[uint32]bool, handle func(field uint32, wire byte) error, seen *fieldSet) error {
	var order uint32
	for !r.done() {
		field, wire, err := r.readKey()
		if err != nil {
			return err
		}
		if field == order {
			return fmt.Errorf("%w: %s field %d appears twice", ErrDuplicateField, messageName, field)
		}
		if field < order {
			return fmt.Errorf("%w: %s field %d follows field %d", ErrFieldOrder, messageName, field, order)
		}
		order = field
		if field < first || field > last {
			return fmt.Errorf("%w: %s field %d", ErrUnknownField, messageName, field)
		}
		if err := seen.add(field); err != nil {
			return fmt.Errorf("%w: %s field %d", err, messageName, field)
		}
		if err := handle(field, wire); err != nil {
			return err
		}
	}
	for field := first; field <= last; field++ {
		if optional[field] {
			continue
		}
		if !seen.has(field) {
			return fmt.Errorf("%w: %s field %d", ErrMissingField, messageName, field)
		}
	}
	return nil
}

// readFixedBytes reads one length-delimited field of exactly want bytes.
func (r *wireReader) readFixedBytes(wire byte, field uint32, want int, name string) ([]byte, error) {
	if wire != wireBytes {
		return nil, fmt.Errorf("%w: field %d (%s) wire type %d", ErrMalformed, field, name, wire)
	}
	buf, err := r.readBytes()
	if err != nil {
		return nil, err
	}
	if len(buf) != want {
		return nil, fmt.Errorf("%w: field %d (%s) length %d, want %d",
			ErrInvalidValue, field, name, len(buf), want)
	}
	return buf, nil
}

// readMessage reads one length-delimited submessage field.
func (r *wireReader) readMessage(wire byte, field uint32) ([]byte, error) {
	if wire != wireBytes {
		return nil, fmt.Errorf("%w: field %d wire type %d, want length-delimited", ErrMalformed, field, wire)
	}
	return r.readBytes()
}

// EncodedFrameSize returns the exact size of the canonical encoding: the
// precise sum of key, varint, length and data sizes of the pinned table,
// with no approximate overhead. It equals len(Encode()) for every frame.
func (f *TransportFrame) EncodedFrameSize() (uint64, error) {
	if err := f.Validate(); err != nil {
		return 0, err
	}
	scalars := frameScalars{chainSequence: f.ChainSequence,
		fragmentIndex: f.FragmentIndex, fragmentCount: f.FragmentCount}
	if f.Complete != nil {
		return completeEncodedSize(scalars, uint64(len(f.Complete.FragmentBytes))), nil
	}
	return partEncodedSize(scalars, f.Part.PartIndex, f.Part.PartCount,
		f.Part.FragmentTotalBytes, uint64(len(f.Part.Data))), nil
}

// frameScalars are the wrapper scalar fields whose varint lengths vary with
// their values; every other field of the pinned table has a fixed size.
type frameScalars struct {
	chainSequence uint64
	fragmentIndex uint64
	fragmentCount uint64
}

// networkIdentityEncodedSize is the fixed encoded size of the network
// message: enum varint plus two 32-byte length-delimited fields.
func networkIdentityEncodedSize() uint64 {
	const fixed32 = 1 + 1 + 32 // key + one-byte length + 32 bytes
	return 1 + uint64(varintLen(uint64(NetworkFamilyEVM))) + fixed32 + fixed32
}

// completeEncodedSize is the exact encoded size of a COMPLETE frame whose
// fragment carries fragmentBytes payload bytes.
func completeEncodedSize(s frameScalars, fragmentBytes uint64) uint64 {
	inner := 1 + uint64(varintLen(fragmentBytes)) + fragmentBytes
	return wrapperEncodedSize(s, 1+uint64(varintLen(inner))+inner)
}

// partEncodedSize is the exact encoded size of a PART frame with the given
// part scalars and dataLen data bytes. The layout planner calls it with
// part_index=part_count=U64Max as the conservative overhead probe.
func partEncodedSize(s frameScalars, partIndex, partCount, total, dataLen uint64) uint64 {
	inner := 1 + uint64(varintLen(partIndex)) +
		1 + uint64(varintLen(partCount)) +
		1 + uint64(varintLen(total)) +
		1 + uint64(varintLen(dataLen)) + dataLen
	return wrapperEncodedSize(s, 1+uint64(varintLen(inner))+inner)
}

// wrapperEncodedSize sums the fixed-size fields and the varying varints of
// the wrapper around one already-measured oneof payload field.
func wrapperEncodedSize(s frameScalars, payloadField uint64) uint64 {
	netSize := networkIdentityEncodedSize()
	return 1 + uint64(varintLen(uint64(TransportVersion))) + // field 1
		1 + uint64(varintLen(netSize)) + netSize + // field 2
		1 + 1 + 40 + // field 3 event_id
		1 + 1 + 32 + // field 4 record_digest
		1 + 1 + 16 + // field 5 publication_epoch
		1 + uint64(varintLen(s.chainSequence)) + // field 6
		payloadField + // field 7 or 8
		1 + uint64(varintLen(s.fragmentIndex)) + // field 9
		1 + uint64(varintLen(s.fragmentCount)) + // field 10
		1 + 1 + 32 + // field 11 fragment_digest
		1 + uint64(varintLen(uint64(RecordFormatVersion))) // field 12
}

func appendVarintField(b []byte, field uint32, v uint64) []byte {
	b = appendVarint(b, uint64(field)<<3|wireVarint)
	return appendVarint(b, v)
}

func appendBytesField(b []byte, field uint32, v []byte) []byte {
	b = appendVarint(b, uint64(field)<<3|wireBytes)
	b = appendVarint(b, uint64(len(v)))
	return append(b, v...)
}

func appendMessageField(b []byte, field uint32, msg []byte) []byte {
	return appendBytesField(b, field, msg)
}

func appendNetworkIdentity(b []byte, n NetworkIdentity) []byte {
	b = appendVarintField(b, 1, uint64(n.Family))
	b = appendBytesField(b, 2, n.ChainID[:])
	return appendBytesField(b, 3, n.GenesisHash[:])
}

func decodeNetworkIdentity(b []byte) (NetworkIdentity, error) {
	r := &wireReader{b: b}
	var seen fieldSet
	var net NetworkIdentity
	err := decodeFields("NetworkIdentity", r, 1, 3, nil, func(field uint32, wire byte) error {
		switch field {
		case 1:
			if wire != wireVarint {
				return fmt.Errorf("%w: NetworkIdentity field 1 wire type %d", ErrMalformed, wire)
			}
			v, err := r.readVarint()
			if err != nil {
				return err
			}
			if NetworkFamily(v) != NetworkFamilyEVM {
				return fmt.Errorf("%w: network family %d is not EVM=1", ErrClosedRegistry, v)
			}
			net.Family = NetworkFamily(v)
			return nil
		default: // fields 2 and 3
			buf, err := r.readFixedBytes(wire, field, 32, "network identity bytes")
			if err != nil {
				return err
			}
			if field == 2 {
				copy(net.ChainID[:], buf)
			} else {
				copy(net.GenesisHash[:], buf)
			}
			return nil
		}
	}, &seen)
	if err != nil {
		return NetworkIdentity{}, err
	}
	return net, nil
}
