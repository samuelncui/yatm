package media

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
)

func TestParseCurrentLTFSReadIndexBindsIdentityWithoutHistoricalXML(t *testing.T) {
	// The same stream parser validates current-mount identity before publishing read extents.
	const id = "204d1c2f-c755-44cf-ac70-8541ded5f005"
	index := `<ltfsindex version="2.4.0"><volumeuuid>` + id + `</volumeuuid><generationnumber>7</generationnumber>
<directory><name>TAPE01</name><contents><file><name>file</name><length>5</length><extentinfo>
<extent><fileoffset>0</fileoffset><partition>b</partition><startblock>4</startblock><byteoffset>0</byteoffset><bytecount>5</bytecount></extent>
</extentinfo></file></contents></directory></ltfsindex>`
	var entries []*LTFSIndexEntry
	require.NoError(t, ParseLTFSReadIndex(t.Context(), strings.NewReader(index), id, 7,
		func(entry *LTFSIndexEntry) error { entries = append(entries, entry); return nil }))
	require.Len(t, entries, 1)
	require.Equal(t, "file", entries[0].Path)
	require.Equal(t, int64(5), entries[0].Size)
	require.Equal(t, uint64(4), entries[0].Storage.Metadata.GetLtfs().Extents[0].StartBlock)

	// An absent, stale or malformed capture cannot supply a partially accepted alternative source.
	for _, invalid := range []string{
		strings.Replace(index, id, "other", 1),
		strings.Replace(index, ">7<", ">6<", 1),
		strings.Replace(index, "<volumeuuid>"+id+"</volumeuuid>", "", 1),
		strings.Replace(index, "<generationnumber>7</generationnumber>", "", 1),
		strings.Replace(index, "<directory>", "<volumeuuid>"+id+"</volumeuuid><directory>", 1),
		strings.ReplaceAll(index, "ltfsindex", "wrongroot"),
		strings.Replace(index, "<volumeuuid>", "<future><volumeuuid>", 1),
		strings.Replace(strings.Replace(index, "<volumeuuid>", "<future><volumeuuid>", 1), "</generationnumber>", "</generationnumber></future>", 1),
		strings.Replace(index, `<ltfsindex version="2.4.0">`, `<ltfsindex xmlns="urn:other" version="2.4.0">`, 1),
		index + index,
		index + "outside",
		index[:len(index)-5],
	} {
		require.Error(t, ParseLTFSReadIndex(t.Context(), strings.NewReader(invalid), id, 7, func(*LTFSIndexEntry) error { return nil }))
	}
	stop := errors.New("planner callback failure")
	require.ErrorIs(t, ParseLTFSReadIndex(t.Context(), strings.NewReader(index), id, 7,
		func(*LTFSIndexEntry) error { return stop }), stop)
}

func TestCurrentLTFSReadIndexBoundsOneFragmentedFile(t *testing.T) {
	// A file with too many extents is refused during streaming, before building its complete slice.
	index := `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber><directory><name>TAPE</name><contents><file><name>file</name><length>4097</length><extentinfo>` +
		strings.Repeat(`<extent><partition>b</partition><startblock>4</startblock><bytecount>1</bytecount></extent>`, readplan.MaxRanges+1) +
		`</extentinfo></file></contents></directory></ltfsindex>`
	called := false
	err := ParseLTFSReadIndex(t.Context(), strings.NewReader(index), "volume", 1, func(*LTFSIndexEntry) error {
		called = true
		return nil
	})
	require.ErrorContains(t, err, "extent count")
	require.False(t, called)
}

type ltfsRepeatingReader struct{ read int64 }

func (r *ltfsRepeatingReader) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = 'a'
	}
	r.read += int64(len(data))
	return len(data), nil
}

func TestCurrentLTFSReadIndexBoundsTokenBeforeAllocation(t *testing.T) {
	// An unending character token must stop at the input budget, before a full token can be materialized.
	repeated := new(ltfsRepeatingReader)
	input := io.MultiReader(strings.NewReader(`<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber><directory><name>TAPE</name><contents><file><name>`), repeated)
	err := ParseLTFSReadIndex(t.Context(), input, "volume", 1, func(*LTFSIndexEntry) error {
		t.Fatal("an oversized file was yielded")
		return nil
	})
	require.ErrorContains(t, err, "bounded input allowance")
	require.LessOrEqual(t, repeated.read, int64(ltfsReadIndexBytes))
}

