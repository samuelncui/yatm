package tape

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

type cmGenerationRun struct {
	first   int
	records []uint32
	marks   []uint32
}

// cmGenerationImage builds one image for a named layout with independent framing code: a protected
// table pointing at an unprotected table that lists the directory and one EOD page per run.
func cmGenerationImage(t testing.TB, layout string, size int, runs ...cmGenerationRun) []byte {
	t.Helper()
	var spec *directoryLayout
	for i := range directoryLayouts {
		if directoryLayouts[i].name == layout {
			spec = &directoryLayouts[i]
		}
	}
	require.NotNil(t, spec)
	data := make([]byte, size)
	const table = 64
	binary.BigEndian.PutUint16(data[36:38], 0x0fff)
	binary.BigEndian.PutUint16(data[38:40], table)
	cmDirectoryTestSeal(data[36:44])

	// Directory: every entry starts unused, then runs fill their wraps with counts and datasets.
	dir := 256
	length := spec.header + spec.entries*spec.unit
	directory := data[dir : dir+length]
	binary.BigEndian.PutUint16(directory[:2], uint16(spec.version)<<12|0x103)
	binary.BigEndian.PutUint16(directory[2:4], uint16(length))
	cmDirectoryTestSeal(directory[:spec.header])
	entry := func(i int) []byte { return directory[spec.header+i*spec.unit : spec.header+(i+1)*spec.unit] }
	datasetOffset := 4
	if spec.passOffset < 0 {
		datasetOffset = 0
	}
	for i := 0; i < spec.entries; i++ {
		binary.BigEndian.PutUint32(entry(i)[datasetOffset:], 0xffffffff)
		cmDirectoryTestSeal(entry(i))
	}
	descriptors := [][2]uint16{{uint16(spec.version)<<12 | 0x103, uint16(dir)}}
	eodLength := 96
	if spec.halfWraps {
		eodLength = 64
	}
	address := dir + length
	for partition, run := range runs {
		var records, marks uint64
		for i := range run.records {
			raw := entry(run.first + i)
			if spec.passOffset >= 0 {
				binary.BigEndian.PutUint32(raw[spec.passOffset:], uint32(9+partition))
			}
			binary.BigEndian.PutUint32(raw[datasetOffset:], uint32(500+run.first+i))
			binary.BigEndian.PutUint32(raw[spec.recordOffsets[0]:], run.records[i])
			binary.BigEndian.PutUint32(raw[spec.filemarkOffset[0]:], run.marks[i])
			records, marks = records+uint64(run.records[i]), marks+uint64(run.marks[i])
			cmDirectoryTestSeal(raw)
		}
		id := eodPages[partition]
		eod := data[address : address+eodLength]
		word := 0x2000 | id
		binary.BigEndian.PutUint16(eod[:2], word)
		binary.BigEndian.PutUint16(eod[2:4], uint16(eodLength))
		binary.BigEndian.PutUint32(eod[4:8], uint32(9+partition))
		cmDirectoryTestPut48(eod[12:18], records)
		cmDirectoryTestPut48(eod[18:24], marks)
		terminal := run.first + len(run.records) - 1
		binary.BigEndian.PutUint32(eod[24:28], uint32(500+terminal))
		binary.BigEndian.PutUint32(eod[28:32], uint32(terminal))
		binary.BigEndian.PutUint16(eod[32:34], 1)
		cmDirectoryTestSeal(eod)
		descriptors = append(descriptors, [2]uint16{word, uint16(address)})
		address += eodLength
	}
	unprotected := data[table : table+len(descriptors)*4+8]
	for i, descriptor := range descriptors {
		binary.BigEndian.PutUint16(unprotected[i*4:], descriptor[0])
		binary.BigEndian.PutUint16(unprotected[i*4+2:], descriptor[1])
	}
	binary.BigEndian.PutUint16(unprotected[len(descriptors)*4:], 0x0fff)
	binary.BigEndian.PutUint16(unprotected[len(descriptors)*4+2:], uint16(address))
	cmDirectoryTestSeal(unprotected)
	return data
}

