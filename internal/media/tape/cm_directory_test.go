package tape

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

// cmDirectoryTestSeal constructs the ECMA-319 generator rather than copying decoder coefficients.
func cmDirectoryTestSeal(data []byte) {
	// Build field logarithms from the primitive polynomial for independent polynomial arithmetic.
	var exp [510]byte
	var log [256]int
	value := 1
	for i := 0; i < 255; i++ {
		exp[i], exp[i+255], log[value] = byte(value), byte(value), i
		value <<= 1
		if value&0x100 != 0 {
			value ^= 0x11d
		}
	}
	multiply := func(a, b byte) byte {
		if a == 0 || b == 0 {
			return 0
		}
		return exp[log[a]+log[b]]
	}

	// Multiply the four root factors, then divide the padded message by that polynomial.
	generator := []byte{1}
	for power := 126; power <= 129; power++ {
		next := make([]byte, len(generator)+1)
		for i, coefficient := range generator {
			next[i] ^= coefficient
			next[i+1] ^= multiply(coefficient, exp[power])
		}
		generator = next
	}
	work := bytes.Clone(data)
	clear(work[len(work)-4:])
	for i := 0; i < len(work)-4; i++ {
		factor := work[i]
		for j, coefficient := range generator {
			work[i+j] ^= multiply(factor, coefficient)
		}
	}
	copy(data[len(data)-4:], work[len(work)-4:])
}

func cmDirectoryTestPut48(data []byte, value uint64) {
	binary.BigEndian.PutUint16(data[:2], uint16(value>>32))
	binary.BigEndian.PutUint32(data[2:6], uint32(value))
}

type cmDirectoryTestRun struct {
	first  int
	counts [][4]uint32
}

type cmDirectoryTestLayout struct {
	data       []byte
	table, dir int
	eods       []int
}

func cmDirectoryTestImage(t testing.TB, shift int, runs ...cmDirectoryTestRun) cmDirectoryTestLayout {
	t.Helper()
	// Two independently checksummed tables describe relocated pages, without production table helpers.
	fixture := cmDirectoryTestLayout{data: make([]byte, 8160), table: 64 + shift, dir: 128 + 2*shift}
	binary.BigEndian.PutUint16(fixture.data[36:38], 0x0fff)
	binary.BigEndian.PutUint16(fixture.data[38:40], uint16(fixture.table))
	cmDirectoryTestSeal(fixture.data[36:44])
	table := fixture.data[fixture.table : fixture.table+32]
	binary.BigEndian.PutUint16(table[:2], 0x0ffc)
	binary.BigEndian.PutUint16(table[2:4], uint16(fixture.dir))
	binary.BigEndian.PutUint16(table[4:6], 0x4103)
	binary.BigEndian.PutUint16(table[6:8], uint16(fixture.dir))
	for offset := 8; offset < 24; offset += 4 {
		binary.BigEndian.PutUint16(table[offset:offset+2], 0x0ffe)
	}

	// Invalid dataset sentinels delimit runs; flags and partition-start heuristics are unnecessary.
	directory := fixture.data[fixture.dir : fixture.dir+2576]
	binary.BigEndian.PutUint16(directory[:2], 0x4103)
	binary.BigEndian.PutUint16(directory[2:4], uint16(len(directory)))
	cmDirectoryTestSeal(directory[:16])
	for i := 0; i < 80; i++ {
		entry := directory[16+i*32 : 48+i*32]
		binary.BigEndian.PutUint32(entry[4:8], 0xffffffff)
		cmDirectoryTestSeal(entry)
	}
	for partition, run := range runs {
		if len(run.counts) == 0 || run.first < 0 || run.first+len(run.counts) > 80 || partition > 1 {
			t.Fatal("invalid synthetic CM run")
		}
		var records, marks uint64
		for i, counts := range run.counts {
			wrap := run.first + i
			entry := directory[16+wrap*32 : 48+wrap*32]
			binary.BigEndian.PutUint32(entry[:4], uint32(7+partition))
			binary.BigEndian.PutUint32(entry[4:8], uint32(100+wrap))
			for j, count := range counts {
				binary.BigEndian.PutUint32(entry[8+j*4:12+j*4], count)
			}
			records += uint64(counts[0]) + uint64(counts[1])
			marks += uint64(counts[2]) + uint64(counts[3])
			cmDirectoryTestSeal(entry)
		}

		// EOD totals independently bind the whole run and its terminal pass/dataset.
		address := fixture.dir + 2576 + 64 + shift + partition*128
		fixture.eods = append(fixture.eods, address)
		eod := fixture.data[address : address+96]
		id := uint16(0x2104)
		if partition == 1 {
			id = 0x210e
		}
		binary.BigEndian.PutUint16(eod[:2], id)
		binary.BigEndian.PutUint16(eod[2:4], 96)
		binary.BigEndian.PutUint32(eod[4:8], uint32(7+partition))
		cmDirectoryTestPut48(eod[12:18], records)
		cmDirectoryTestPut48(eod[18:24], marks)
		terminal := uint32(run.first + len(run.counts) - 1)
		binary.BigEndian.PutUint32(eod[24:28], 100+terminal)
		binary.BigEndian.PutUint32(eod[28:32], terminal)
		binary.BigEndian.PutUint16(eod[32:34], 1)
		cmDirectoryTestSeal(eod)
		binary.BigEndian.PutUint16(table[8+partition*4:10+partition*4], id)
		binary.BigEndian.PutUint16(table[10+partition*4:12+partition*4], uint16(address))
	}

	// Close the address envelope after every referenced page, retaining zeroed unrelated storage.
	end := fixture.dir + 2576
	if len(fixture.eods) != 0 {
		end = fixture.eods[len(fixture.eods)-1] + 96
	}
	binary.BigEndian.PutUint16(table[24:26], 0x0fff)
	binary.BigEndian.PutUint16(table[26:28], uint16(end))
	cmDirectoryTestSeal(table[4:])
	return fixture
}

