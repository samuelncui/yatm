package library

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

type fileReadSQL struct {
	logger.Interface
	queries []string
}

func (l *fileReadSQL) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	l.queries = append(l.queries, query)
}

func TestFileReadProjectionCosts(t *testing.T) {
	// Large pages contain originals but must read only the persisted File rows.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	for index := 0; index < 60; index++ {
		file := &File{Name: fmt.Sprintf("file-%03d", index)}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name, Size: int64(index), Signature: []byte("duplicates")}).Error)
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	page, err := lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, false, "", "", 60)
	require.NoError(t, err)
	require.Len(t, page.Files, 60)
	require.Len(t, trace.queries, 1)
	for _, excluded := range []string{"file_locations", "file_versions", "positions", "file_tracking_keys"} {
		require.NotContains(t, trace.queries[0], excluded)
	}

	// Attributes load two batched projections, never copy statistics or tracking evidence.
	ids := make([]int64, 0, len(page.Files))
	for _, file := range page.Files {
		ids = append(ids, file.ID)
	}
	trace.queries = nil
	facts, err := lib.ReadFileFacts(ctx, ids, true, false)
	require.NoError(t, err)
	require.Len(t, trace.queries, 2)
	require.Equal(t, int64(59), *facts[ids[59]].Size)
	require.NotContains(t, strings.Join(trace.queries, "\n"), "positions")
	require.NotContains(t, strings.Join(trace.queries, "\n"), "file_tracking_keys")

	// Status is bounded by batch count and uses EXISTS rather than hidden exact counts.
	trace.queries = nil
	_, err = lib.ReadFileFacts(ctx, ids, false, true)
	require.NoError(t, err)
	require.Len(t, trace.queries, 3)
	require.Contains(t, strings.Join(trace.queries, "\n"), "EXISTS")
	require.NotContains(t, strings.ToUpper(strings.Join(trace.queries, "\n")), "COUNT(")

	// Duplicate candidates use the same persisted row shape without loading presentation facts.
	trace.queries = nil
	duplicates, _, err := lib.ListContentDuplicates(ctx, []byte("duplicates"), 0, 60)
	require.NoError(t, err)
	require.Len(t, duplicates, 60)
	require.Len(t, trace.queries, 1)
}

func TestFilePresentationIsLoadedExplicitly(t *testing.T) {
	// A direct row read contains only persisted identity and organization.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	file := &File{Name: "explicit.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name,
		Mode: 0o640, MtimeNS: 123, Size: 7, Hash: []byte("hash"), Signature: []byte("signature")}).Error)
	var row fileRow
	require.NoError(t, db.First(&row, file.ID).Error)
	stored := row.file()
	require.Zero(t, stored.Mode)
	require.Zero(t, stored.Size)
	require.Nil(t, stored.Signature)

	// Library reads attach the compatibility projection through an explicit operation.
	loaded, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, uint32(0o640), loaded.Mode)
	require.Equal(t, int64(7), loaded.Size)
	require.Equal(t, []byte("signature"), loaded.Signature)
	page, err := lib.SearchFiles(ctx, "name:explicit.txt", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, []byte("signature"), page.Results[0].File.Signature)
}

func TestFileReadFactsRestoreEligibilityAndHistory(t *testing.T) {
	// Current unknown content must remain distinct from an older recoverable version.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	file := &File{Name: "changed"}
	createFileRows(t, db, file)
	require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name, Size: 9}).Error)
	hash := sha256.Sum256([]byte("old"))
	signature := []byte("opaque-content")
	media := &Media{Name: "Disk", Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()}
	require.NoError(t, db.Create(media).Error)
	version := &FileVersion{FileID: file.ID, Signature: signature, Hash: hash[:], Size: 3}
	require.NoError(t, db.Create(version).Error)
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: media.ID, Path: "saved", Signature: signature, Hash: hash[:], Size: 3}))
	facts, err := lib.ReadFileFacts(ctx, []int64{file.ID}, false, true)
	require.NoError(t, err)
	require.True(t, facts[file.ID].HasArchive)
	require.False(t, facts[file.ID].CurrentArchive)
	require.False(t, facts[file.ID].LatestUnavailable)

	// A bad copy is retained as history but cannot count as a default Restore candidate.
	require.NoError(t, db.Model(&Position{}).Where("path = ?", "saved").Update("health", entity.PositionHealth_POSITION_HEALTH_DAMAGED).Error)
	facts, err = lib.ReadFileFacts(ctx, []int64{file.ID}, false, true)
	require.NoError(t, err)
	require.False(t, facts[file.ID].HasArchive)
	require.True(t, facts[file.ID].HasBadCopies)
	require.True(t, facts[file.ID].LatestUnavailable)
}

func TestFileReadRecursiveCursorAndNavigation(t *testing.T) {
	// Equal names at different depths require a tie-safe cursor tied to the scope and root.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	left := &File{Name: "left", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	right := &File{Name: "right", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, left, right)
	for _, parent := range []*File{left, right} {
		createFileRows(t, db, &File{Name: "same", ParentID: parent.ID})
	}
	first, err := lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "name:same", "", 1)
	require.NoError(t, err)
	require.Len(t, first.Files, 1)
	second, err := lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "name:same", first.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Files, 1)
	require.NotEqual(t, first.Files[0].ID, second.Files[0].ID)
	require.Empty(t, second.NextCursor)
	_, err = lib.ListFileRows(ctx, left.ID, entity.FileScope_FILE_SCOPE_ALL, true, "name:same", first.NextCursor, 1)
	require.Error(t, err)

	// Navigation reads metadata only and does not load content projections for the leaf or ancestors.
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	parents, err := lib.ReadFileAncestors(ctx, first.Files[0].ID)
	require.NoError(t, err)
	require.Len(t, parents, 2)
	queries := strings.Join(trace.queries, "\n")
	require.NotContains(t, queries, "file_locations")
	require.NotContains(t, queries, "file_versions")
}
