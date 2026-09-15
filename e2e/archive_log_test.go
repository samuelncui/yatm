//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func requireArchiveNoSpaceCheckpointLog(
	t *testing.T,
	ctx context.Context,
	client entity.JobServiceClient,
	jobID, mediaID, files, writtenBytes int64,
) {
	t.Helper()
	reply, err := client.GetLog(ctx, &entity.GetJobLogRequest{Id: jobID})
	require.NoError(t, err)

	var checkpoint map[string]string
	scanner := bufio.NewScanner(bytes.NewReader(reply.Logs))
	for scanner.Scan() {
		fields := parseLogFields(scanner.Text())
		if fields["event"] == "archive_media_checkpoint" {
			checkpoint = fields
		}
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, "no_space", checkpoint["reason"])
	require.Equal(t, fmt.Sprint(mediaID), checkpoint["media_id"])
	require.Equal(t, fmt.Sprint(files), checkpoint["files"])
	require.Equal(t, fmt.Sprint(writtenBytes), checkpoint["bytes"])
	require.Contains(t, checkpoint, "error")
}

func parseLogFields(line string) map[string]string {
	result := make(map[string]string)
	for _, field := range strings.Fields(line) {
		key, value, found := strings.Cut(field, "=")
		if found {
			result[key] = strings.Trim(value, `"`)
		}
	}
	return result
}
