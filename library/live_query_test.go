package library

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLiveQueriesShareCatalogGrammarWithoutAdmission(t *testing.T) {
	// Live names and sizes supersede logical presentation; only annotations join by a real File ID.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	file := &File{Name: "logical-name", Note: "holiday"}
	require.NoError(t, db.Create(file).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{file.ID}, FileMetadataEdit{AddTags: []string{"travel"}}))
	rows := []LiveQueryRow{
		{FileID: file.ID, LocationID: 7, Name: "photo.jpg", Note: file.Note, Size: 22, Signature: []byte("opaque"), HasArchive: true},
		{LocationID: 7, Name: "other.txt", Size: 3},
		{LocationID: 7, Name: "folder", Kind: entity.EntryKind_ENTRY_DIRECTORY},
	}
	for _, test := range []struct {
		query string
		want  []int
	}{
		{"", []int{0, 1, 2}},
		{"name:photo* AND tag:travel", []int{0}},
		{"note:holiday OR name:other", []int{0, 1}},
		{"has:unknown AND type:file", []int{1}},
		{"has:archive AND location:7", []int{0}},
		{"NOT has:archive AND type:file", []int{1}},
		{"size:[10 TO 30]", []int{0}},
		{"name:logical-name", []int{}},
		{"location:9", []int{}},
	} {
		t.Run(test.query, func(t *testing.T) {
			matched, err := lib.MatchLiveFiles(ctx, test.query, rows)
			require.NoError(t, err)
			require.Equal(t, test.want, matched)
		})
	}
	require.Error(t, lib.ValidateFilesQuery("unsupported:value"))
	require.Error(t, lib.ValidateFilesQuery("has:invalid"))
	var count int64
	require.NoError(t, db.Model(ModelFile).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestLiveQueryAcrossProjectionBatches(t *testing.T) {
	_, lib := newTestLibrary(t)
	rows := make([]LiveQueryRow, 140)
	for index := range rows {
		rows[index] = LiveQueryRow{Name: fmt.Sprintf("item-%d", index), Size: int64(index), MtimeNS: time.Now().UnixNano()}
	}
	matched, err := lib.MatchLiveFiles(context.Background(), "size:[128 TO 130]", rows)
	require.NoError(t, err)
	require.Equal(t, []int{128, 129, 130}, matched)
}
