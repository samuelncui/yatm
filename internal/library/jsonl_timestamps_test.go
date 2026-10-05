package library

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLibraryNanosecondJSONLRejectsIncompatibleShapes(t *testing.T) {
	// Keep a complete valid snapshot so only the selected timestamp boundary becomes invalid.
	_, snapshot := timestampSnapshot(t, 1_700_000_000_123_456_789)
	for _, field := range []struct{ record, current, old string }{
		{recordTypeFile, "created_at_ns", "created_at_ms"},
		{recordTypeFile, "updated_at_ns", "updated_at_ms"},
		{recordTypeMedia, "created_at_ns", "create_time"},
		{recordTypeMedia, "destroyed_at_ns", "destroy_time"},
		{recordTypePosition, "mtime_ns", "mod_time"},
		{recordTypePosition, "written_at_ns", "write_time"},
		{recordTypePosition, "checked_at_ns", "checked_at"},
		{recordTypeFileVersion, "mtime_ns", "mtime_ns"},
		{recordTypeFileVersion, "first_archived_at_ns", "first_archived_at_ms"},
		{recordTypeFileVersion, "last_archived_at_ns", "last_archived_at_ms"},
		{recordTypeFileVersionArchive, "archived_at_ns", "archived_at_ms"},
		{recordTypeLocation, "created_at_ns", "created_at_ms"},
		{recordTypeLocation, "updated_at_ns", "updated_at_ms"},
		{recordTypeLocation, "last_sync_at_ns", "last_sync_at_ms"},
		{recordTypeFileLocation, "mtime_ns", "mtime_ns"},
		{recordTypeTracking, "observed_at_ns", "observed_at_ms"},
	} {
		for _, mutation := range []string{"old_key", "number", "overflow", "missing"} {
			optional := field.current == "destroyed_at_ns" || field.current == "first_archived_at_ns" || field.current == "last_archived_at_ns"
			if (mutation == "missing" && optional) || (mutation == "old_key" && field.old == field.current) {
				continue
			}
			t.Run(field.record+"/"+field.current+"/"+mutation, func(t *testing.T) {
				// Place the incompatible record after valid records to exercise transaction rollback.
				var broken bytes.Buffer
				for _, line := range bytes.Split(bytes.TrimSpace(snapshot), []byte("\n")) {
					var record jsonlInputRecord
					require.NoError(t, json.Unmarshal(line, &record))
					if record.Type == field.record {
						var fields map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(record.Data, &fields))
						switch mutation {
						case "old_key":
							fields[field.old] = fields[field.current]
							delete(fields, field.current)
						case "number":
							fields[field.current] = json.RawMessage(`1700000000123456789`)
						case "overflow":
							fields[field.current] = json.RawMessage(`"9223372036854775808"`)
						case "missing":
							delete(fields, field.current)
						}
						var err error
						record.Data, err = json.Marshal(fields)
						require.NoError(t, err)
					}
					require.NoError(t, json.NewEncoder(&broken).Encode(record))
				}
				db, target := newTestLibrary(t)
				createFileRows(t, db, &File{ID: 99, Name: "kept"})
				require.Error(t, target.Import(context.Background(), &broken, false))
				var kept []fileRow
				require.NoError(t, db.Find(&kept).Error)
				require.Len(t, kept, 1)
				require.EqualValues(t, 99, kept[0].ID)
				var mediaCount int64
				require.NoError(t, db.Model(&Media{}).Count(&mediaCount).Error)
				require.Zero(t, mediaCount)
			})
		}
	}

	// Tracking birth time used the same key before this format, so its old numeric shape must fail.
	_, target := newTestLibrary(t)
	broken := strings.Replace(string(snapshot), `"birth_ns":"1700000000123456789"`, `"birth_ns":1700000000123456789`, 1)
	require.NotEqual(t, string(snapshot), broken)
	require.Error(t, target.Import(context.Background(), strings.NewReader(broken), false))
}

func legacyTimestampSnapshot(t *testing.T, stamp time.Time, change func(*legacyLibraryFile, *legacyLibraryTape, *legacyLibraryPosition)) []byte {
	t.Helper()
	file := &legacyLibraryFile{ID: 7, Name: "content", Mode: 0644, ModTime: stamp, Signature: []byte("saved")}
	tape := &legacyLibraryTape{ID: 3, Barcode: "NS0001", CreateTime: stamp, DestroyTime: &stamp}
	position := &legacyLibraryPosition{ID: 11, FileID: file.ID, TapeID: tape.ID, Path: file.Name, Mode: 0644,
		ModTime: stamp, WriteTime: stamp}
	if change != nil {
		change(file, tape, position)
	}
	encoded, err := json.Marshal(map[string]any{"files": []*legacyLibraryFile{file},
		"tapes": []*legacyLibraryTape{tape}, "positions": []*legacyLibraryPosition{position}})
	require.NoError(t, err)
	return encoded
}

