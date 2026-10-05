package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func timestampSnapshot(t *testing.T, stamp int64) (*Library, []byte) {
	t.Helper()
	// Seed every persisted instant with the same exact value, including explicit zero clocks.
	db, lib := newTestLibrary(t)
	insert := db.Session(&gorm.Session{NowFunc: func() time.Time { return time.Unix(0, stamp) }})
	file := &File{ID: 7, Name: "content", CreatedAtNS: stamp, UpdatedAtNS: stamp}
	require.NoError(t, createFileRow(insert, file))
	location := &Location{ID: 5, Name: "source", ExecutorID: "local", RootPath: t.TempDir(),
		Config:      &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore"}},
		CreatedAtNS: stamp, UpdatedAtNS: stamp, LastSyncAtNS: stamp}
	require.NoError(t, insert.Create(location).Error)
	media := testTapeMedia("NS0001", TapeFormatLTFSV0)
	media.ID, media.CreatedAtNS, media.DestroyedAtNS = 3, stamp, &stamp
	require.NoError(t, insert.Create(media).Error)
	require.NoError(t, insert.Create(&Position{ID: 11, MediaID: media.ID, Path: file.Name,
		MtimeNS: stamp, WrittenAtNS: stamp, CheckedAtNS: stamp}).Error)
	require.NoError(t, insert.Create(&FileLocation{FileID: file.ID, LocationID: location.ID,
		Path: file.Name, Mode: 0644, MtimeNS: stamp}).Error)
	require.NoError(t, insert.Create(&FileTrackingKey{FileID: file.ID, LocationID: location.ID,
		Kind: TrackingNative, Scope: "local:1", KeyValue: []byte("1"), ObservedAtNS: stamp,
		Details: TrackingDetails{BirthNS: stamp}}).Error)
	version := &FileVersion{ID: 13, FileID: file.ID, Signature: []byte("saved"), MtimeNS: stamp}
	if stamp != 0 {
		version.FirstArchivedAtNS, version.LastArchivedAtNS = &stamp, &stamp
	}
	require.NoError(t, insert.Create(version).Error)
	if stamp != 0 {
		require.NoError(t, insert.Create(&FileVersionArchive{VersionID: version.ID, ArchivedAtNS: stamp}).Error)
	}

	// Export through the public snapshot owner, including the complete Location group.
	var snapshot bytes.Buffer
	require.NoError(t, lib.Export(context.Background(), &snapshot, []entity.LibraryEntityType{
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA,
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION,
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION,
	}))
	return lib, snapshot.Bytes()
}

func assertTimestampRows(t *testing.T, lib *Library, stamp int64) {
	t.Helper()
	// SQLite must retain integers, not driver-specific datetime text or rounded floating point.
	for table, columns := range map[string][]string{
		"files":              {"created_at_ns", "updated_at_ns"},
		"locations":          {"created_at_ns", "updated_at_ns", "last_sync_at_ns"},
		"media":              {"created_at_ns", "destroyed_at_ns"},
		"positions":          {"mtime_ns", "written_at_ns", "checked_at_ns"},
		"file_locations":     {"mtime_ns"},
		"file_tracking_keys": {"observed_at_ns"},
		"file_versions":      {"mtime_ns", "first_archived_at_ns", "last_archived_at_ns"},
	} {
		for _, column := range columns {
			var value struct {
				Stamp *int64
				Kind  string
			}
			require.NoError(t, lib.db.Table(table).Select(column+" AS stamp, typeof("+column+") AS kind").Scan(&value).Error)
			if stamp == 0 && (column == "first_archived_at_ns" || column == "last_archived_at_ns") {
				require.Nil(t, value.Stamp, table+"."+column)
				require.Equal(t, "null", value.Kind)
				continue
			}
			require.Equal(t, "integer", value.Kind, table+"."+column)
			require.Equal(t, &stamp, value.Stamp, table+"."+column)
		}
	}
	var archives []FileVersionArchive
	require.NoError(t, lib.db.Find(&archives).Error)
	if stamp == 0 {
		require.Empty(t, archives)
	} else {
		require.Equal(t, []FileVersionArchive{{VersionID: 13, ArchivedAtNS: stamp}}, archives)
		var kind string
		require.NoError(t, lib.db.Table("file_version_archives").Select("typeof(archived_at_ns)").Scan(&kind).Error)
		require.Equal(t, "integer", kind)
	}

	// Embedded native birth time is JSON text; stats retain optional unknown write time.
	var tracking FileTrackingKey
	require.NoError(t, lib.db.First(&tracking).Error)
	require.Equal(t, stamp, tracking.Details.BirthNS)
	stats, err := lib.GetMediaStats(context.Background(), 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.FileCount)
	if stamp == 0 {
		require.Nil(t, stats.LastWrittenAtNS)
	} else {
		require.Equal(t, &stamp, stats.LastWrittenAtNS)
	}
	encoded, err := json.Marshal(stats)
	require.NoError(t, err)
	var restored MediaStats
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.Equal(t, *stats, restored)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	if stamp == 0 {
		require.NotContains(t, fields, "last_written_at_ns")
	} else {
		require.Equal(t, strconv.Quote(strconv.FormatInt(stamp, 10)), string(fields["last_written_at_ns"]))
	}
}

