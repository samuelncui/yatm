package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilesPerformanceFixtureNames(t *testing.T) {
	// Odd counts also verify that associations follow lexical names rather than creation order.
	for _, order := range []string{"random", "reverse"} {
		t.Run(order, func(t *testing.T) {
			// Inspect all physical files, including those excluded from the projected List.
			const count = 101
			ctx := context.Background()
			rules := filesPerformanceRules()
			service, directory := setupFilesListMixedBenchmark(t, count, order, 16, rules)
			location, err := service.api.lib.GetLocation(ctx, directory.GetLocation().LocationId)
			require.NoError(t, err)
			require.Equal(t, rules, location.Config.Ignore.Text)
			require.Len(t, strings.Split(directory.GetLocation().Path, "/"), 16)
			physical := filepath.Join(location.RootPath, filepath.FromSlash(directory.GetLocation().Path))
			entries, err := os.ReadDir(physical)
			require.NoError(t, err)
			require.Len(t, entries, count)

			// Content, mode and recorded facts must describe each final filename exactly.
			for index, entry := range entries {
				name := fmt.Sprintf("entry-%08d", index)
				require.Equal(t, name, entry.Name())
				data, err := os.ReadFile(filepath.Join(physical, name))
				require.NoError(t, err)
				require.Equal(t, "content", string(data))
				info, err := entry.Info()
				require.NoError(t, err)
				require.EqualValues(t, 0644, info.Mode().Perm())
				association, err := service.api.lib.GetFileLocationAtPath(ctx, location.ID, directory.GetLocation().Path+"/"+name)
				require.NoError(t, err)
				if index%2 != 0 {
					require.Nil(t, association)
					continue
				}
				require.NotNil(t, association)
				require.Equal(t, info.Size(), association.Size)
				require.Equal(t, uint32(info.Mode()), association.Mode)
				require.Equal(t, info.ModTime().UnixNano(), association.MtimeNS)
				file, err := service.api.lib.GetFile(ctx, association.FileID)
				require.NoError(t, err)
				require.Equal(t, name, file.Name)
			}

			// The 102 rules exclude ten names and restore the one ending in 17.
			stream := &filesStructuralRecorder{filesListRecorder: filesListRecorder{ctx: ctx}, maxBatch: 8}
			require.NoError(t, service.List(filesPerformanceRequest(directory, 8), stream))
			require.Equal(t, 92, stream.count)
			require.EqualValues(t, 92, stream.total)
			require.Equal(t, 51, stream.associated)
		})
	}
}

func TestFilesLocationFixtureNames(t *testing.T) {
	// The existing unmixed helper keeps its original names and complete List contract.
	service, directory, physical := setupFilesLocationBenchmark(t, 3, 0, "")
	entries, err := os.ReadDir(physical)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	for index, entry := range entries {
		require.Equal(t, fmt.Sprintf("file-%05d.txt", index), entry.Name())
	}

	// The shared setup does not add catalog associations for plain live fixtures.
	stream := &filesStructuralRecorder{filesListRecorder: filesListRecorder{ctx: context.Background()}, maxBatch: 2}
	require.NoError(t, service.List(filesPerformanceRequest(directory, 2), stream))
	require.Equal(t, 3, stream.count)
	require.EqualValues(t, 3, stream.total)
	require.Zero(t, stream.associated)
}