func TestCurrentLTFSReadIndexBoundsOpenDirectoryNames(t *testing.T) {
	// Every name fits the per-token allowance, but their live ancestors exceed the shared byte bound.
	const opening = `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber>`
	name := strings.Repeat("a", 1<<20)
	var parts []io.Reader
	parts = append(parts, strings.NewReader(opening))
	for range 9 {
		parts = append(parts, strings.NewReader(`<directory><name>`), strings.NewReader(name), strings.NewReader(`</name><contents>`))
	}
	parts = append(parts, strings.NewReader(strings.Repeat(`</contents></directory>`, 9)+`</ltfsindex>`))
	err := ParseLTFSReadIndex(t.Context(), io.MultiReader(parts...), "volume", 1, func(*LTFSIndexEntry) error {
		t.Fatal("an oversized path was yielded")
		return nil
	})
	require.ErrorContains(t, err, "directory names exceed")
}

func TestCurrentLTFSReadIndexBoundsOpenXMLNames(t *testing.T) {
	// A small nesting count can still retain oversized XML names across individually bounded tokens.
	for _, namespace := range []bool{false, true} {
		var index strings.Builder
		index.WriteString(`<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber><directory><name>TAPE</name><contents>`)
		for i := range 9 {
			value := strings.Repeat(string(rune('a'+i)), 1<<20)
			if namespace {
				index.WriteString(`<future xmlns:p="` + value + `">`)
			} else {
				index.WriteString(`<` + value + `>`)
			}
		}
		for i := 8; i >= 0; i-- {
			if namespace {
				index.WriteString(`</future>`)
			} else {
				index.WriteString(`</` + strings.Repeat(string(rune('a'+i)), 1<<20) + `>`)
			}
		}
		index.WriteString(`</contents></directory></ltfsindex>`)
		err := ParseLTFSReadIndex(t.Context(), strings.NewReader(index.String()), "volume", 1, func(*LTFSIndexEntry) error { return nil })
		require.ErrorContains(t, err, "XML frames exceed")
		require.NoError(t, ParseLTFSIndex(t.Context(), strings.NewReader(index.String()), func(*LTFSIndexEntry) error { return nil }))
	}
}

