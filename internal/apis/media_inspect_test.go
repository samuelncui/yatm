package apis_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestMediaInspectTapePrefersDeviceBarcodeAndReturnsLibraryStats(t *testing.T) {
	// Simulate only barcode inspection; no physical Tape device is opened.
	ctx := context.Background()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	readInfo := filepath.Join(root, "read-info")
	require.NoError(t, os.WriteFile(readInfo, []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' '{\"barcode\":\"ABC001\"}' > \"$OUT\"\n"), 0o755))
	exe := executor.New(executorDB, lib, []string{"/dev/nst0"}, executor.Paths{}, executor.Scripts{ReadInfo: readInfo}, nil)
	require.NoError(t, exe.AutoMigrate())
	api := apis.New(lib, exe)

	// Publish inventory facts for the barcode returned by the script.
	hash := sha256.Sum256([]byte("fixture"))
	written := time.Unix(20, 123456789)
	order := make([]byte, 17)
	order[0] = 'b'
	media, err := lib.CommitMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001", Name: "fixture",
		Profile:     (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack(),
		CreatedAtNS: 10_123_456_789,
	}, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{
			Path: "file.txt", Size: 7, Mode: 0o644, WriteTime: written, Hash: hash[:], StorageOrder: order,
			StorageMetadata: (&entity.LtfsMetadata{Extents: []*entity.LtfsExtent{{
				Partition: "b", StartBlock: 1, ByteCount: 7,
			}}}).Pack(),
		})
	})
	require.NoError(t, err)

	// Observed identity wins over a caller fallback and resolves the matching catalog statistics.
	fallback := "ZZZ999"
	request := (&entity.InspectMediaTapeTarget{Device: "/dev/nst0"}).Pack()
	request.Identity = &fallback
	reply, err := entity.NewMediaServiceClient(domainConnection(t, api)).Inspect(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ABC001", reply.Identity)
	require.NotNil(t, reply.Media)
	require.Equal(t, media.ID, reply.Media.Id)
	require.EqualValues(t, 10_123_456_789, reply.Media.CreatedAtNs)
	require.Equal(t, library.TapeFormatLTFSV1, reply.Media.Profile.GetTape().Format)
	require.Equal(t, int64(1), reply.FileCount)
	require.NotNil(t, reply.LastWrittenAtNs)
	require.Equal(t, written.UnixNano(), *reply.LastWrittenAtNs)
}

func TestMediaInspectTapeUsesManualBarcodeWhenDeviceHasNone(t *testing.T) {
	// An isolated inspection script reports no physical barcode.
	ctx := context.Background()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	readInfo := filepath.Join(root, "read-info")
	require.NoError(t, os.WriteFile(readInfo, []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' '{}' > \"$OUT\"\n"), 0o755))
	exe := executor.New(executorDB, lib, []string{"/dev/nst0"}, executor.Paths{}, executor.Scripts{ReadInfo: readInfo}, nil)
	require.NoError(t, exe.AutoMigrate())
	api := apis.New(lib, exe)

	// Read-only inspection may show a normalized caller fallback without inventing Media metadata.
	barcode := "abc001"
	request := (&entity.InspectMediaTapeTarget{Device: "/dev/nst0"}).Pack()
	request.Identity = &barcode
	reply, err := entity.NewMediaServiceClient(domainConnection(t, api)).Inspect(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ABC001", reply.Identity)
	require.Nil(t, reply.Media)
	require.Nil(t, reply.LastWrittenAtNs)
}
