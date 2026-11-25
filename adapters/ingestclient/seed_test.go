package ingestclient

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// contractSeed assembles a reservation seed the way CONTROL_WIRE_CONTRACT
// ("READY manifest and reservation") lists it, field by field, without the
// code under test: the oracle of ParseSeed.
type contractSeed struct {
	reservation, epoch, producer, run [16]byte
	seq                               uint64
	manifest                          [32]byte
	events, sequenceBase              uint64
	binding                           [32]byte
	frameLimit                        uint64
	versions                          [5]uint32
}

func (c contractSeed) bytes() []byte {
	out := []byte("extractor.reservation.v2")
	out = append(out, 0)
	out = append(out, c.reservation[:]...)
	out = append(out, c.epoch[:]...)
	out = append(out, c.producer[:]...)
	out = append(out, c.run[:]...)
	out = binary.BigEndian.AppendUint64(out, c.seq)
	out = append(out, c.manifest[:]...)
	out = binary.BigEndian.AppendUint64(out, c.events)
	out = binary.BigEndian.AppendUint64(out, c.sequenceBase)
	out = append(out, c.binding[:]...)
	out = binary.BigEndian.AppendUint64(out, c.frameLimit)
	for _, v := range c.versions {
		out = binary.BigEndian.AppendUint32(out, v)
	}
	return out
}

// pinned are the five versions the seed carries, in its order.
var pinned = [5]uint32{proto.TransportVersion, proto.LayoutVersion, proto.HeaderVersion,
	proto.RecordFormatVersion, proto.ManifestVersion}

// goldenSeed is one seed of fixed inputs. goldenHex is its bytes written out
// once by the oracle, so that a change of the parser cannot hide behind a
// matching builder.
var goldenSeed = contractSeed{
	reservation: [16]byte{0x01}, epoch: [16]byte{0x02}, producer: [16]byte{0x03}, run: [16]byte{0x04},
	seq: 7, manifest: sha256.Sum256([]byte("manifest")), events: 3, sequenceBase: 41,
	binding: sha256.Sum256([]byte("binding")), frameLimit: 2097152, versions: pinned,
}

const goldenHex = "657874726163746f722e7265736572766174696f6e2e763200" + // domain, zero byte
	"01000000000000000000000000000000" + // reservation id
	"02000000000000000000000000000000" + // issuer store epoch
	"03000000000000000000000000000000" + // producer id
	"04000000000000000000000000000000" + // run id
	"0000000000000007" + // batch sequence
	"05b3abf2579a5eb66403cd78be557fd860633a1fe2103c7642030defe32c657f" + // manifest digest
	"0000000000000003" + // event count
	"0000000000000029" + // sequence base
	"80f70afeef3caa57646fd20afb95be9c3f2c03d38e091906de31838813dcc22e" + // binding digest
	"0000000000200000" + // frame limit
	"0000000200000002000000020000000100000002" // transport, layout, header, record format, manifest

// digestOf is the plan digest of a seed as the wire carries it.
func digestOf(seed []byte) []byte {
	digest := PlanDigest(seed)
	return digest[:]
}

func TestSeedGoldenBytes(t *testing.T) {
	if got := hex.EncodeToString(goldenSeed.bytes()); got != goldenHex {
		t.Fatalf("the contract layout of the oracle changed:\n got %s\nwant %s", got, goldenHex)
	}
	if SeedLength != len(goldenSeed.bytes()) {
		t.Fatalf("SeedLength = %d, the contract layout is %d bytes", SeedLength, len(goldenSeed.bytes()))
	}
	seed, err := ParseSeed(goldenSeed.bytes())
	if err != nil {
		t.Fatalf("ParseSeed of the golden seed: %v", err)
	}
	want := Seed{ReservationID: proto.ID16{0x01}, IssuerEpoch: proto.ID16{0x02}, ProducerID: proto.ID16{0x03},
		RunID: proto.ID16{0x04}, BatchSeq: 7, ManifestDigest: proto.Hash32(sha256.Sum256([]byte("manifest"))),
		EventCount: 3, SequenceBase: 41, BindingDigest: proto.Hash32(sha256.Sum256([]byte("binding"))),
		FrameLimit: 2097152, raw: goldenSeed.bytes()}
	if seed.ReservationID != want.ReservationID || seed.IssuerEpoch != want.IssuerEpoch ||
		seed.ProducerID != want.ProducerID || seed.RunID != want.RunID || seed.BatchSeq != want.BatchSeq ||
		seed.ManifestDigest != want.ManifestDigest || seed.EventCount != want.EventCount ||
		seed.SequenceBase != want.SequenceBase || seed.BindingDigest != want.BindingDigest ||
		seed.FrameLimit != want.FrameLimit || string(seed.raw) != string(want.raw) {
		t.Fatalf("ParseSeed:\n got %+v\nwant %+v", seed, want)
	}
	if PlanDigest(goldenSeed.bytes()) != proto.Hash32(sha256.Sum256(goldenSeed.bytes())) {
		t.Fatal("PlanDigest is not the SHA-256 of the seed bytes")
	}
}

