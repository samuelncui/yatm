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

func onlineTestSource(t *testing.T, l *Library) *Location {
	t.Helper()
	s := &Location{Name: "Everyday", ExecutorID: "local", RootPath: filepath.Join(t.TempDir(), "source")}
	require.NoError(t, l.CreateOnlineSource(context.Background(), s))
	return s
}

func onlineTestPosition(name, content string) *OnlinePosition {
	hash := sha256.Sum256([]byte(content))
	return &OnlinePosition{Path: name, Size: int64(len(content)), Mode: 0644, MtimeNS: 1000000001, Hash: hash[:]}
}

func onlineTestManifest(rows ...*OnlinePosition) OnlineManifest {
	return func(_ context.Context, yield func(*OnlinePosition) error) error {
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestOnlinePublicationPreservesIdentityAndTrimProtection(t *testing.T) {
	// Publish duplicate content as independent Library identities.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := onlineTestSource(t, l)
	s, err := l.PublishOnline(ctx, s.ID, s.Revision, 7, onlineTestManifest(onlineTestPosition("a.txt", "same"), onlineTestPosition("dir/b.txt", "same")))
	require.NoError(t, err)
	rows, err := l.OnlineFilesPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].FileID, rows[1].FileID)
	file, err := l.GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	file.Name, file.Note = "organized.txt", "keep this note"
	require.NoError(t, l.SaveFile(ctx, file))

	// A physical rename keeps all logical facts and invalidates old physical page cursors.
	oldRevision := s.Revision
	renamed := onlineTestPosition("renamed.txt", "same")
	renamed.FileID = file.ID // The Sync matcher supplies the chosen identity before publication.
	s, err = l.PublishOnline(ctx, s.ID, s.Revision, 8, onlineTestManifest(renamed))
	require.NoError(t, err)
	_, _, _, err = l.ListOnlinePositions(ctx, s.ID, "", "a.txt", oldRevision, 1)
	require.ErrorIs(t, err, ErrOnlineConflict)
	require.NoError(t, l.Trim(ctx, true, true))
	kept, err := l.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "organized.txt", kept.Name)
	require.Equal(t, "keep this note", kept.Note)

	// A matched path overwrite changes current facts, not the organized File.
	changed := onlineTestPosition("renamed.txt", "new")
	changed.FileID = file.ID
	s, err = l.PublishOnline(ctx, s.ID, s.Revision, 9, onlineTestManifest(changed))
	require.NoError(t, err)
	rows, err = l.OnlineFilesPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Equal(t, file.ID, rows[0].FileID)
	created, err := l.GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	require.Equal(t, "keep this note", created.Note)
	_, err = l.GetFile(ctx, file.ID)
	require.NoError(t, err)

	// Source removal only removes positions; explicit Trim is the separate metadata cleanup.
	require.NoError(t, l.DeleteOnlineSource(ctx, s.ID, s.Revision))
	_, err = l.GetFile(ctx, created.ID)
	require.NoError(t, err)
	require.NoError(t, l.Trim(ctx, true, true))
	_, err = l.GetFile(ctx, created.ID)
	require.ErrorIs(t, err, ErrFileNotFound)
}