func TestCurrentLTFSReadIndexBoundsNesting(t *testing.T) {
	// Token-loop directories and nested DecodeElement/Skip paths must share the same depth bound.
	const opening = `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber>`
	const directory = `<directory><name>TAPE</name><contents>`
	deep := strings.Repeat(`<future>`, readplan.MaxRanges) + strings.Repeat(`</future>`, readplan.MaxRanges)
	for _, test := range []struct{ name, body string }{
		{name: "directories", body: strings.Repeat(directory, readplan.MaxRanges) + strings.Repeat(`</contents></directory>`, readplan.MaxRanges)},
		{name: "skipped extension", body: `<extension>` + deep + `</extension>` + directory + `</contents></directory>`},
		{name: "directory name", body: `<directory><name>TAPE` + deep + `</name></directory>`},
		{name: "file extension", body: directory + `<file><name>file</name><length>0</length>` + deep + `</file></contents></directory>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			index := opening + test.body + `</ltfsindex>`
			err := ParseLTFSReadIndex(t.Context(), strings.NewReader(index), "volume", 1, func(*LTFSIndexEntry) error { return nil })
			require.ErrorContains(t, err, "nesting exceeds")

			// Optional planning limits do not restrict ordinary inventory parsing.
			require.NoError(t, ParseLTFSIndex(t.Context(), strings.NewReader(index), func(*LTFSIndexEntry) error { return nil }))
		})
	}
}

func TestCurrentLTFSReadIndexReleasesClosedDirectoryBudget(t *testing.T) {
	// Closed siblings reuse the live-name allowance; total Index size is deliberately not capped.
	name := strings.Repeat("a", 1<<20)
	var parts []io.Reader
	parts = append(parts, strings.NewReader(`<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber><directory><name>TAPE</name><contents>`))
	for range 10 {
		parts = append(parts, strings.NewReader(`<directory><name>`), strings.NewReader(name), strings.NewReader(`</name></directory>`))
	}
	parts = append(parts, strings.NewReader(`<file><name>file</name><length>0</length></file></contents></directory></ltfsindex>`))
	var paths []string
	require.NoError(t, ParseLTFSReadIndex(t.Context(), io.MultiReader(parts...), "volume", 1, func(entry *LTFSIndexEntry) error {
		paths = append(paths, entry.Path)
		return nil
	}))
	require.Equal(t, []string{"file"}, paths)
}

func TestCurrentLTFSReadIndexExtentStaysWithinFile(t *testing.T) {
	// Use declared EOF, including unsigned overflow cases, without forbidding legitimate sparse holes.
	for _, test := range []struct {
		name, size, offset, bytes string
		valid                     bool
	}{
		{name: "cross EOF", size: "1", offset: "0", bytes: "5"},
		{name: "start after EOF", size: "5", offset: "6", bytes: "0"},
		{name: "overflow", size: "5", offset: "18446744073709551614", bytes: "4"},
		{name: "huge count", size: "5", offset: "1", bytes: "18446744073709551615"},
		{name: "ends at EOF", size: "5", offset: "0", bytes: "5", valid: true},
		{name: "sparse prefix", size: "5", offset: "4", bytes: "1", valid: true},
		{name: "sparse suffix", size: "5", offset: "1", bytes: "1", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			index := `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber><directory><name>TAPE</name><contents><file><name>file</name><length>` + test.size + `</length><extentinfo><extent><partition>b</partition><startblock>4</startblock><fileoffset>` + test.offset + `</fileoffset><bytecount>` + test.bytes + `</bytecount></extent></extentinfo></file></contents></directory></ltfsindex>`
			count := 0
			err := ParseLTFSReadIndex(t.Context(), strings.NewReader(index), "volume", 1, func(*LTFSIndexEntry) error { count++; return nil })
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, 1, count)
			} else {
				require.ErrorContains(t, err, "extent exceeds declared file size")
				require.Zero(t, count)
			}

			// The ordinary inventory path preserves its existing representation and validation boundary.
			require.NoError(t, ParseLTFSIndex(t.Context(), strings.NewReader(index), func(*LTFSIndexEntry) error { return nil }))
		})
	}
}

type ltfsCancelingReader struct {
	reader io.Reader
	cancel context.CancelFunc
	read   int
}

func (r *ltfsCancelingReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	r.read += n
	if r.read >= 8192 {
		r.cancel()
	}
	return n, err
}

func TestCurrentLTFSReadIndexCancelsInsideElement(t *testing.T) {
	// Cancellation while Skip or DecodeElement owns the token loop must stop before consuming its full allowance.
	for _, body := range []string{
		`<extension>` + strings.Repeat(`<future/>`, 10000) + `</extension>`,
		`<directory><name>TAPE</name><contents><file><name>` + strings.Repeat("a", 1<<20) + `</name><length>0</length></file></contents></directory>`,
	} {
		ctx, cancel := context.WithCancel(t.Context())
		input := &ltfsCancelingReader{reader: strings.NewReader(`<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber>` + body + `</ltfsindex>`), cancel: cancel}
		err := ParseLTFSReadIndex(ctx, input, "volume", 1, func(*LTFSIndexEntry) error { t.Fatal("a canceled file was yielded"); return nil })
		cancel()
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 8192, input.read)
	}
}

func BenchmarkCurrentLTFSReadIndex(b *testing.B) {
	// Freeze normal flat and nested inputs to measure planning parser overhead independently of disk I/O.
	const opening = `<ltfsindex><volumeuuid>volume</volumeuuid><generationnumber>1</generationnumber>`
	const directory = `<directory><name>folder</name><contents>`
	const file = `<file><name>file</name><length>5</length><extentinfo><extent><partition>b</partition><startblock>4</startblock><bytecount>5</bytecount></extent></extentinfo></file>`
	for _, test := range []struct {
		name, body string
		files      int
	}{
		{name: "flat_1000", body: directory + strings.Repeat(file, 1000) + `</contents></directory>`, files: 1000},
		{name: "nested_64", body: strings.Repeat(directory, 64) + file + strings.Repeat(`</contents></directory>`, 64), files: 1},
	} {
		b.Run(test.name, func(b *testing.B) {
			index := opening + test.body + `</ltfsindex>`
			b.ReportAllocs()
			b.SetBytes(int64(len(index)))
			for b.Loop() {
				count := 0
				err := ParseLTFSReadIndex(b.Context(), strings.NewReader(index), "volume", 1, func(*LTFSIndexEntry) error {
					count++
					return nil
				})
				if err != nil || count != test.files {
					b.Fatalf("entries=%d want=%d err=%v", count, test.files, err)
				}
			}
		})
	}
}

func TestParseLTFSIndexPreservesExtentsAndPhysicalOrder(t *testing.T) {
	index := `<?xml version="1.0" encoding="UTF-8"?>
<ltfsindex version="2.4.0">
  <directory>
    <name>ABC001</name>
    <contents>
      <directory>
        <name percentencoded="true">folder%20name</name>
        <contents>
          <file>
            <name percentencoded="true">file%2Etxt</name>
            <length>7</length>
            <extentinfo>
              <extent><fileoffset>4</fileoffset><partition>b</partition><startblock>50</startblock><byteoffset>0</byteoffset><bytecount>3</bytecount></extent>
              <extent><fileoffset>0</fileoffset><partition>b</partition><startblock>40</startblock><byteoffset>3</byteoffset><bytecount>4</bytecount></extent>
            </extentinfo>
          </file>
          <file><name>empty.txt</name><length>0</length></file>
        </contents>
      </directory>
    </contents>
  </directory>
</ltfsindex>`
	var entries []*LTFSIndexEntry
	require.NoError(t, ParseLTFSIndex(context.Background(), strings.NewReader(index), func(entry *LTFSIndexEntry) error {
		entries = append(entries, entry)
		return nil
	}))

	require.Len(t, entries, 2)
	require.Equal(t, "folder name/file.txt", entries[0].Path)
	require.Len(t, entries[0].Storage.Order, 17)
	require.Equal(t, byte('b'), entries[0].Storage.Order[0])
	require.Equal(t, uint64(40), binary.BigEndian.Uint64(entries[0].Storage.Order[1:9]))
	require.Equal(t, uint64(3), binary.BigEndian.Uint64(entries[0].Storage.Order[9:17]))
	require.Len(t, entries[0].Storage.Metadata.GetLtfs().Extents, 2)
	require.Equal(t, uint64(50), entries[0].Storage.Metadata.GetLtfs().Extents[0].StartBlock)
	require.Equal(t, uint64(40), entries[0].Storage.Metadata.GetLtfs().Extents[1].StartBlock)
	require.Equal(t, "folder name/empty.txt", entries[1].Path)
	require.Empty(t, entries[1].Storage.Order)
	require.Empty(t, entries[1].Storage.Metadata.GetLtfs().Extents)
}

func TestParseLTFSIndexPreservesIndexPartition(t *testing.T) {
	index := `<ltfsindex><directory><name>IDX001</name><contents>
<file><name>small.txt</name><length>5</length><extentinfo>
<extent><fileoffset>0</fileoffset><partition>a</partition><startblock>4</startblock>
<byteoffset>0</byteoffset><bytecount>5</bytecount></extent>
</extentinfo></file></contents></directory></ltfsindex>`
	var entries []*LTFSIndexEntry

	// Decode a placement-policy file whose physical extent is in the index partition.
	require.NoError(t, ParseLTFSIndex(context.Background(), strings.NewReader(index), func(entry *LTFSIndexEntry) error {
		entries = append(entries, entry)
		return nil
	}))

	// Preserve the partition in both the sortable order and typed storage metadata.
	require.Len(t, entries, 1)
	require.Len(t, entries[0].Storage.Order, 17)
	require.Equal(t, byte('a'), entries[0].Storage.Order[0])
	require.Equal(t, "a", entries[0].Storage.Metadata.GetLtfs().Extents[0].Partition)
}

func TestParseLTFSIndexRejectsInvalidData(t *testing.T) {
	tests := []struct {
		name  string
		index string
		want  string
	}{
		{
			name:  "missing extent",
			index: `<ltfsindex><directory><name>ABC001</name><contents><file><name>file.txt</name><length>1</length></file></contents></directory></ltfsindex>`,
			want:  "extent is missing",
		},
		{
			name:  "invalid encoded name",
			index: `<ltfsindex><directory><name>ABC001</name><contents><file><name percentencoded="true">bad%ZZ</name><length>0</length></file></contents></directory></ltfsindex>`,
			want:  "invalid URL escape",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ParseLTFSIndex(context.Background(), strings.NewReader(test.index), func(*LTFSIndexEntry) error {
				return nil
			})
			require.ErrorContains(t, err, test.want)
		})
	}
}