func TestCartridgeDirectoryFixtureHashes(t *testing.T) {
	// Sanitized hardware captures are frozen inputs; any byte change needs a new reviewed fixture.
	for name, want := range map[string]string{
		"hp-lto5-directory-v4.bin":        "2443c04238f06ff7a7b0790363ec50637a7ec6cb1b3536f4e170a8a8962f1d6c",
		"hp-lto5-single-partition-v4.bin": "c1bea484e78ea69796c50d94651f12feeec2d79473a91f39c9788da07de6c7f3",
	} {
		data, err := os.ReadFile("testdata/cm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Fatalf("%s sha256=%x", name, sum)
		}
	}
}

func TestCartridgeDirectoryCapture(t *testing.T) {
	// The sanitized hardware image and numeric REOWP answer are frozen independently of the decoder.
	data, err := os.ReadFile("testdata/cm/hp-lto5-directory-v4.bin")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile("testdata/cm/hp-lto5-directory-v4-endpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []WrapEndpoint
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(data)
	observed, err := decodeCartridgeDirectory(data, 2)
	if err != nil || observed == nil {
		t.Fatalf("decode captured CM: %v", err)
	}

	// Exact exclusive endpoints include both filemark totals and the partial final wrap.
	if len(want) != 23 || !reflect.DeepEqual(observed.Endpoints, want) || !observed.Valid() || !observed.AllWraps ||
		observed.EndpointConvention != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE ||
		observed.Fresh != nil || observed.ObservedAt.IsZero() || !bytes.Equal(before, data) {
		t.Fatalf("captured CM map=%+v", observed)
	}

	// The independent polynomial encoder agrees with every retained observed codeword.
	ranges := [][2]int{{36, 64}, {260, 352}, {1968, 1984}, {4544, 4640}, {4640, 4736}}
	for i := 0; i < 80; i++ {
		ranges = append(ranges, [2]int{1984 + i*32, 2016 + i*32})
	}
	for _, bounds := range ranges {
		codeword := bytes.Clone(data[bounds[0]:bounds[1]])
		cmDirectoryTestSeal(codeword)
		if !bytes.Equal(codeword, data[bounds[0]:bounds[1]]) {
			t.Fatalf("reference RS differs at %d", bounds[0])
		}
	}
}

func TestCartridgeDirectoryActualSinglePartitionCapture(t *testing.T) {
	// The real FORMAT retained a valid second EOD page with state zero despite one physical partition.
	data, err := os.ReadFile("testdata/cm/hp-lto5-single-partition-v4.bin")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile("testdata/cm/hp-lto5-single-partition-v4-endpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []WrapEndpoint
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Fatal(err)
	}
	observed, err := decodeCartridgeDirectory(data, 1)
	if err != nil || observed == nil || len(want) != 35 || !reflect.DeepEqual(observed.Endpoints, want) {
		t.Fatalf("single partition captured map=%+v err=%v", observed, err)
	}
	if binary.BigEndian.Uint16(data[4640:4642]) != 0x210e || binary.BigEndian.Uint16(data[4672:4674]) != 0 {
		t.Fatal("fixture no longer retains the inactive EOD page")
	}

	// Inactive payload is not a second partition even if it retains an old completed state and counters.
	stale := bytes.Clone(data)
	copy(stale[4644:4732], stale[4548:4636])
	cmDirectoryTestSeal(stale[4640:4736])
	accepted, err := decodeCartridgeDirectory(stale, 1)
	if err != nil || accepted == nil || !reflect.DeepEqual(accepted.Endpoints, want) {
		t.Fatalf("stale inactive EOD changed active endpoints: %+v %v", accepted, err)
	}
	for _, count := range []uint32{0, 2, 3, 0xffffffff} {
		if got, err := decodeCartridgeDirectory(data, count); got != nil || err == nil {
			t.Fatalf("unproved partition count %d accepted", count)
		}
	}

	// The inactive page is still structurally checked; ignoring it cannot conceal corrupt framing.
	for _, change := range []func([]byte){
		func(value []byte) { value[4735] ^= 1 },
		func(value []byte) { value[4643] = 95 },
	} {
		bad := bytes.Clone(data)
		change(bad)
		if got, err := decodeCartridgeDirectory(bad, 1); got != nil || err == nil {
			t.Fatal("corrupt inactive page accepted")
		}
	}
}

func TestCartridgeDirectoryLayouts(t *testing.T) {
	for _, test := range []struct {
		name  string
		shift int
		runs  []cmDirectoryTestRun
		want  []WrapEndpoint
	}{
		{name: "single partition", runs: []cmDirectoryTestRun{{first: 0, counts: [][4]uint32{{10, 2, 3, 1}}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 16}}},
		{name: "relocated tables and pages", shift: 512,
			runs: []cmDirectoryTestRun{{first: 0, counts: [][4]uint32{{10, 2, 3, 1}}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 16}}},
		{name: "alternate partition starts", runs: []cmDirectoryTestRun{
			{first: 3, counts: [][4]uint32{{10, 0, 0, 0}, {20, 0, 1, 0}}},
			{first: 12, counts: [][4]uint32{{30, 0, 0, 2}, {1, 0, 0, 0}}}},
			want: []WrapEndpoint{{Wrap: 3, Object: 10}, {Wrap: 4, Object: 31},
				{Partition: 1, Wrap: 12, Object: 32}, {Partition: 1, Wrap: 13, Object: 33}}},
		{name: "partition order differs from wrap order", runs: []cmDirectoryTestRun{
			{first: 12, counts: [][4]uint32{{5, 0, 0, 0}}}, {first: 3, counts: [][4]uint32{{7, 0, 0, 0}}}},
			want: []WrapEndpoint{{Wrap: 12, Object: 5}, {Partition: 1, Wrap: 3, Object: 7}}},
		{name: "counts cross uint32", runs: []cmDirectoryTestRun{{first: 0,
			counts: [][4]uint32{{0xffffffff, 2, 0xffffffff, 3}, {4, 5, 6, 7}}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 2*(1<<32) + 3}, {Wrap: 1, Object: 2*(1<<32) + 25}}},
		{name: "empty completed run", runs: []cmDirectoryTestRun{{first: 0, counts: [][4]uint32{{}}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 0}}},
		{name: "filemarks only", runs: []cmDirectoryTestRun{{first: 2, counts: [][4]uint32{{0, 0, 7, 2}}}},
			want: []WrapEndpoint{{Wrap: 2, Object: 9}}},
		{name: "partial final wrap", runs: []cmDirectoryTestRun{{first: 78,
			counts: [][4]uint32{{1000, 0, 0, 1}, {17, 0, 0, 1}}}},
			want: []WrapEndpoint{{Wrap: 78, Object: 1001}, {Wrap: 79, Object: 1019}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := cmDirectoryTestImage(t, test.shift, test.runs...)
			observed, err := decodeCartridgeDirectory(fixture.data, uint32(len(test.runs)))
			if err != nil || observed == nil || !reflect.DeepEqual(observed.Endpoints, test.want) {
				t.Fatalf("map=%+v err=%v want=%v", observed, err, test.want)
			}
		})
	}
}

func TestCartridgeDirectoryMalformed(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*cmDirectoryTestLayout)
		want error
	}{
		{name: "protected table checksum", edit: func(f *cmDirectoryTestLayout) { f.data[40] ^= 1 }},
		{name: "unprotected table checksum", edit: func(f *cmDirectoryTestLayout) { f.data[f.table+28] ^= 1 }},
		{name: "directory header checksum", edit: func(f *cmDirectoryTestLayout) { f.data[f.dir+12] ^= 1 }},
		{name: "directory valid entry checksum", edit: func(f *cmDirectoryTestLayout) { f.data[f.dir+16+28] ^= 1 }},
		{name: "directory invalid entry checksum", edit: func(f *cmDirectoryTestLayout) { f.data[f.dir+16+79*32+28] ^= 1 }},
		{name: "EOD checksum", edit: func(f *cmDirectoryTestLayout) { f.data[f.eods[0]+92] ^= 1 }},
		{name: "directory header length", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.dir+2:f.dir+4], 2572)
			cmDirectoryTestSeal(f.data[f.dir : f.dir+16])
		}},
		{name: "EOD header length", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.eods[0]+2:f.eods[0]+4], 92)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "directory version", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+4:f.table+6], 0x5103)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
			binary.BigEndian.PutUint16(f.data[f.dir:f.dir+2], 0x5103)
			cmDirectoryTestSeal(f.data[f.dir : f.dir+16])
		}},
		{name: "EOD version", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+8:f.table+10], 0x3104)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
			binary.BigEndian.PutUint16(f.data[f.eods[0]:f.eods[0]+2], 0x3104)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "page identity mismatch", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+8:f.table+10], 0x3104)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "missing directory", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+4:f.table+6], 0x0ffe)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "missing EOD", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+8:f.table+10], 0x0ffe)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "duplicate directory ID with another version", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+16:f.table+18], 0x5103)
			binary.BigEndian.PutUint16(f.data[f.table+18:f.table+20], uint16(f.dir))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "too many partitions", want: ErrUnsupported, edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+16:f.table+18], 0x210f)
			binary.BigEndian.PutUint16(f.data[f.table+18:f.table+20], uint16(f.dir+2576))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "out of image EOD", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+10:f.table+12], 0xfffc)
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "unaligned page", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+10:f.table+12], uint16(f.eods[0]+1))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "EOD over declared end", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+26:f.table+28], uint16(f.eods[0]+92))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "empty directory", edit: func(f *cmDirectoryTestLayout) {
			for i := 0; i < 2; i++ {
				entry := f.data[f.dir+16+i*32 : f.dir+48+i*32]
				binary.BigEndian.PutUint32(entry[4:8], 0xfffffffe)
				cmDirectoryTestSeal(entry)
			}
		}},
		{name: "interrupted writing EOD", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.eods[0]+32:f.eods[0]+34], 2)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "unknown EOD state", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.eods[0]+32:f.eods[0]+34], 0)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "failed EOD state", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.eods[0]+32:f.eods[0]+34], 3)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "terminal outside directory", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint32(f.data[f.eods[0]+28:f.eods[0]+32], 80)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "terminal inside run", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint32(f.data[f.eods[0]+28:f.eods[0]+32], 0)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "terminal pass mismatch", edit: func(f *cmDirectoryTestLayout) {
			f.data[f.eods[0]+7] ^= 1
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "terminal dataset mismatch", edit: func(f *cmDirectoryTestLayout) {
			f.data[f.eods[0]+27] ^= 1
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "record and filemark swap preserves combined total", edit: func(f *cmDirectoryTestLayout) {
			cmDirectoryTestPut48(f.data[f.eods[0]+12:f.eods[0]+18], 11)
			cmDirectoryTestPut48(f.data[f.eods[0]+18:f.eods[0]+24], 4)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "unexplained record high word", edit: func(f *cmDirectoryTestLayout) {
			cmDirectoryTestPut48(f.data[f.eods[0]+12:f.eods[0]+18], 1<<32+12)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "must not select suffix to fit EOD total", edit: func(f *cmDirectoryTestLayout) {
			cmDirectoryTestPut48(f.data[f.eods[0]+12:f.eods[0]+18], 2)
			cmDirectoryTestPut48(f.data[f.eods[0]+18:f.eods[0]+24], 0)
			cmDirectoryTestSeal(f.data[f.eods[0] : f.eods[0]+96])
		}},
		{name: "unknown page overlaps directory", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+16:f.table+18], 0x1200)
			binary.BigEndian.PutUint16(f.data[f.table+18:f.table+20], uint16(f.dir+16))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "EOD page overlaps directory", edit: func(f *cmDirectoryTestLayout) {
			copy(f.data[f.dir+16:f.dir+112], f.data[f.eods[0]:f.eods[0]+96])
			binary.BigEndian.PutUint16(f.data[f.table+10:f.table+12], uint16(f.dir+16))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
		{name: "page lies inside page table", edit: func(f *cmDirectoryTestLayout) {
			binary.BigEndian.PutUint16(f.data[f.table+10:f.table+12], uint16(f.table+24))
			cmDirectoryTestSeal(f.data[f.table+4 : f.table+32])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Corrupt only the stated fact, resealing checksums when semantic validation is under test.
			fixture := cmDirectoryTestImage(t, 0, cmDirectoryTestRun{first: 0, counts: [][4]uint32{{10, 0, 3, 0}, {2, 0, 0, 0}}})
			test.edit(&fixture)
			observed, err := decodeCartridgeDirectory(fixture.data, 1)
			if observed != nil || err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("invalid map=%+v err=%v want=%v", observed, err, test.want)
			}
		})
	}
}

