package restore

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRestoreFreezesTarLocatorThroughReopenedBundle(t *testing.T) {
	// Publish a tar Position with physical placement independent of FileVersion facts.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	metadata := (&entity.TarMetadata{TapeFile: 3, HeaderOffsetBytes: 512}).Pack()
	media := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "TAR001", Profile: (&entity.TapeMediaProfile{
		Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111",
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144},
	}).Pack()}
	hash := sha256.Sum256([]byte("saved"))
	stored, err := lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "archives/0000000000000003/file.txt", Mode: 0644, Size: 5, Hash: hash[:], StorageOrder: entity.TarStorageOrder(metadata.GetTar()), StorageMetadata: metadata})
	})
	require.NoError(t, err)
	positions, err := lib.ListMediaFilePositions(ctx, stored.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	_, err = lib.ImportArchivePositions(ctx, []int64{positions[0].ID})
	require.NoError(t, err)
	file, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/TAR001/archives/0000000000000003/file.txt")
	require.NoError(t, err)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.True(t, proto.Equal(metadata, copy.StorageMetadata))
	var frozen File
	require.NoError(t, runner.db.First(&frozen, copy.ItemID).Error)
	candidate := newCopyCandidate(&copy, &frozen)
	copy.StorageMetadata.GetTar().TapeFile = 99
	require.EqualValues(t, 3, candidate.StorageMetadata.GetTar().TapeFile)

	// Closing and reopening the runner reads the same nullable BLOB without a Catalog lookup.
	require.NoError(t, value.Close())
	reopened, err := newRunner(ctx, exe, job)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	var persisted Copy
	require.NoError(t, reopened.(*jobRestoreRunner).db.First(&persisted).Error)
	require.True(t, proto.Equal(metadata, persisted.StorageMetadata))
}

func TestRestoreRejectsBundleMissingFrozenLocatorColumn(t *testing.T) {
	// Remove only the newly required column from an otherwise valid current-family bundle.
	runner, _, _ := setupVolumeRestore(t, "old-candidate-schema", map[string][]byte{"file.txt": []byte("saved")})
	require.NoError(t, runner.db.Migrator().DropColumn(&Copy{}, "storage_metadata"))
	require.NoError(t, runner.Close())
	job, err := runner.exe.GetJob(context.Background(), runner.job.ID)
	require.NoError(t, err)

	// Opening the old Alpha layout rejects it without adding a column or inventing a nil locator.
	reopened, err := newRunner(context.Background(), runner.exe, job)
	require.ErrorContains(t, err, "copies.storage_metadata is missing")
	require.Nil(t, reopened)
	db, err := runner.exe.NewStateDB(context.Background(), job.ID)
	require.NoError(t, err)
	require.False(t, db.Migrator().HasColumn(&Copy{}, "storage_metadata"))
	connection, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, connection.Close())
}