func TestParseSeedRejects(t *testing.T) {
	base := goldenSeed.bytes()
	cases := []struct {
		name  string
		seed  []byte
		wants string
	}{
		{"no bytes", nil, "length"},
		{"truncated", base[:SeedLength-1], "length"},
		{"one byte long", append(append([]byte(nil), base...), 0), "length"},
		{"cut short", base[:40], "length"},
	}
	for i := range pinned {
		other := goldenSeed
		other.versions[i] = pinned[i] + 1
		cases = append(cases, struct {
			name  string
			seed  []byte
			wants string
		}{"wrong version " + strconv.Itoa(i), other.bytes(), "version"})
	}
	for _, tc := range cases {
		seed, err := ParseSeed(tc.seed)
		if !errors.Is(err, ErrInvalidSeed) || seed.raw != nil {
			t.Fatalf("%s: ParseSeed answered %+v, %v; want ErrInvalidSeed", tc.name, seed, err)
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Fatalf("%s: reason %q does not name %q", tc.name, err.Error(), tc.wants)
		}
	}
	// The domain and its zero byte.
	for name, seed := range map[string][]byte{
		"other domain": append([]byte("extractor.reservation.v3"), base[len("extractor.reservation.v3"):]...),
		"no zero byte": concatBytes(base[:len(SeedDomain)], []byte{1}, base[len(SeedDomain)+1:]),
	} {
		if _, err := ParseSeed(seed); !errors.Is(err, ErrInvalidSeed) {
			t.Fatalf("%s: %v, want ErrInvalidSeed", name, err)
		}
	}
}

func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func TestCheckSeedBindsTheReservationToTheBatch(t *testing.T) {
	id := producer.Identity{ProducerID: proto.ID16{0x03}, RunID: proto.ID16{0x04}}
	ready := producer.Ready{ManifestDigest: goldenSeed.manifest, ManifestLength: 300, EventCount: 3}
	good := goldenSeed.bytes()
	seed, err := checkSeed(good, digestOf(good), 7, ready, id, goldenSeed.binding)
	if err != nil || seed.SequenceBase != 41 {
		t.Fatalf("checkSeed of the matching seed: %+v %v", seed, err)
	}

	cases := []struct {
		name   string
		change func(*contractSeed)
		digest func([]byte) []byte
		wants  error
	}{
		{"another producer", func(c *contractSeed) { c.producer = [16]byte{9} }, nil, ErrBlocked},
		{"another run", func(c *contractSeed) { c.run = [16]byte{9} }, nil, ErrBlocked},
		{"another batch", func(c *contractSeed) { c.seq++ }, nil, ErrBlocked},
		{"another manifest digest", func(c *contractSeed) { c.manifest = [32]byte{1} }, nil, ErrBlocked},
		{"another event count", func(c *contractSeed) { c.events++ }, nil, ErrBlocked},
		{"another binding", func(c *contractSeed) { c.binding = [32]byte{1} }, nil, ErrBlocked},
		{"zero frame limit", func(c *contractSeed) { c.frameLimit = 0 }, nil, ErrBlocked},
		{"digest of other bytes", nil, func([]byte) []byte { return make([]byte, 32) }, ErrBlocked},
		{"short digest", nil, func([]byte) []byte { return make([]byte, 31) }, ErrProtocol},
		{"malformed seed", func(c *contractSeed) { c.versions[4]++ }, nil, ErrInvalidSeed},
	}
	for _, tc := range cases {
		other := goldenSeed
		if tc.change != nil {
			tc.change(&other)
		}
		digest := digestOf(other.bytes())
		if tc.digest != nil {
			digest = tc.digest(other.bytes())
		}
		if _, err := checkSeed(other.bytes(), digest, 7, ready, id, goldenSeed.binding); !errors.Is(err, tc.wants) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.wants)
		}
	}
}
