//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/preview"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestCLILibraryPreviewSettings(t *testing.T) {
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A read exposes complete defaults without requiring callers to reconstruct generator options.
	before := new(entity.PreviewSettings)
	cliResult(t, ctx, connection, before, "settings", "preview")
	require.NotEmpty(t, before.GetGenerators())

	// The CLI accepts a complete PreviewSettings document and returns that typed group.
	custom, err := preview.SettingsFromConfig(preview.Config{})
	require.NoError(t, err)
	custom.Generators[0].Extensions = []*entity.PreviewExtension{{Name: "cli-preview", Enabled: true}}
	encoded, err := protojson.Marshal(custom)
	require.NoError(t, err)
	filename := filepath.Join(root, "preview-settings.json")
	require.NoError(t, os.WriteFile(filename, encoded, 0o600))
	saved := new(entity.PreviewSettings)
	cliResult(t, ctx, connection, saved, "settings", "preview", "--preview-json", filename)
	require.True(t, proto.Equal(custom, saved))

	// Updating Library preferences does not rewrite the independently stored Preview group.
	library := new(entity.LibrarySettings)
	cliResult(t, ctx, connection, library, "settings", "library", "--confirm-remove=false")
	require.False(t, library.ConfirmRemove)
	final := new(entity.PreviewSettings)
	cliResult(t, ctx, connection, final, "settings", "preview")
	require.True(t, proto.Equal(custom, final))
}
