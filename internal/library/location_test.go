package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func locationTestSource(t *testing.T, l *Library) *Location {
	t.Helper()
	s := &Location{Name: "Everyday", ExecutorID: "local", RootPath: filepath.Join(t.TempDir(), "source")}
	require.NoError(t, l.CreateLocation(context.Background(), s))
	return s
}

func TestLocationConfigStoredAsJSON(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	source := locationTestSource(t, lib)
	source.Config.UseMmap = true
	source.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "private/\n"}
	_, err := lib.UpdateLocation(ctx, source)
	require.NoError(t, err)

	var stored string
	require.NoError(t, db.Raw("SELECT config FROM locations WHERE id = ?", source.ID).Scan(&stored).Error)
	require.JSONEq(t, `{"ignore":{"format":"gitignore","text":"private/\n"},"use_mmap":true}`, stored)
	reloaded, err := lib.GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.True(t, reloaded.Config.GetUseMmap())
	require.Equal(t, "private/\n", reloaded.Config.GetIgnore().GetText())
	require.False(t, db.Migrator().HasColumn(&Location{}, "write_tracking_uuid"))
	require.False(t, db.Migrator().HasColumn(&Location{}, "exclusions"))
}

func TestLocationConfigIgnoresRemovedUUIDOption(t *testing.T) {
	// A retired preference alone is harmless; it cannot reactivate file-attribute writes.
	db, lib := newTestLibrary(t)
	source := locationTestSource(t, lib)
	require.NoError(t, db.Model(&Location{}).Where("id = ?", source.ID).
		UpdateColumn("config", `{"write_tracking_uuid":true,"use_mmap":true}`).Error)
	reloaded, err := lib.GetLocation(context.Background(), source.ID)
	require.NoError(t, err)
	require.True(t, reloaded.Config.UseMmap)
}

func observedTestEntry(name, content string) *ObservedEntry {
	hash := sha256.Sum256([]byte(content))
	return &ObservedEntry{Path: name, Size: int64(len(content)), Mode: 0644, MtimeNS: 1000000001, Hash: hash[:]}
}

// publishComplete publishes a complete observation of one Location: the manifest states what is
// there, and every recorded original it no longer contains is retired, the way a Scan publishes its
// observed rows together with the REMOVED ones as the absent manifest.
func publishComplete(t *testing.T, l *Library, locationID, jobID int64, rows ...*ObservedEntry) *Location {
	t.Helper()
	ctx := context.Background()
	present := make(map[int64]bool, len(rows))
	for _, row := range rows {
		present[row.FileID] = true
	}
	previous, err := l.LocationOriginalsPage(ctx, locationID, "", 1000)
	require.NoError(t, err)
	location, err := l.PublishAnalyzed(ctx, locationID, jobID, observedTestManifest(rows...),
		func(_ context.Context, yield func(*ObservedEntry) error) error {
			for _, row := range previous {
				if present[row.FileID] {
					continue
				}
				if err := yield(&ObservedEntry{FileID: row.FileID, Path: row.Path}); err != nil {
					return err
				}
			}
			return nil
		})
	require.NoError(t, err)
	return location
}

func observedTestManifest(rows ...*ObservedEntry) ObservationManifest {
	return func(_ context.Context, yield func(*ObservedEntry) error) error {
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestLocationPublicationPreservesIdentityAndTrimProtection(t *testing.T) {
	// Publish duplicate content as independent Library identities.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := locationTestSource(t, l)
	s = publishComplete(t, l, s.ID, 7, observedTestEntry("a.txt", "same"), observedTestEntry("dir/b.txt", "same"))
	rows, err := l.LocationOriginalsPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].FileID, rows[1].FileID)
	file, err := l.GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	file.Name, file.Note = "organized.txt", "keep this note"
	require.NoError(t, l.SaveFile(ctx, file))

	// A physical rename keeps all logical facts.
	renamed := observedTestEntry("renamed.txt", "same")
	renamed.FileID = file.ID // The Scan matcher supplies the chosen identity before publication.
	s = publishComplete(t, l, s.ID, 8, renamed)
	_, err = l.Trim(ctx, true, true, false)
	require.NoError(t, err)
	kept, err := l.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "organized.txt", kept.Name)
	require.Equal(t, "keep this note", kept.Note)

	// A matched path overwrite changes current facts, not the organized File.
	changed := observedTestEntry("renamed.txt", "new")
	changed.FileID = file.ID
	s = publishComplete(t, l, s.ID, 9, changed)
	rows, err = l.LocationOriginalsPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Equal(t, file.ID, rows[0].FileID)
	created, err := l.GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	require.Equal(t, "keep this note", created.Note)
	_, err = l.GetFile(ctx, file.ID)
	require.NoError(t, err)

	// Source removal only removes positions; explicit Trim is the separate metadata cleanup.
	_, err = l.DeleteLocation(ctx, s.ID, false)
	require.NoError(t, err)
	_, err = l.GetFile(ctx, created.ID)
	require.NoError(t, err)
	_, err = l.Trim(ctx, true, true, false)
	require.NoError(t, err)
	_, err = l.GetFile(ctx, created.ID)
	require.ErrorIs(t, err, ErrFileNotFound)
}