func TestCartridgeDirectoryRunOwnership(t *testing.T) {
	for _, name := range []string{"unmapped run", "same terminal", "adjacent partitions", "interior invalid marker"} {
		t.Run(name, func(t *testing.T) {
			// Each partition must own one entire run, regardless of how tempting partial totals look.
			fixture := cmDirectoryTestImage(t, 0,
				cmDirectoryTestRun{first: 0, counts: [][4]uint32{{4, 0, 0, 0}}},
				cmDirectoryTestRun{first: 4, counts: [][4]uint32{{5, 0, 0, 0}, {2, 0, 0, 0}, {3, 0, 0, 0}}})
			switch name {
			case "unmapped run":
				entry := fixture.data[fixture.dir+16+12*32 : fixture.dir+48+12*32]
				copy(entry, fixture.data[fixture.dir+16:fixture.dir+48])
			case "same terminal":
				copy(fixture.data[fixture.eods[1]+4:fixture.eods[1]+92],
					fixture.data[fixture.eods[0]+4:fixture.eods[0]+92])
				cmDirectoryTestSeal(fixture.data[fixture.eods[1] : fixture.eods[1]+96])
			case "adjacent partitions":
				for i := 1; i < 4; i++ {
					copy(fixture.data[fixture.dir+16+i*32:fixture.dir+48+i*32],
						fixture.data[fixture.dir+16:fixture.dir+48])
				}
			case "interior invalid marker":
				entry := fixture.data[fixture.dir+16+5*32 : fixture.dir+48+5*32]
				binary.BigEndian.PutUint32(entry[4:8], 0xfffffffe)
				cmDirectoryTestSeal(entry)
			}
			if observed, err := decodeCartridgeDirectory(fixture.data, 2); observed != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("ambiguous run map=%+v err=%v", observed, err)
			}
		})
	}
}