func TestLibraryNanosecondRoundTrip(t *testing.T) {
	for _, stamp := range []int64{0, -1, math.MinInt64, math.MaxInt64, 1_700_000_000_123_456_789} {
		t.Run(strconv.FormatInt(stamp, 10), func(t *testing.T) {
			// Both the writer and imported Catalog must preserve every low digit and signed endpoint.
			source, snapshot := timestampSnapshot(t, stamp)
			assertTimestampRows(t, source, stamp)
			_, target := newTestLibrary(t)
			require.NoError(t, target.Import(context.Background(), bytes.NewReader(snapshot), false))
			assertTimestampRows(t, target, stamp)

			// Every exported timestamp is a decimal string; nil optional dates are absent.
			for _, line := range bytes.Split(bytes.TrimSpace(snapshot), []byte("\n")) {
				var record jsonlInputRecord
				require.NoError(t, json.Unmarshal(line, &record))
				var fields map[string]json.RawMessage
				if len(record.Data) == 0 {
					continue
				}
				require.NoError(t, json.Unmarshal(record.Data, &fields))
				if record.Type == recordTypeTracking {
					var details map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(fields["details"], &details))
					for name, value := range details {
						fields[name] = value
					}
					if stamp == 0 {
						require.NotContains(t, fields, "birth_ns")
					}
				}
				for name, value := range fields {
					if len(name) > 3 && name[len(name)-3:] == "_ns" {
						require.Equal(t, strconv.Quote(strconv.FormatInt(stamp, 10)), string(value), record.Type+"."+name)
					}
				}
				if record.Type == recordTypeFileVersion && stamp == 0 {
					require.NotContains(t, fields, "first_archived_at_ns")
					require.NotContains(t, fields, "last_archived_at_ns")
				}
			}
		})
	}
}

func TestLibraryNanosecondAutoTimestamps(t *testing.T) {
	// GORM must use ns for automatic creation and update, while runtime File.ModTime stays time.Time.
	db, _ := newTestLibrary(t)
	now := time.Unix(1_700_000_000, 123_456_789)
	lib := New(db.Session(&gorm.Session{NowFunc: func() time.Time { return now }}))
	ctx := context.Background()
	file := &File{Name: "directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, file))
	location := locationTestSource(t, lib)
	created := now.UnixNano()
	require.Equal(t, created, file.CreatedAtNS)
	require.Equal(t, created, location.CreatedAtNS)
	now = now.Add(time.Nanosecond)
	file.Note = "changed"
	require.NoError(t, lib.SaveFile(ctx, file))
	location.Name = "renamed"
	_, err := lib.UpdateLocation(ctx, location)
	require.NoError(t, err)
	require.Equal(t, created, file.CreatedAtNS)
	require.Equal(t, now.UnixNano(), file.UpdatedAtNS)
	require.Equal(t, now.UnixNano(), location.UpdatedAtNS)
	projected, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, now.UnixNano(), projected.ModTime.UnixNano())
	require.Equal(t, now.UnixNano(), FileRowTime(projected))

	// Directory search compares the stored instant directly without a millisecond multiplier.
	page, err := lib.SearchFiles(ctx, `mtime:"`+now.Format(time.RFC3339Nano)+`"`, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, file.ID, page.Results[0].File.ID)
}

