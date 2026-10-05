package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/treeops"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImplicitMovePreservesBasenameWhitespace(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(fmt.Sprintf("library=%t", logical), func(t *testing.T) {
			// The provider supplies the existing basename, including all leading and trailing spaces.
			f := organizationFixture{f: setup(t), logical: logical}
			source := f.file(t, "from/  name.txt  ")
			target := f.dir(t, "to")
			stream := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
				Sources: []*entity.FileOperationRef{source}, Destination: target}, nil)

			// An implicit move changes only the parent; no trimmed destination is created.
			require.Zero(t, stream.summary().FailedCount)
			if logical {
				file, err := f.f.exe.Lib().GetFile(context.Background(), source.GetFileId())
				require.NoError(t, err)
				require.Equal(t, target.GetFileId(), file.ParentID)
				require.Equal(t, "  name.txt  ", file.Name)
				f.exists(t, "to/name.txt", false)
				return
			}
			f.exists(t, "to/  name.txt  ", true)
			f.exists(t, "to/name.txt", false)
			f.exists(t, "from/  name.txt  ", false)
		})
	}
}

func TestCancellationDuringPhysicalPublicationSettlesBeforeNextPrimitive(t *testing.T) {
	// Cancel only after the first syscall moved its file and before Library publication queries it.
	f := setup(t)
	f.write(t, "a", "first")
	f.write(t, "b", "second")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "to"), 0755))
	first, second := f.original(t, "a"), f.original(t, "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := false
	require.NoError(t, f.libDB.Callback().Query().Before("gorm:query").Register("cancel-after-move", func(tx *gorm.DB) {
		if cancelled || tx.Statement.Table != "locations" {
			return
		}
		if _, err := os.Stat(filepath.Join(f.root, "to/a")); err != nil {
			return
		}
		cancelled = true
		cancel()
		require.NoError(t, tx.Statement.Context.Err())
		require.Nil(t, context.Cause(tx.Statement.Context))
		_, deadline := tx.Statement.Context.Deadline()
		require.False(t, deadline)
	}))
	stream := &operationStream{ctx: ctx}
	err := (&service{exe: f.exe}).executeRequest(&operationRequest{Spec: &entity.FileOperationSpec{
		Kind:    entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "a"), f.ref(t, "b")}, Destination: f.ref(t, "to")}}, stream)

	// Publication and receipt delivery settle once; the second primitive keeps both bytes and association.
	require.True(t, cancelled)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, stream.updates, 2)
	require.Equal(t, entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_SUCCEEDED, stream.updates[1].Entry.Outcome)
	require.Equal(t, "to/a", stream.updates[1].Entry.TargetPath)
	for _, test := range []struct {
		id   int64
		path string
	}{{first.ID, "to/a"}, {second.ID, "b"}} {
		original, err := f.exe.Lib().GetFileLocation(context.Background(), test.id)
		require.NoError(t, err)
		require.Equal(t, test.path, original.Path)
		require.FileExists(t, filepath.Join(f.root, test.path))
	}
	require.NoFileExists(t, filepath.Join(f.root, "to/b"))
}

func TestPhysicalWalkOwnsOneDirectoryAcrossBoundedPages(t *testing.T) {
	// A wide directory spans many pages, including names hidden by ordinary Location browsing.
	f := setup(t)
	for index := 0; index < 8*treeops.PageSize+3; index++ {
		f.write(t, fmt.Sprintf("wide/.entry-%04d", index), "content")
	}
	store := &locationTree{op: &operation{exe: f.exe}, location: f.location}
	parent, err := store.Stat(context.Background(), "path:wide")
	require.NoError(t, err)
	countDescriptors := func() int {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			return -1
		}
		return len(entries)
	}
	before := countDescriptors()
	seen := make(map[string]bool)
	err = store.WalkChildren(context.Background(), parent, func(node treeops.Node) error {
		require.False(t, seen[node.Ref])
		seen[node.Ref] = true
		if before >= 0 {
			require.Equal(t, before+1, countDescriptors(), "one owned directory remains open throughout the walk")
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 8*treeops.PageSize+3)
	if before >= 0 {
		require.Equal(t, before, countDescriptors())
	}

	// Both a visitor failure and cancellation unwind the open directory immediately.
	failure := errors.New("stop directory visit")
	err = store.WalkChildren(context.Background(), parent, func(treeops.Node) error { return failure })
	require.ErrorIs(t, err, failure)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visits := 0
	err = store.WalkChildren(ctx, parent, func(treeops.Node) error {
		visits++
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, visits)
	if before >= 0 {
		require.Equal(t, before, countDescriptors())
	}
}

func TestPhysicalWalkKeepsMandatoryBoundaries(t *testing.T) {
	for _, boundary := range []string{"administrator", "registered Location"} {
		t.Run(boundary, func(t *testing.T) {
			// These are operation failures even though browsing may omit the same child.
			f := setup(t)
			f.write(t, "parent/blocked/file", "untouched")
			if boundary == "administrator" {
				f.exe = executor.New(f.executorDB, f.exe.Lib(), nil, executor.Paths{Work: f.work,
					Access: []executor.AccessRange{{Root: f.root, Ignore: "parent/blocked/\n"}}}, executor.Scripts{}, nil)
			} else {
				require.NoError(t, f.exe.Lib().CreateLocation(context.Background(), &library.Location{Name: "Nested", ExecutorID: "local",
					RootPath: filepath.Join(f.root, "parent/blocked")}))
			}
			store := &locationTree{op: &operation{exe: f.exe}, location: f.location}
			err := store.WalkChildren(context.Background(), treeops.Node{Ref: "path:parent", Path: "parent", Directory: true, Exists: true},
				func(treeops.Node) error { t.Fatal("protected child reached the visitor"); return nil })
			require.Error(t, err)
			if boundary == "administrator" {
				require.ErrorIs(t, err, executor.ErrAccessExcluded)
			} else {
				require.ErrorContains(t, err, "overlaps registered Location")
			}
			require.FileExists(t, filepath.Join(f.root, "parent/blocked/file"))
		})
	}
}