func TestCartridgeDirectoryGenerations(t *testing.T) {
	for _, test := range []struct {
		name, layout string
		size         int
		runs         []cmGenerationRun
		want         []WrapEndpoint
	}{
		// The shape of the public HP LTO-6 LTFS image: partition 0 on wrap 0, partition 1 after guard wraps.
		{name: "Ultrium-6 two partitions", layout: "Ultrium-6", size: 16352, runs: []cmGenerationRun{
			{first: 0, records: []uint32{3}, marks: []uint32{4}},
			{first: 4, records: []uint32{38615, 36779, 29910}, marks: []uint32{6, 0, 2}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 7}, {Partition: 1, Wrap: 4, Object: 38621},
				{Partition: 1, Wrap: 5, Object: 75400}, {Partition: 1, Wrap: 6, Object: 105312}}},
		{name: "Ultrium-9 single partition", layout: "Ultrium-9", size: 32736, runs: []cmGenerationRun{
			{first: 0, records: []uint32{10, 20}, marks: []uint32{1, 0}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 11}, {Wrap: 1, Object: 31}}},
		{name: "Ultrium-2 shares Ultrium-4's directory length", layout: "Ultrium-2", size: 4096, runs: []cmGenerationRun{
			{first: 0, records: []uint32{5}, marks: []uint32{1}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 6}}},
		// Ultrium-1 entries are half wraps: a wrap ends at its second half, and EOD can end a first half.
		{name: "Ultrium-1 half wraps", layout: "Ultrium-1", size: 4096, runs: []cmGenerationRun{
			{first: 0, records: []uint32{4, 6, 7}, marks: []uint32{0, 1, 0}}},
			want: []WrapEndpoint{{Wrap: 0, Object: 11}, {Wrap: 1, Object: 18}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed, err := decodeCartridgeDirectory(cmGenerationImage(t, test.layout, test.size, test.runs...), uint32(len(test.runs)))
			require.NoError(t, err)
			require.Equal(t, "LTO-CM directory "+test.layout, observed.Source)
			if !reflect.DeepEqual(observed.Endpoints, test.want) {
				t.Fatalf("endpoints=%v want=%v", observed.Endpoints, test.want)
			}
		})
	}
}

func TestCartridgeDirectoryFallsBackToTheNextBuffer(t *testing.T) {
	// A drive without HP's buffer 10h is asked for the IBM-documented 05h image instead.
	data := cmGenerationImage(t, "Ultrium-6", 16352, cmGenerationRun{first: 0, records: []uint32{3}, marks: []uint32{4}})
	fixture := newGeometryFixture()
	fixture.vendor, fixture.unsupported, fixture.actualPartitions = "IBM", true, 1
	var buffers []byte
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] != 0x3c {
			return fixture.execute(command)
		}
		buffers = append(buffers, command.CDB[2])
		if command.CDB[2] == 0x10 {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
		}
		if command.CDB[1] == 3 {
			copy(command.Data, []byte{0, 0, byte(len(data) >> 8), byte(len(data))})
		} else {
			offset := int(command.CDB[3])<<16 | int(command.CDB[4])<<8 | int(command.CDB[5])
			copy(command.Data, data[offset:])
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	device, err := NewDevice(t.Context(), Identity{}, transport)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, device.Cleanup(context.Background())) })
	_, err = device.Configure(t.Context(), &entity.TarMediaProfile{
		RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512,
	}, Read)
	require.NoError(t, err)
	observed, err := device.Geometry(t.Context())
	require.NoError(t, err)
	require.Equal(t, []WrapEndpoint{{Wrap: 0, Object: 7}}, observed.Endpoints)
	require.Equal(t, byte(0x10), buffers[0])
	require.Equal(t, byte(0x05), buffers[1])
}