func TestCartridgeDirectoryTruncation(t *testing.T) {
	// Every header/table/page boundary must stay within the actual received image.
	fixture := cmDirectoryTestImage(t, 0, cmDirectoryTestRun{first: 0, counts: [][4]uint32{{10, 0, 3, 0}}})
	for _, size := range []int{0, 35, 36, 40, 43, 63, 64, 92, 95, 127, 128, 143,
		fixture.dir + 2575, fixture.eods[0] + 95, 8193} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := fixture.data
			if size > len(data) {
				data = append(bytes.Clone(data), make([]byte, size-len(data))...)
			} else {
				data = data[:size]
			}
			if observed, err := decodeCartridgeDirectory(data, 1); observed != nil || err == nil {
				t.Fatalf("truncated image returned map=%+v err=%v", observed, err)
			}
		})
	}
}

func BenchmarkCartridgeDirectory(b *testing.B) {
	// Fixture reads and independent synthetic construction are excluded from parser measurements.
	data, err := os.ReadFile("testdata/cm/hp-lto5-directory-v4.bin")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		observed, err := decodeCartridgeDirectory(data, 2)
		if err != nil || observed == nil || len(observed.Endpoints) != 23 {
			b.Fatalf("map=%+v err=%v", observed, err)
		}
	}
}