func TestLibraryNanosecondArchiveCutoffAndMerge(t *testing.T) {
	// Distinct saves within one millisecond keep their order, including reused content and ties.
	db, lib := newTestLibrary(t)
	file, other := &File{Name: "history"}, &File{Name: "other"}
	createFileRows(t, db, file, other)
	base := int64(1_700_000_000_123_000_000)
	a := saveVersionAt(t, db, file.ID, "A", base+1)
	b := saveVersionAt(t, db, file.ID, "B", base+2)
	saveVersionAt(t, db, file.ID, "A", base+3)
	for _, test := range []struct{ cutoff, wantID, wantTime int64 }{
		{base + 1, a.ID, base + 1}, {base + 2, b.ID, base + 2}, {base + 3, a.ID, base + 3},
	} {
		result, err := lib.ResolveRestoreVersion(context.Background(), file.ID, &entity.RestoreVersionPolicy{BeforeAtNs: &test.cutoff})
		require.NoError(t, err)
		require.Equal(t, test.wantID, result.Version.Id)
		require.Equal(t, test.wantTime, result.GetArchivedAtNs())
	}
	latest, err := lib.LatestFileVersion(context.Background(), file.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, latest.ID)

	// Negative timestamps are real instants; cursor initialization must not drop them during a merge.
	old := saveVersionAt(t, db, other.ID, "A", math.MinInt64)
	saveVersionAt(t, db, other.ID, "A", -1)
	saveVersionAt(t, db, other.ID, "B", 1)
	for _, cutoff := range []int64{math.MinInt64, -1, 0} {
		policy := &entity.RestoreVersionPolicy{BeforeAtNs: &cutoff}
		require.NoError(t, ValidateRestoreVersionSelection(policy, true))
		result, err := lib.ResolveRestoreVersion(context.Background(), other.ID, policy)
		require.NoError(t, err)
		require.Equal(t, min(cutoff, -1), result.GetArchivedAtNs())
		var selected []*RestoreSelectionItem
		require.NoError(t, lib.WalkRestoreSelections(context.Background(), []*entity.FileSelection{restoreSelection(other.ID)}, nil,
			policy, func(item *RestoreSelectionItem) error { selected = append(selected, item); return nil }))
		require.Len(t, selected, 1)
		require.Equal(t, old.ID, selected[0].Version.ID)
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeVersion(tx, old, a) }))
	var dates []int64
	require.NoError(t, db.Model(&FileVersionArchive{}).Where("version_id = ?", a.ID).Order("archived_at_ns").Pluck("archived_at_ns", &dates).Error)
	require.Equal(t, []int64{math.MinInt64, -1, base + 1, base + 3}, dates)
	require.ErrorContains(t, db.Create(&FileVersionArchive{VersionID: a.ID, ArchivedAtNS: 0}).Error, "nonzero save time")
}

func TestLibraryNanosecondArchiveEndpoints(t *testing.T) {
	// Imported history can prove a pre-epoch save from either endpoint without individual observations.
	for _, field := range []string{"first", "last"} {
		t.Run(field, func(t *testing.T) {
			db, lib := newTestLibrary(t)
			file := &File{Name: "imported"}
			createFileRows(t, db, file)
			stamp, cutoff := int64(-1), int64(0)
			version := &FileVersion{FileID: file.ID, Signature: []byte("saved")}
			if field == "first" {
				version.FirstArchivedAtNS = &stamp
			} else {
				version.LastArchivedAtNS = &stamp
			}
			require.NoError(t, db.Create(version).Error)
			result, err := lib.ResolveRestoreVersion(context.Background(), file.ID, &entity.RestoreVersionPolicy{BeforeAtNs: &cutoff})
			require.NoError(t, err)
			require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_MATCH_MATCHED, result.Match)
			require.NotNil(t, result.Version)
			require.Equal(t, version.ID, result.Version.Id)
			require.Equal(t, stamp, result.GetArchivedAtNs())
		})
	}
}