func TestLocationPublicationRollsBackAcrossBatches(t *testing.T) {
	// Establish an authoritative old index before attempting a large incomplete replacement.
	ctx := context.Background()
	db, l := newTestLibrary(t)
	s := locationTestSource(t, l)
	s, err := l.PublishAnalyzed(ctx, s.ID, 1, observedTestManifest(observedTestEntry("kept", "old")), nil)
	require.NoError(t, err)
	var before int64
	require.NoError(t, db.Model(ModelFile).Count(&before).Error)
	failure := errors.New("incomplete manifest")

	// Failure after multiple flushed batches rolls back new Files, folders, positions, and source facts.
	_, err = l.PublishAnalyzed(ctx, s.ID, 2, func(_ context.Context, yield func(*ObservedEntry) error) error {
		for i := 0; i < 250; i++ {
			if err := yield(observedTestEntry(fmt.Sprintf("deep/%04d", i), fmt.Sprint(i))); err != nil {
				return err
			}
		}
		return failure
	}, nil)
	require.ErrorIs(t, err, failure)
	var after int64
	require.NoError(t, db.Model(ModelFile).Count(&after).Error)
	require.Equal(t, before, after)
	rows, err := l.LocationOriginalsPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "kept", rows[0].Path)
	actual, err := l.GetLocation(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, s.Revision, actual.Revision)
}

