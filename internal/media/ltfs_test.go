package media

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
