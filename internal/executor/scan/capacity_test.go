package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestLocationWalkCrossesWidePagesAndEnforcesDepth(t *testing.T) {
	root := t.TempDir()
	source := &library.Location{RootPath: root}

	// Cross several directory pages without retaining a filesystem-sized result slice.
	const pageCount = 8
	for index := 0; index < batchSize*pageCount+17; index++ {
		name := filepath.Join(root, fmt.Sprintf("wide-%05d", index))
		file, err := os.Create(name)
		require.NoError(t, err)
		require.NoError(t, file.Close())
	}
	observed := 0
	require.NoError(t, walk(context.Background(), locationWalkExecutor(t, source), source, "", 0, func(string, os.FileInfo) error {
		observed++
		return nil
	}))
	require.Equal(t, batchSize*pageCount+17, observed)

	// The deepest supported directory is readable; descending once more is rejected.
	components := make([]string, 256)
	for index := range components {
		components[index] = "d"
	}
	deepest := filepath.Join(append([]string{root}, components...)...)
	require.NoError(t, os.MkdirAll(deepest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deepest, "leaf"), nil, 0o644))
	deepSource := &library.Location{RootPath: filepath.Join(root, "d")}
	seenLeaf := false
	require.NoError(t, walk(context.Background(), locationWalkExecutor(t, deepSource), deepSource, "", 1, func(name string, _ os.FileInfo) error {
		seenLeaf = name == strings.Join(append(components[1:], "leaf"), "/")
		return nil
	}))
	require.True(t, seenLeaf)

	require.NoError(t, os.Mkdir(filepath.Join(deepest, "overflow"), 0o755))
	err := walk(context.Background(), locationWalkExecutor(t, source), source, "", 0, func(string, os.FileInfo) error { return nil })
	require.ErrorContains(t, err, "exceeds 256 directory levels")
}

func TestLocationWalkCancellationClosesOpenDirectoryStack(t *testing.T) {
	root := t.TempDir()
	current := root
	for index := 0; index < 32; index++ {
		current = filepath.Join(current, "nested")
		require.NoError(t, os.Mkdir(current, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(current, "leaf"), nil, 0o644))

	before, measured := openDescriptorCount()
	ctx, cancel := context.WithCancel(context.Background())
	source := &library.Location{RootPath: root}
	err := walk(ctx, locationWalkExecutor(t, source), source, "", 0, func(string, os.FileInfo) error {
		cancel()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	if !measured {
		t.Log("open-descriptor measurement is unavailable on this platform")
		return
	}
	after, ok := openDescriptorCount()
	require.True(t, ok)
	require.LessOrEqual(t, after, before)
	t.Logf("open descriptors before canceled traversal=%d after unwind=%d", before, after)
}

func TestScanResultAPIEnforcesBoundedWindows(t *testing.T) {
	exe, source := setupAnalyze(t)
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{
		Selections: scanLocationSelections(source.ID), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
	})
	require.NoError(t, r.db.Exec("DELETE FROM entries").Error)

	rows := make([]*Entry, 0, batchSize)
	for index := 0; index < executor.MaxJobResultPageSize+5; index++ {
		rows = append(rows, &Entry{LocationID: source.ID, Path: fmt.Sprintf("result-%04d", index)})
		if len(rows) == batchSize || index == executor.MaxJobResultPageSize+4 {
			require.NoError(t, r.db.Create(&rows).Error)
			rows = rows[:0]
		}
	}

	service := &service{exe: exe}
	_, err := service.ListEntries(context.Background(), &entity.ListScanJobEntriesRequest{Id: r.job.ID, Limit: executor.MaxJobResultPageSize + 1})
	require.ErrorContains(t, err, "Job result page limit must be between")
	first, err := service.ListEntries(context.Background(), &entity.ListScanJobEntriesRequest{Id: r.job.ID, Limit: executor.MaxJobResultPageSize, IncludeTotal: true})
	require.NoError(t, err)
	require.Len(t, first.Entries, executor.MaxJobResultPageSize)
	require.True(t, first.HasMore)
	require.Equal(t, int64(executor.MaxJobResultPageSize+5), first.GetTotalEntryCount())
	after := strconv.FormatInt(first.Entries[len(first.Entries)-1].Id, 10)
	last, err := service.ListEntries(context.Background(), &entity.ListScanJobEntriesRequest{Id: r.job.ID, Limit: executor.MaxJobResultPageSize, Cursor: after})
	require.NoError(t, err)
	require.Len(t, last.Entries, 5)
	require.False(t, last.HasMore)
	require.Nil(t, last.TotalEntryCount, "an unrequested total must stay absent")
}

func openDescriptorCount() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func locationWalkExecutor(t *testing.T, source *library.Location) *executor.Executor {
	t.Helper()
	// Tests use the same canonical administrator boundary as a registered production Location.
	root, err := filepath.EvalSymlinks(source.RootPath)
	require.NoError(t, err)
	source.RootPath = root
	return executor.New(nil, nil, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}}, executor.Scripts{}, nil)
}
