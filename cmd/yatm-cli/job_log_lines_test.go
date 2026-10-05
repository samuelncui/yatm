package main

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestJobLogLinesUsesServerCursorAndFilter(t *testing.T) {
	// A CLI page passes byte position and filters to the public API without a client limit.
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{logLines: func(_ context.Context, req *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
			require.Equal(t, int64(42), req.Id)
			require.Equal(t, entity.JobLogDirection_JOB_LOG_DIRECTION_NEWER, req.Direction)
			require.Equal(t, int64(4096), req.GetCursor())
			require.Equal(t, "error", req.Level)
			require.Equal(t, "needle", req.Query)
			return &entity.ListJobLogLinesResponse{AfterCursor: 8192, HasNewer: true}, nil
		}})
	}, nil, nil)

	// The returned cursor remains visible to scripts that continue the scan.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "log-lines", "42", "--direction", "newer", "--cursor", "4096", "--level", "error", "--query", "needle")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"after_cursor":"8192"`)
	require.Equal(t, 1, recorder.count(entity.JobService_ListLogLines_FullMethodName))
}
