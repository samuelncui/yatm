package executor

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestCaptureOriginalKeepsOpaqueSignatureAfterLearningHash(t *testing.T) {
	// Imported opaque content can have no SHA-256, and a fresh file has no ACP cache.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	require.NoError(t, exe.lib.AutoMigrate())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	exe = New(exe.db, exe.lib, nil, Paths{Access: []AccessRange{{Root: root}}}, Scripts{}, nil)
	location := &library.Location{Name: "capture", RootPath: root, ExecutorID: "local"}
	require.NoError(t, exe.lib.CreateLocation(ctx, location))
	file := &library.File{Name: "original.txt"}
	require.NoError(t, exe.lib.SaveFile(ctx, file))
	name := filepath.Join(root, file.Name)
	content := []byte("unchanged original")
	require.NoError(t, os.WriteFile(name, content, 0644))
	info, err := os.Stat(name)
	require.NoError(t, err)
	opaque := []byte{2, 0, 255, 17}
	original := &library.FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name,
		Mode: uint32(info.Mode()), Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), Signature: opaque}
	require.NoError(t, exe.db.Create(original).Error)
	_, valid, _ := acp.ReadCachedSignature(name)
	require.False(t, valid)

	// A later capture must retain the content identity whether or not the first read cached its hash.
	hash := sha256.Sum256(content)
	ids := make([]int64, 257)
	ids[0], ids[len(ids)-1] = file.ID, file.ID
	var captures []*entity.ExpectedFile
	require.NoError(t, exe.CaptureOriginals(ctx, ids, func(id int64, path string, expected *entity.ExpectedFile) error {
		require.Equal(t, file.ID, id)
		require.Equal(t, name, path)
		captures = append(captures, expected)
		return nil
	}))
	require.Len(t, captures, 2)
	for _, expected := range captures {
		require.Equal(t, opaque, expected.Signature)
		require.Equal(t, hash[:], expected.Sha256)
	}
	_, again, err := exe.CaptureOriginal(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, opaque, again.Signature)
	stored, err := exe.lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, opaque, stored.Signature)
}

func TestCaptureOriginalsShareParentChecksWithoutBypassingAccess(t *testing.T) {
	// Known Library originals bypass user Ignore, while administrator exclusions always apply.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	require.NoError(t, exe.lib.AutoMigrate())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	exe = New(exe.db, exe.lib, nil, Paths{Access: []AccessRange{{Root: root, Ignore: "/shared/secret.txt"}}}, Scripts{}, nil)
	location := &library.Location{Name: "capture", RootPath: root, ExecutorID: "local",
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "shared/ignored.txt"}}}
	require.NoError(t, exe.lib.CreateLocation(ctx, location))
	require.NoError(t, os.Mkdir(filepath.Join(root, "shared"), 0755))
	var ids []int64
	for _, relative := range []string{"shared/allowed.txt", "shared/ignored.txt", "shared/secret.txt"} {
		file := &library.File{Name: filepath.Base(relative)}
		require.NoError(t, exe.lib.SaveFile(ctx, file))
		name := filepath.Join(root, relative)
		require.NoError(t, os.WriteFile(name, []byte("content"), 0644))
		info, err := os.Stat(name)
		require.NoError(t, err)
		hash := sha256.Sum256([]byte("content"))
		require.NoError(t, exe.db.Create(&library.FileLocation{FileID: file.ID, LocationID: location.ID,
			Path: relative, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(),
			Hash: hash[:], Signature: []byte("content")}).Error)
		ids = append(ids, file.ID)
	}

	// A warmed sibling parent never authorizes an administrator-excluded leaf.
	var captured []int64
	yield := func(id int64, _ string, _ *entity.ExpectedFile) error {
		captured = append(captured, id)
		return nil
	}
	require.NoError(t, exe.CaptureOriginals(ctx, ids[:2], yield))
	require.Equal(t, ids[:2], captured)
	require.ErrorIs(t, exe.CaptureOriginals(ctx, ids, yield), ErrAccessExcluded)

	// A new operation revalidates the parent and cannot follow a replacement symlink.
	moved := filepath.Join(root, "moved")
	require.NoError(t, os.Rename(filepath.Join(root, "shared"), moved))
	require.NoError(t, os.Symlink(moved, filepath.Join(root, "shared")))
	require.Error(t, exe.CaptureOriginals(ctx, ids[:2], yield))
}
