package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestArchiveImportPreservesLiteralUTF8Names(t *testing.T) {
	// Media display text becomes a real component only at this explicit import boundary.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "9ef04c80-e054-41fc-8d7f-ed195eab9b47", Name: "Archive\\'\"\n照片 ",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	for _, name := range []string{`back\slash`, " leading", "trailing ", " \t\n", "quotes'\"\n照片"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			// Admit the recorded copy through the same dry-run and mutation paths.
			position := &Position{MediaID: media.ID, Path: " parent /" + name, Signature: []byte(name), Mode: 0o644}
			require.NoError(t, db.Create(position).Error)
			preview, err := lib.ImportArchivePositionSelection(ctx, []int64{position.ID}, nil, true)
			require.NoError(t, err)
			applied, err := lib.ImportArchivePositionRoots(ctx, []int64{position.ID})
			require.NoError(t, err)
			require.Equal(t, preview.Files, applied.Files)
			require.Len(t, applied.FileIDs, 1)

			// The admitted File keeps each spelling exactly, without synthetic escapes.
			file, err := lib.GetByPath(ctx, 0, "Unforged/"+media.Name+"/ parent /"+name)
			require.NoError(t, err)
			require.NotNil(t, file)
			require.Equal(t, applied.FileIDs[0], file.ID)
			require.Equal(t, name, file.Name)
		})
	}
}

func TestArchiveImportDirectoryUsesComponentSyntax(t *testing.T) {
	// No physical component exists for an empty name, traversal, a slash, NUL or invalid UTF-8.
	for _, name := range []string{"", ".", "..", "a/b", "nul\x00", "invalid\xff"} {
		_, err := archiveImportDirectory(&Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Name: name})
		require.Error(t, err, "%q", name)
	}
	for _, name := range []string{" ", "\t\n", `back\slash`, "�", "quote'\""} {
		actual, err := archiveImportDirectory(&Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Name: name})
		require.NoError(t, err)
		require.Equal(t, name, actual)
	}

	// The existing Tape and unnamed Volume identity fallback remains unchanged.
	for _, media := range []*Media{
		{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "TAPE01", Name: "display/name"},
		{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "volume-id"},
	} {
		actual, err := archiveImportDirectory(media)
		require.NoError(t, err)
		require.Equal(t, media.Identity, actual)
	}
}
