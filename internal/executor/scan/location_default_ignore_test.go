package scan

import (
	"context"
	"os"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestWalkHidesYATMManagedEntries(t *testing.T) {
	// The default Location Ignore rules hide YATM-managed entries; a running walk decides from
	// those rules, so a directory created after the walk started is still hidden.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	source.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: library.DefaultLocationIgnore}
	_, err := exe.Lib().UpdateLocation(ctx, source)
	require.NoError(t, err)

	writeAnalyzeFile(t, source, "file.txt", "ordinary")
	writeAnalyzeFile(t, source, ".yatm.json", "archive marker")
	writeAnalyzeFile(t, source, ".yatm-restore-probe/must-not-index", "temporary")

	var walked []string
	require.NoError(t, walk(ctx, exe, source, "", 0, func(relative string, _ os.FileInfo) error {
		walked = append(walked, relative)
		return nil
	}))
	require.Equal(t, []string{"file.txt"}, walked)

	// Ignore is visibility, not permission: an explicit path stays accessible.
	_, _, err = exe.CheckLocationPath(source, ".yatm.json")
	require.NoError(t, err)
}