func TestLocationPublicationDoesNotBorrowHistoricalIdentity(t *testing.T) {
	ctx := context.Background()
	db, l := newTestLibrary(t)
	source := locationTestSource(t, l)
	position := observedTestEntry("ordinary.txt", "content")
	signature, err := NewFileSignature(position.Hash, position.Size)
	require.NoError(t, err)
	existing := &File{Name: "organized.txt", Mode: 0644, Note: "preserve"}
	require.NoError(t, l.SaveFile(ctx, existing))
	require.NoError(t, db.Create(&FileVersion{FileID: existing.ID, Signature: signature, Hash: position.Hash, Size: position.Size}).Error)
	_, err = l.PublishAnalyzed(ctx, source.ID, 1, observedTestManifest(position), nil)
	require.NoError(t, err)
	rows, err := l.LocationOriginalsPage(ctx, source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotEqual(t, existing.ID, rows[0].FileID)
	kept, err := l.GetFile(ctx, existing.ID)
	require.NoError(t, err)
	require.Equal(t, "preserve", kept.Note)
}

func TestIgnoreRulesAndLifecycle(t *testing.T) {
	// Typed rules preserve their original order and subtree boundaries.
	text := "# Keep the configuration verbatim\n/logs/current\n/future\n/logs\n/logs\n"
	rules, err := NormalizeIgnoreRules(&entity.IgnoreRules{Format: "gitignore", Text: text})
	require.NoError(t, err)
	require.Equal(t, text, rules.Text)
	location := &Location{Config: &entity.LocationConfig{Ignore: rules}}
	require.True(t, location.Ignored("logs/current", false))
	require.False(t, location.Ignored("logs-other", false))
	for _, invalid := range []string{"", "paths", "unknown"} {
		_, err := NormalizeIgnoreRules(&entity.IgnoreRules{Format: invalid})
		require.Error(t, err, invalid)
	}

	// Name changes preserve verification; exclusions invalidate access but retain the index.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := locationTestSource(t, l)
	s, err = l.PublishAnalyzed(ctx, s.ID, 1, observedTestManifest(observedTestEntry("x", "")), nil)
	require.NoError(t, err)
	s.Name = "renamed"
	s, err = l.UpdateLocation(ctx, s)
	require.NoError(t, err)
	s.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "/future"}
	s, err = l.UpdateLocation(ctx, s)
	require.NoError(t, err)
	rows, err := l.LocationOriginalsPage(ctx, s.ID, "", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestLocationConfigurationKeepsTrackingEvidence(t *testing.T) {
	// Recorded evidence survives a configuration change. It only ever confirms: an observation
	// that disagrees with it, including one taken under a different root, stops confirming
	// instead of being assumed, so dropping it can only unconfirm Files the operator still has.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := locationTestSource(t, l)
	position := observedTestEntry("x.txt", "content")
	position.TrackingKeys = []*FileTrackingKey{{Kind: TrackingNative, Scope: "local/fs/1", KeyValue: []byte("42")}}
	s, err := l.PublishAnalyzed(ctx, s.ID, 1, observedTestManifest(position), nil)
	require.NoError(t, err)
	rows, err := l.LocationOriginalsPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	fileID := rows[0].FileID
	before, err := l.ReadFileTracking(ctx, fileID)
	require.NoError(t, err)
	require.Len(t, before, 1)

	// Both a rule change and a root change keep the evidence and the association.
	s.RootPath = filepath.Join(t.TempDir(), "moved")
	s.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "/future"}
	updated, err := l.UpdateLocation(ctx, s)
	require.NoError(t, err)
	require.Equal(t, s.RootPath, updated.RootPath)
	require.Equal(t, "/future", updated.Config.Ignore.Text)
	after, err := l.ReadFileTracking(ctx, fileID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, before[0].KeyValue, after[0].KeyValue)
	remaining, err := l.LocationOriginalsPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestIgnoreConfigurationHasOneTypedFormat(t *testing.T) {
	// Missing configuration initializes an explicitly typed empty rule set.
	empty, err := NormalizeIgnoreRules(nil)
	require.NoError(t, err)
	require.Equal(t, "gitignore", empty.Format)
	require.Empty(t, empty.Text)

	// Validation does not rewrite comments, ordering, duplicates or escaped trailing spaces.
	input := &entity.IgnoreRules{Format: "gitignore", Text: "# note\n\n*.tmp\n!keep.tmp\n/root\\ \n*.tmp\n"}
	actual, err := NormalizeIgnoreRules(input)
	require.NoError(t, err)
	require.True(t, proto.Equal(input, actual))
	actual.Text = "changed"
	require.NotEqual(t, input.Text, actual.Text)
	for _, text := range []string{"a\x00b", string([]byte{0xff})} {
		_, err := NormalizeIgnoreRules(&entity.IgnoreRules{Format: "gitignore", Text: text})
		require.Error(t, err)
	}
}

func TestLocationConfigRoundTripAndLegacyIDReplacement(t *testing.T) {
	// Export a complete online reference group and import it as it stands.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := locationTestSource(t, l)
	s.Config.UseMmap = true
	s.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "private/\n"}
	_, err := l.UpdateLocation(ctx, s)
	require.NoError(t, err)
	s, err = l.PublishAnalyzed(ctx, s.ID, 1, observedTestManifest(observedTestEntry("folder/file", "abc")), nil)
	require.NoError(t, err)
	var backup bytes.Buffer
	types := []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION}
	require.NoError(t, l.Export(ctx, &backup, types))
	_, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(backup.Bytes()), false))
	imported, err := target.GetLocation(ctx, s.ID)
	require.NoError(t, err)
	require.True(t, imported.Config.GetUseMmap())
	require.Equal(t, "private/\n", imported.Config.GetIgnore().GetText())
	positions, err := target.LocationOriginalsPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	_, err = target.Trim(ctx, true, true, false)
	require.NoError(t, err)
	_, err = target.GetFile(ctx, positions[0].FileID)
	require.NoError(t, err)

	// A legacy File-only replacement must not inherit the previous numeric-ID online association.
	legacy := fmt.Sprintf(`{"files":[{"id":%d,"name":"replacement","mode":420}]}`, positions[0].FileID)
	require.NoError(t, target.Import(ctx, bytes.NewBufferString(legacy), false))
	positions, err = target.LocationOriginalsPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, positions)
	if _, err := target.GetLocation(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLocationConflictsDoNotMoveOrganizedNodes(t *testing.T) {
	// Occupy the import-directory name with a tagged logical file.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	old := &File{Name: "Unforged", Mode: uint32(fs.FileMode(0644)), Note: "keep"}
	require.NoError(t, l.SaveFile(ctx, old))
	s := locationTestSource(t, l)

	// Import uses a suffixed directory without changing or trashing the existing node.
	_, err := l.PublishAnalyzed(ctx, s.ID, 1, observedTestManifest(observedTestEntry("x", "content")), nil)
	require.NoError(t, err)
	actual, err := l.GetFile(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, old.ID, actual.ID)
	require.Equal(t, old.Name, actual.Name)
	require.Equal(t, old.Note, actual.Note)
	directory, err := l.GetByName(ctx, 0, "Unforged (1)")
	require.NoError(t, err)
	require.True(t, fs.FileMode(directory.Mode).IsDir())
}

func TestLocationV5InvalidReferencesRollbackAcrossBatches(t *testing.T) {
	// Export a source group whose complete file manifest exceeds the import batch size.
	ctx := context.Background()
	_, original := newTestLibrary(t)
	source := locationTestSource(t, original)
	_, err := original.PublishAnalyzed(ctx, source.ID, 1, func(_ context.Context, yield func(*ObservedEntry) error) error {
		for i := 0; i < 230; i++ {
			if err := yield(observedTestEntry(fmt.Sprintf("folder/%04d", i), fmt.Sprint(i))); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	require.NoError(t, err)
	types := []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION}
	var backup bytes.Buffer
	require.NoError(t, original.Export(ctx, &backup, types))
	_, target := newTestLibrary(t)
	kept := locationTestSource(t, target)
	_, err = target.PublishAnalyzed(ctx, kept.ID, 9, observedTestManifest(observedTestEntry("keep", "organized old content")), nil)
	require.NoError(t, err)
	var before bytes.Buffer
	require.NoError(t, target.Export(ctx, &before, types))

	// Reference and path failures after all rows have been loaded still roll back the whole group.
	for _, replacement := range []string{`"location_id":0`, `"location_id":9999999`} {
		broken := strings.Replace(backup.String(), fmt.Sprintf(`"location_id":%d`, source.ID), replacement, 1)
		require.Error(t, target.Import(ctx, strings.NewReader(broken), false))
		var after bytes.Buffer
		require.NoError(t, target.Export(ctx, &after, types))
		require.Equal(t, before.String(), after.String())
	}
	require.Error(t, target.Export(ctx, &bytes.Buffer{}, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION}))
}

func TestLocationFileSearchUsesPublishedLocations(t *testing.T) {
	// Registered accessibility is irrelevant to location-based search and Trim protection.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	source := locationTestSource(t, lib)
	source, err := lib.PublishAnalyzed(ctx, source.ID, 1, observedTestManifest(observedTestEntry("one", "first"), observedTestEntry("two", "second")), nil)
	require.NoError(t, err)
	for _, query := range []string{fmt.Sprintf("location:%d", source.ID), "has:original", "has:original AND NOT has:archive"} {
		page, err := lib.SearchFiles(ctx, query, "", 100)
		require.NoError(t, err)
		require.Len(t, page.Results, 2, query)
	}
	for _, query := range []string{"location:0", "location:unknown", "has:backup"} {
		_, err := lib.SearchFiles(ctx, query, "", 100)
		require.Error(t, err)
	}
}

func TestLocationLongNamesOnlySuffixNewNodes(t *testing.T) {
	// Full ordinary filename lengths remain supported, including UTF-8 names that need a suffix.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	source := locationTestSource(t, lib)
	name := strings.Repeat("日", 80) + ".txt"
	source = publishComplete(t, lib, source.ID, 1, observedTestEntry(name, "old"))
	publishComplete(t, lib, source.ID, 2, observedTestEntry(name, "new"))
	page, err := lib.SearchFiles(ctx, "type:file", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 2)
	for _, row := range page.Results {
		require.LessOrEqual(t, len(row.File.Name), 256)
	}
}
