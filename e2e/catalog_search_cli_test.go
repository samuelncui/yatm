//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCLICatalogSearch(t *testing.T) {
	// Use actual server/CLI binaries; only empty fixture directories are created outside public commands.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Register mixed names and preferences so filtering must precede each Location page.
	locations := make([]*entity.Location, 0, 7)
	wantLocations := make([]int64, 0, 3)
	for index, item := range []struct {
		name      string
		preferred bool
	}{
		{name: "Other location"},
		{name: "Quarterly 100AA"},
		{name: "Quarterly 100%_!", preferred: true},
		{name: "Quarterly photos", preferred: true},
		{name: "Other preferred", preferred: true},
		{name: "Quarterly documents", preferred: true},
		{name: "Quarterly media"},
	} {
		directory := filepath.Join(root, "originals", fmt.Sprintf("Root-%03d", index))
		require.NoError(t, os.Mkdir(directory, 0o755))
		args := []string{"location", "create", "--name", item.name, "--root", directory}
		if item.preferred {
			args = append(args, "--restore-target")
		}
		reply := new(entity.LocationReply)
		cliResult(t, ctx, connection, reply, args...)
		locations = append(locations, reply.Location)
		if item.preferred && strings.HasPrefix(item.name, "Quarterly") {
			wantLocations = append(wantLocations, reply.Location.Id)
		}
	}

	// A case-insensitive name query and recommendation filter combine before ID-ascending pagination.
	foundLocations := make([]int64, 0, len(wantLocations))
	for after := int64(0); ; {
		page := new(entity.ListLocationsReply)
		cliResult(t, ctx, connection, page, "location", "list", "--query", "qUaRtErLy",
			"--restore-target", "true", "--after-id", decimal(after), "--limit", "1")
		require.Len(t, page.Locations, 1)
		require.Greater(t, page.Locations[0].Id, after)
		after = page.Locations[0].Id
		foundLocations = append(foundLocations, after)
		if !page.HasMore {
			break
		}
	}
	require.Equal(t, wantLocations, foundLocations)

	// Root paths and LIKE punctuation remain searchable literally, without catalog admission or scans.
	locationPage := new(entity.ListLocationsReply)
	cliResult(t, ctx, connection, locationPage, "location", "list", "--query", "ROOT-003")
	require.Len(t, locationPage.Locations, 1)
	require.Equal(t, locations[3].Id, locationPage.Locations[0].Id)
	cliResult(t, ctx, connection, locationPage, "location", "list", "--query", "100%_!")
	require.Len(t, locationPage.Locations, 1)
	require.Equal(t, locations[2].Id, locationPage.Locations[0].Id)
	cliResult(t, ctx, connection, locationPage, "location", "list", "--query", "quarterly", "--restore-target", "false")
	require.Len(t, locationPage.Locations, 2)
	cliResult(t, ctx, connection, locationPage, "location", "list", "--query", "No matching location")
	require.Empty(t, locationPage.Locations)
	_, err := connection.run(ctx, "location", "get", "999999")
	require.Error(t, err)

	// Initialize only isolated mounted directories, never a Tape or hardware device.
	media := make([]*entity.Media, 0, 5)
	wantMedia := make([]int64, 0, 4)
	for index, name := range []string{"Other disk", "Quarterly 100AA", "Quarterly 100%_!", "Quarterly photos", "Quarterly documents"} {
		directory := filepath.Join(root, "volumes", fmt.Sprintf("catalog-disk-%d", index))
		require.NoError(t, os.Mkdir(directory, 0o755))
		reply := new(entity.VolumeInitializeReply)
		cliResult(t, ctx, connection, reply, "volume", "initialize", directory, "--name", name, "--type", "hdd")
		media = append(media, reply.Media)
		if strings.HasPrefix(name, "Quarterly") {
			wantMedia = append(wantMedia, reply.Media.Id)
		}
	}

	// Media names are searched on the server, with a two-row bound and explicit backend kind.
	foundMedia := make([]int64, 0, len(wantMedia))
	for after := int64(0); ; {
		page := new(entity.MediaListReply)
		cliResult(t, ctx, connection, page, "media", "list", "--query", "qUaRtErLy",
			"--kind", "volume", "--after-id", decimal(after), "--limit", "2")
		require.NotEmpty(t, page.Media)
		require.LessOrEqual(t, len(page.Media), 2)
		for _, row := range page.Media {
			require.Greater(t, row.Id, after)
			require.Equal(t, entity.MediaKind_MEDIA_KIND_VOLUME, row.Kind)
			after = row.Id
			foundMedia = append(foundMedia, row.Id)
		}
		if !page.HasMore {
			break
		}
	}
	require.Equal(t, wantMedia, foundMedia)

	// Exact identities, literal metacharacters, absent kinds and stale IDs use the same public transport.
	mediaPage := new(entity.MediaListReply)
	cliResult(t, ctx, connection, mediaPage, "media", "list", "--query", strings.ToUpper(media[3].Identity), "--after-id", "0")
	require.Len(t, mediaPage.Media, 1)
	require.Equal(t, media[3].Id, mediaPage.Media[0].Id)
	cliResult(t, ctx, connection, mediaPage, "media", "list", "--query", "100%_!", "--after-id", "0")
	require.Len(t, mediaPage.Media, 1)
	require.Equal(t, media[2].Id, mediaPage.Media[0].Id)
	cliResult(t, ctx, connection, mediaPage, "media", "list", "--query", "quarterly", "--kind", "tape", "--after-id", "0")
	require.Empty(t, mediaPage.Media)
	require.False(t, mediaPage.HasMore)
	cliResult(t, ctx, connection, mediaPage, "media", "list", "--query", "No matching Media", "--after-id", "0")
	require.Empty(t, mediaPage.Media)
	cliResult(t, ctx, connection, mediaPage, "media", "get", "999999")
	require.Empty(t, mediaPage.Media)
}
