package library

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestSelectionInspectionCountsWholeTreeWithoutContentReads(t *testing.T) {
	// Published metadata may remain browsable when its actual source directory does not exist.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	location, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, func(_ context.Context, yield func(*OnlinePosition) error) error {
		for index := 0; index < 125; index++ {
			if err := yield(&OnlinePosition{Path: fmt.Sprintf("%03d.txt", index), Size: 2, Mode: 0644, MtimeNS: 1}); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	archived := &File{Name: "archived-only.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, archived))
	version := &FileVersion{FileID: archived.ID, Signature: []byte("lost-copy"), Size: 7}
	require.NoError(t, db.Create(version).Error)
	selection := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: Root.ID}}, Scope: entity.FileScope_FILE_SCOPE_ALL}

	// Repeated roots do not multiply totals; unhashed originals still contribute known sizes.
	reply, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Selections: []*entity.FileSelection{selection, selection}})
	require.NoError(t, err)
	require.EqualValues(t, 126, reply.Files)
	require.EqualValues(t, 250, reply.Bytes)
	require.EqualValues(t, 1, reply.MissingOriginals)
	require.EqualValues(t, 1, reply.UnknownSizeFiles)
	require.Zero(t, reply.MissingCopies)
	require.Len(t, reply.Selections, 2)
	require.NoDirExists(t, location.RootPath, "review does not probe or create filesystem content")

	// Explicit versions and default directory versions deduplicate by version identity, not File identity.
	saved := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: Root.ID}}, Scope: entity.FileScope_FILE_SCOPE_SAVED}
	restored, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{
		Restore: true, Selections: []*entity.FileSelection{saved}, FileVersionIds: []int64{version.ID, version.ID},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, restored.Files)
	require.EqualValues(t, 7, restored.Bytes)
	require.EqualValues(t, 1, restored.MissingCopies)

	// Configuration invalidation is reflected in logical selection readiness without dropping the index.
	location.Exclusions = &entity.OnlineExclusions{Format: "gitignore", Text: "future/"}
	_, err = lib.UpdateOnlineSource(ctx, location, false)
	require.NoError(t, err)
	reply, err = lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Selections: []*entity.FileSelection{selection}})
	require.NoError(t, err)
	require.EqualValues(t, 126, reply.Files)
	require.EqualValues(t, 126, reply.MissingOriginals)
}

func TestRestoreEstimateUsesHealthAndReportsIgnoredOutputs(t *testing.T) {
	// A damaged physical copy is retained inventory, not a default Restore candidate.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	location.Exclusions = &entity.OnlineExclusions{Format: "gitignore", Text: "output/\n!output/saved.txt"}
	_, err := lib.UpdateOnlineSource(ctx, location, false)
	require.NoError(t, err)
	file := &File{Name: "saved.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	version := &FileVersion{FileID: file.ID, Signature: []byte("opaque"), Hash: bytes.Repeat([]byte{1}, 32), Size: 7}
	require.NoError(t, db.Create(version).Error)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ESTIMATE", Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "stored.txt", Signature: version.Signature, Hash: version.Hash, Size: 7, Health: entity.PositionHealth_DAMAGED}).Error)
	request := &entity.InspectSelectionRequest{Restore: true, FileVersionIds: []int64{version.ID, version.ID},
		Destination: &entity.RestoreDestination{LocationId: location.ID, Path: "output"}}
	reply, err := lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.Files)
	require.EqualValues(t, 7, reply.Bytes)
	require.Zero(t, reply.UnknownSizeFiles)
	require.EqualValues(t, 1, reply.MissingCopies)
	require.EqualValues(t, 1, reply.IgnoredOutputs)

	// Explicit salvage changes availability, never the expected size or Ignore result.
	request.AllowDamagedCopies = true
	reply, err = lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.Zero(t, reply.MissingCopies)
	require.EqualValues(t, 7, reply.Bytes)
	require.EqualValues(t, 1, reply.IgnoredOutputs)
}

func TestRestoreEstimateRejectsMissingOrContradictoryIntegrityFacts(t *testing.T) {
	for _, scenario := range []string{"missing baseline", "negative size", "copy hash", "copy size", "missing media"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, lib := newTestLibrary(t)
			file := &File{Name: "saved.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, lib.SaveFile(ctx, file))
			version := &FileVersion{FileID: file.ID, Signature: []byte{0, 255, 1}, Hash: bytes.Repeat([]byte{1}, 32), Size: 7}
			if scenario == "missing baseline" {
				version.Hash = nil
			}
			if scenario == "negative size" {
				version.Size = -1
			}
			require.NoError(t, db.Create(version).Error)
			media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ESTIMATE", Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()})
			require.NoError(t, err)
			copy := &Position{MediaID: media.ID, Path: "stored.txt", Signature: version.Signature, Hash: version.Hash, Size: version.Size}
			switch scenario {
			case "copy hash":
				copy.Hash = bytes.Repeat([]byte{2}, 32)
			case "copy size":
				copy.Size++
			case "missing media":
				copy.MediaID = media.ID + 1
			}
			require.NoError(t, db.Create(copy).Error)
			for _, salvage := range []bool{false, true} {
				reply, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Restore: true, FileVersionIds: []int64{version.ID}, AllowDamagedCopies: salvage})
				require.NoError(t, err)
				require.EqualValues(t, 1, reply.MissingCopies)
				require.EqualValues(t, 1, reply.Files)
			}
		})
	}
}
