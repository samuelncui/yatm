package scan

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarScanFreezesLocatorAndRejectsForeignHint(t *testing.T) {
	// Verify freezes physical placement with expected content before any Tape read.
	ctx := context.Background()
	exe, _, _ := setupScanExecutor(t)
	profile := (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
		InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 262144}}).Pack()
	locator := &entity.TarMetadata{TapeFile: 1, HeaderOffsetBytes: 512}
	metadata := locator.Pack()
	media, err := exe.Lib().CommitMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "TARSCAN", Profile: profile},
		func(_ context.Context, yield func(*library.MediaFile) error) error {
			return yield(&library.MediaFile{Path: "archives/0000000000000001/member", Mode: 0644, StorageOrder: entity.TarStorageOrder(locator), StorageMetadata: metadata})
		})
	require.NoError(t, err)
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)
	require.NoError(t, r.freezeMedia(ctx, &config))
	var entry Entry
	require.NoError(t, r.db.First(&entry).Error)
	require.True(t, proto.Equal(metadata, entry.Storage.Metadata))
	require.Equal(t, entity.TarStorageOrder(locator), entry.StorageOrder)

	// A caller hint cannot substitute different Media or its expected profile.
	request := (&entity.ReadTapeTarget{Device: "/dev/unconfigured", MediaId: proto.Int64(media.ID + 100)}).Pack()
	request.ExpectedIdentity = "CLIENT"
	request.ExpectedProfile = (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack()
	_, err = r.openSession(ctx, &config, request)
	require.ErrorContains(t, err, "hint differs from frozen Media")
}