func TestLibraryNanosecondLegacyImport(t *testing.T) {
	for _, source := range []time.Time{time.Time{}, time.Unix(0, -1).UTC(), time.Unix(0, math.MinInt64).UTC(),
		time.Unix(0, math.MaxInt64).UTC(), time.Unix(1_700_000_000, 123_456_789).In(time.FixedZone("historical", 3660))} {
		t.Run(source.Format(time.RFC3339Nano), func(t *testing.T) {
			// v0.1.x text timestamps remain supported and cross a checked conversion at import.
			db, lib := newTestLibrary(t)
			require.NoError(t, lib.Import(context.Background(), bytes.NewReader(legacyTimestampSnapshot(t, source, nil)), false))
			want := source.UnixNano()
			if source.IsZero() {
				want = 0
			}
			var file fileRow
			require.NoError(t, db.First(&file, 7).Error)
			require.Equal(t, want, file.CreatedAtNS)
			require.Equal(t, want, file.UpdatedAtNS)
			media, err := lib.GetMedia(context.Background(), 3)
			require.NoError(t, err)
			require.Equal(t, want, media.CreatedAtNS)
			require.Equal(t, &want, media.DestroyedAtNS)
			position, err := lib.GetPosition(context.Background(), 11)
			require.NoError(t, err)
			require.Equal(t, want, position.MtimeNS)
			require.Equal(t, want, position.WrittenAtNS)
			version, err := lib.LatestFileVersion(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, want, version.MtimeNS)
			require.Nil(t, version.FirstArchivedAtNS)
			require.Nil(t, version.LastArchivedAtNS)
		})
	}
}

func TestLibraryNanosecondLegacyRejectsOutOfRange(t *testing.T) {
	for _, year := range []int{1600, 3000} {
		bad := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		for name, change := range map[string]func(*legacyLibraryFile, *legacyLibraryTape, *legacyLibraryPosition){
			"file":      func(file *legacyLibraryFile, _ *legacyLibraryTape, _ *legacyLibraryPosition) { file.ModTime = bad },
			"created":   func(_ *legacyLibraryFile, tape *legacyLibraryTape, _ *legacyLibraryPosition) { tape.CreateTime = bad },
			"destroyed": func(_ *legacyLibraryFile, tape *legacyLibraryTape, _ *legacyLibraryPosition) { tape.DestroyTime = &bad },
			"mtime": func(_ *legacyLibraryFile, _ *legacyLibraryTape, position *legacyLibraryPosition) {
				position.ModTime = bad
			},
			"written": func(_ *legacyLibraryFile, _ *legacyLibraryTape, position *legacyLibraryPosition) {
				position.WriteTime = bad
			},
		} {
			t.Run(bad.Format("2006")+"/"+name, func(t *testing.T) {
				// Invalid legacy instants cannot wrap into plausible Catalog values or replace existing files.
				db, lib := newTestLibrary(t)
				createFileRows(t, db, &File{ID: 99, Name: "kept"})
				snapshot := legacyTimestampSnapshot(t, time.Unix(1, 123), change)
				require.ErrorContains(t, lib.Import(context.Background(), bytes.NewReader(snapshot), false), "outside the signed Unix nanosecond range")
				var files []fileRow
				require.NoError(t, db.Find(&files).Error)
				require.Len(t, files, 1)
				require.EqualValues(t, 99, files[0].ID)
			})
		}
	}
}

func TestLibraryNanosecondQueryRejectsOutOfRange(t *testing.T) {
	_, lib := newTestLibrary(t)
	for _, query := range []string{`mtime:"3000-01-01T00:00:00Z"`, `mtime: >= "1600-01-01T00:00:00Z"`} {
		_, err := lib.CompileFilesQuery(query)
		require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
	}
	query, err := lib.CompileFilesQuery(`mtime:"1969-12-31T23:59:59.999999999Z"`)
	require.NoError(t, err)
	matched, err := lib.MatchLiveQuery(context.Background(), query, []LiveQueryRow{{Name: "old", MtimeNS: -1}})
	require.NoError(t, err)
	require.Equal(t, []int{0}, matched)
	_, err = lib.ListFileQueryRows(context.Background(), 0, entity.FileScope_FILE_SCOPE_ALL, false, query, "", 1)
	require.NoError(t, err)
}
