package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAlreadyStartedWalkExcludesNewTemporaryResources(t *testing.T) {
	// The running scan retains the predicate constructed before another operation starts its probes.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "file.txt", "ordinary")
	_, err := exe.CheckOnlineSource(source)
	require.NoError(t, err)
	name := ".yatm-restore-" + uuid.NewString()
	var release func()
	var first []string
	require.NoError(t, walk(ctx, source, "", 0, func(relative string, _ os.FileInfo) error {
		first = append(first, relative)
		if release != nil {
			return nil
		}
		var err error
		release, err = exe.ProtectTemporaryNames(name)
		if err != nil {
			return err
		}
		writeAnalyzeFile(t, source, name+"/must-not-index", "temporary")
		return nil
	}))
	require.Equal(t, []string{"file.txt"}, first)
	require.NotNil(t, release)
	t.Cleanup(release)

	// The publication validation walk sees the same files, without rebuilding its access predicate.
	var validated []string
	require.NoError(t, walk(ctx, source, "", 0, func(relative string, _ os.FileInfo) error {
		validated = append(validated, relative)
		return nil
	}))
	require.Equal(t, first, validated)
	require.True(t, source.Excluded(name+"/must-not-index", false))
	_, err = exe.OnlineRoot(filepath.Join(source.RootPath, name))
	require.ErrorContains(t, err, "temporary resource")
}
