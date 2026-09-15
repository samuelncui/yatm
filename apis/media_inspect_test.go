package apis_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
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
	written := time.Unix(20, 0)
	order := make([]byte, 17)
	order[0] = 'b'
	tape, err := lib.CreateTape(ctx, &library.Tape{
		Barcode: "ABC001", Name: "fixture", Format: library.TapeFormatLTFSV1, CreateTime: time.Unix(10, 0),
	}, []*library.TapeFile{{
		Path: "file.txt", Size: 7, Mode: 0o644, WriteTime: written, Hash: hash[:], StorageOrder: order,
		StorageMetadata: (&entity.LTFSMetadata{Extents: []*entity.LTFSExtent{{
			Partition: "b", StartBlock: 1, ByteCount: 7,
		}}}).Pack(),
	}})
	require.NoError(t, err)

	// Observed identity wins over a caller fallback and resolves the matching catalog statistics.
	fallback := "ZZZ999"
	request := (&entity.MediaInspectTapeTarget{Device: "/dev/nst0"}).Pack()
	request.Identity = &fallback
	reply, err := api.MediaInspect(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ABC001", reply.Identity)
	require.NotNil(t, reply.Media)
	require.Equal(t, tape.ID, reply.Media.Id)
	require.Equal(t, library.TapeFormatLTFSV1, reply.Media.Profile.GetTape().Format)
	require.Equal(t, int64(1), reply.FileCount)
	require.NotNil(t, reply.LastWriteTime)
	require.Equal(t, written.Unix(), *reply.LastWriteTime)
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
	request := (&entity.MediaInspectTapeTarget{Device: "/dev/nst0"}).Pack()
	request.Identity = &barcode
	reply, err := api.MediaInspect(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ABC001", reply.Identity)
	require.Nil(t, reply.Media)
}