func TestOnlinePublicationRollsBackAcrossBatches(t *testing.T) {
	// Establish an authoritative old index before attempting a large incomplete replacement.
	ctx := context.Background()
	db, l := newTestLibrary(t)
	s := onlineTestSource(t, l)
	s, err := l.PublishOnline(ctx, s.ID, s.Revision, 1, onlineTestManifest(onlineTestPosition("kept", "old")))
	require.NoError(t, err)
	var before int64
	require.NoError(t, db.Model(ModelFile).Count(&before).Error)
	failure := errors.New("incomplete manifest")

	// Failure after multiple flushed batches rolls back new Files, folders, positions, and source facts.
	_, err = l.PublishOnline(ctx, s.ID, s.Revision, 2, func(_ context.Context, yield func(*OnlinePosition) error) error {
		for i := 0; i < 250; i++ {
			if err := yield(onlineTestPosition(fmt.Sprintf("deep/%04d", i), fmt.Sprint(i))); err != nil {
				return err
			}
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var after int64
	require.NoError(t, db.Model(ModelFile).Count(&after).Error)
	require.Equal(t, before, after)
	rows, err := l.OnlineFilesPage(ctx, s.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "kept", rows[0].Path)
	actual, err := l.GetOnlineSource(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, s.Revision, actual.Revision)
}

func TestOnlinePublicationDoesNotBorrowHistoricalIdentity(t *testing.T) {
	ctx := context.Background()
	db, l := newTestLibrary(t)
	source := onlineTestSource(t, l)
	position := onlineTestPosition("ordinary.txt", "content")
	signature, err := NewFileSignature(position.Hash, position.Size)
	require.NoError(t, err)
	existing := &File{Name: "organized.txt", Mode: 0644, Note: "preserve"}
	require.NoError(t, l.SaveFile(ctx, existing))
	require.NoError(t, db.Create(&FileVersion{FileID: existing.ID, Signature: signature, Hash: position.Hash, Size: position.Size}).Error)
	_, err = l.PublishOnline(ctx, source.ID, source.Revision, 1, onlineTestManifest(position))
	require.NoError(t, err)
	rows, err := l.OnlineFilesPage(ctx, source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotEqual(t, existing.ID, rows[0].FileID)
	kept, err := l.GetFile(ctx, existing.ID)
	require.NoError(t, err)
	require.Equal(t, "preserve", kept.Note)
}

func TestOnlineBindingGateAndExclusions(t *testing.T) {
	// Typed rules preserve their original order and subtree boundaries.
	text := "# Keep the configuration verbatim\n/logs/current\n/future\n/logs\n/logs\n"
	rules, err := NormalizeOnlineExclusions(&entity.OnlineExclusions{Format: "gitignore", Text: text})
	require.NoError(t, err)
	require.Equal(t, text, rules.Text)
	require.True(t, OnlineExcluded(rules, "logs/current"))
	require.False(t, OnlineExcluded(rules, "logs-other"))
	for _, invalid := range []string{"", "paths", "unknown"} {
		_, err := NormalizeOnlineExclusions(&entity.OnlineExclusions{Format: invalid})
		require.Error(t, err, invalid)
	}

	// A source operation blocks its config/import boundary but allows ordinary concurrent reads.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := onlineTestSource(t, l)
	release, err := l.UseOnlineSource(s.ID)
	require.NoError(t, err)
	_, err = l.UpdateOnlineSource(ctx, s, false)
	require.ErrorIs(t, err, ErrOnlineBusy)
	readDone, err := l.UseOnlineRead()
	require.NoError(t, err)
	_, err = l.maintainOnline()
	require.ErrorIs(t, err, ErrOnlineBusy)
	readDone()
	release()

	// Name changes preserve verification; exclusions invalidate access but retain the index.
	s, err = l.PublishOnline(ctx, s.ID, s.Revision, 1, onlineTestManifest(onlineTestPosition("x", "")))
	require.NoError(t, err)
	s.Name = "renamed"
	s, err = l.UpdateOnlineSource(ctx, s, false)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, s.Binding)
	s.Exclusions = &entity.OnlineExclusions{Format: "gitignore", Text: "/future"}
	s, err = l.UpdateOnlineSource(ctx, s, false)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, s.Binding)
	rows, err := l.OnlineFilesPage(ctx, s.ID, "", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestIgnoreConfigurationHasOneTypedFormat(t *testing.T) {
	// Missing configuration initializes an explicitly typed empty rule set.
	empty, err := NormalizeOnlineExclusions(nil)
	require.NoError(t, err)
	require.Equal(t, "gitignore", empty.Format)
	require.Empty(t, empty.Text)

	// Validation does not rewrite comments, ordering, duplicates or escaped trailing spaces.
	input := &entity.OnlineExclusions{Format: "gitignore", Text: "# note\n\n*.tmp\n!keep.tmp\n/root\\ \n*.tmp\n"}
	actual, err := NormalizeOnlineExclusions(input)
	require.NoError(t, err)
	require.True(t, proto.Equal(input, actual))
	actual.Text = "changed"
	require.NotEqual(t, input.Text, actual.Text)
	for _, text := range []string{"a\x00b", string([]byte{0xff})} {
		_, err := NormalizeOnlineExclusions(&entity.OnlineExclusions{Format: "gitignore", Text: text})
		require.Error(t, err)
	}
}

func TestOnlineV5RoundTripAndLegacyIDReplacement(t *testing.T) {
	// Export a complete online reference group and import it with mandatory local confirmation.
	ctx := context.Background()
	_, l := newTestLibrary(t)
	s := onlineTestSource(t, l)
	s, err := l.PublishOnline(ctx, s.ID, s.Revision, 1, onlineTestManifest(onlineTestPosition("folder/file", "abc")))
	require.NoError(t, err)
	var backup bytes.Buffer
	types := []entity.LibraryEntityType{entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION}
	require.NoError(t, l.Export(ctx, &backup, types))
	_, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(backup.Bytes())))
	imported, err := target.GetOnlineSource(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, imported.Binding)
	positions, err := target.OnlineFilesPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.NoError(t, target.Trim(ctx, true, true))
	_, err = target.GetFile(ctx, positions[0].FileID)
	require.NoError(t, err)

	// A legacy File-only replacement must not inherit the previous numeric-ID online association.
	legacy := fmt.Sprintf(`{"files":[{"id":%d,"name":"replacement","mode":420}]}`, positions[0].FileID)
	require.NoError(t, target.Import(ctx, bytes.NewBufferString(legacy)))
	positions, err = target.OnlineFilesPage(ctx, s.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, positions)
	imported, err = target.GetOnlineSource(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, imported.Binding)
}

func TestOnlineConflictsDoNotMoveOrganizedNodes(t *testing.T) {
	// Occupy the import-directory name with a tagged logical file.
	ctx := context.Background()
	db, l := newTestLibrary(t)
	old := &File{Name: "Unforged", Mode: uint32(fs.FileMode(0644)), Note: "keep"}
	require.NoError(t, l.SaveFile(ctx, old))
	s := onlineTestSource(t, l)

	// Import uses a suffixed directory without changing or trashing the existing node.
	_, err := l.PublishOnline(ctx, s.ID, s.Revision, 1, onlineTestManifest(onlineTestPosition("x", "content")))
	require.NoError(t, err)
	actual, err := l.GetFile(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, old.ID, actual.ID)
	require.Equal(t, old.Name, actual.Name)
	require.Equal(t, old.Note, actual.Note)
	var directory File
	require.NoError(t, db.Where("parent_id = ? AND name = ?", 0, "Unforged (1)").First(&directory).Error)
	require.True(t, fs.FileMode(directory.Mode).IsDir())
}

func TestOnlineV5InvalidReferencesRollbackAcrossBatches(t *testing.T) {
	// Export a source group whose complete file manifest exceeds the import batch size.
	ctx := context.Background()
	_, original := newTestLibrary(t)
	source := onlineTestSource(t, original)
	_, err := original.PublishOnline(ctx, source.ID, source.Revision, 1, func(_ context.Context, yield func(*OnlinePosition) error) error {
		for i := 0; i < 230; i++ {
			if err := yield(onlineTestPosition(fmt.Sprintf("folder/%04d", i), fmt.Sprint(i))); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	types := []entity.LibraryEntityType{entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION}
	var backup bytes.Buffer
	require.NoError(t, original.Export(ctx, &backup, types))
	_, target := newTestLibrary(t)
	kept := onlineTestSource(t, target)
	_, err = target.PublishOnline(ctx, kept.ID, kept.Revision, 9, onlineTestManifest(onlineTestPosition("keep", "organized old content")))
	require.NoError(t, err)
	var before bytes.Buffer
	require.NoError(t, target.Export(ctx, &before, types))

	// Reference and path failures after all rows have been loaded still roll back the whole group.
	for _, replacement := range []string{`"location_id":0`, `"location_id":9999999`} {
		broken := strings.Replace(backup.String(), fmt.Sprintf(`"location_id":%d`, source.ID), replacement, 1)
		require.Error(t, target.Import(ctx, strings.NewReader(broken)))
		var after bytes.Buffer
		require.NoError(t, target.Export(ctx, &after, types))
		require.Equal(t, before.String(), after.String())
	}
	require.Error(t, target.Export(ctx, &bytes.Buffer{}, []entity.LibraryEntityType{entity.LibraryEntityType_LOCATION}))
}

func TestOnlineFileSearchUsesPublishedLocations(t *testing.T) {
	// Registered accessibility is irrelevant to location-based search and Trim protection.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	source := onlineTestSource(t, lib)
	source, err := lib.PublishOnline(ctx, source.ID, source.Revision, 1, onlineTestManifest(onlineTestPosition("one", "first"), onlineTestPosition("two", "second")))
	require.NoError(t, err)
	for _, query := range []string{fmt.Sprintf("location:%d", source.ID), "has:online", "has:online AND NOT has:archive"} {
		page, err := lib.SearchFiles(ctx, query, "", 100)
		require.NoError(t, err)
		require.Len(t, page.Results, 2, query)
	}
	for _, query := range []string{"location:0", "location:unknown", "has:backup"} {
		_, err := lib.SearchFiles(ctx, query, "", 100)
		require.Error(t, err)
	}
}

func TestOnlineLongNamesOnlySuffixNewNodes(t *testing.T) {
	// Full ordinary filename lengths remain supported, including UTF-8 names that need a suffix.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	source := onlineTestSource(t, lib)
	name := strings.Repeat("日", 80) + ".txt"
	source, err := lib.PublishOnline(ctx, source.ID, source.Revision, 1, onlineTestManifest(onlineTestPosition(name, "old")))
	require.NoError(t, err)
	_, err = lib.PublishOnline(ctx, source.ID, source.Revision, 2, onlineTestManifest(onlineTestPosition(name, "new")))
	require.NoError(t, err)
	page, err := lib.SearchFiles(ctx, "type:file", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 2)
	for _, row := range page.Results {
		require.LessOrEqual(t, len(row.File.Name), 256)
	}
}