func TestLibraryNanosecondHealthAndUnknownTimes(t *testing.T) {
	// A checked pre-epoch instant is present; zero is the established absent-check sentinel.
	db, lib := newTestLibrary(t)
	media, err := lib.CommitMedia(context.Background(), testTapeMedia("NS0001", TapeFormatLTFSV0), mediaFileSource(
		&MediaFile{Path: "directory/known", ModTime: time.Unix(0, -1), WriteTime: time.Unix(0, -1),
			Hash: bytes.Repeat([]byte{1}, 32), CheckedAtNS: -1},
		&MediaFile{Path: "directory/unknown"},
	))
	require.NoError(t, err)
	var position Position
	require.NoError(t, db.Where("path = ?", "directory/known").First(&position).Error)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, position.Health)
	require.EqualValues(t, -1, position.CheckedAtNS)
	observation := &PositionHealthObservation{PositionID: position.ID, Health: entity.PositionHealth_POSITION_HEALTH_DAMAGED,
		CheckedAtNS: math.MinInt64}
	require.NoError(t, lib.PublishPositionHealth(context.Background(), observation))
	observation.CheckedAtNS = 0
	require.ErrorContains(t, lib.PublishPositionHealth(context.Background(), observation), "check time is missing")
	stored, err := lib.GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.EqualValues(t, math.MinInt64, stored.CheckedAtNS)

	// Unknown zero siblings do not replace known negative times in aggregates or derived directories.
	stats, err := lib.GetMediaStats(context.Background(), media.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.FileCount)
	require.EqualValues(t, -1, *stats.LastWrittenAtNS)
	var directory Position
	require.NoError(t, db.Where("path = ?", "directory/").First(&directory).Error)
	require.EqualValues(t, -1, directory.MtimeNS)
	require.EqualValues(t, -1, directory.WrittenAtNS)
}

func TestLibraryNanosecondMediaOrderAndOptionalJSON(t *testing.T) {
	// Media time ordering preserves sub-millisecond digits and keeps unknown zero after known dates.
	db, lib := newTestLibrary(t)
	for index, stamp := range []int64{0, -1, 1_700_000_000_123_000_001, 1_700_000_000_123_000_002} {
		media := testTapeMedia(fmt.Sprintf("NS%04d", index), TapeFormatLTFSV0)
		media.CreatedAtNS = stamp
		require.NoError(t, db.Create(media).Error)
	}
	rows, more, err := lib.ListMediaPage(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, more)
	var ids []int64
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	require.Equal(t, []int64{4, 3, 2, 1}, ids)

	// Unknown optional instants are absent, distinct from an explicitly stored pointer to zero.
	for _, test := range []struct {
		value any
		key   string
	}{
		{rows[0], "destroyed_at_ns"}, {&MediaStats{}, "last_written_at_ns"},
	} {
		encoded, err := json.Marshal(test.value)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &fields))
		require.NotContains(t, fields, test.key)
	}
}

func TestLibraryNanosecondMediaTimeBoundary(t *testing.T) {
	for _, source := range []time.Time{time.Time{}, time.Unix(0, 0), time.Unix(-1, 123),
		time.Unix(0, math.MinInt64), time.Unix(0, math.MaxInt64)} {
		t.Run(source.Format(time.RFC3339Nano), func(t *testing.T) {
			// Runtime descriptors cross the checked boundary exactly, including the zero sentinel.
			_, lib := newTestLibrary(t)
			media, err := lib.CommitMedia(context.Background(), testTapeMedia("NS0001", TapeFormatLTFSV0),
				mediaFileSource(&MediaFile{Path: "directory/file", ModTime: source, WriteTime: source}))
			require.NoError(t, err)
			want := source.UnixNano()
			if source.IsZero() {
				want = 0
			}
			var positions []Position
			require.NoError(t, lib.db.Where("media_id = ?", media.ID).Find(&positions).Error)
			require.Len(t, positions, 2)
			for _, position := range positions {
				require.Equal(t, want, position.MtimeNS)
				require.Equal(t, want, position.WrittenAtNS)
			}
		})
	}
	for _, field := range []string{"mtime", "written"} {
		t.Run("out_of_range_"+field, func(t *testing.T) {
			// Reject an invalid runtime source and roll back the whole inventory publication.
			db, lib := newTestLibrary(t)
			input := &MediaFile{Path: "file"}
			bad := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
			if field == "mtime" {
				input.ModTime = bad
			} else {
				input.WriteTime = bad
			}
			_, err := lib.CommitMedia(context.Background(), testTapeMedia("NS0001", TapeFormatLTFSV0), mediaFileSource(input))
			require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
			for _, model := range []any{&Media{}, &Position{}, &FileVersion{}} {
				var count int64
				require.NoError(t, db.Model(model).Count(&count).Error)
				require.Zero(t, count, fmt.Sprintf("%T", model))
			}
		})
	}
}
