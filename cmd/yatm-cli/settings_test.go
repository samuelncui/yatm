package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestCLIJobExecutionSettings(t *testing.T) {
	// Capture the typed update while the stub rejects unsupported limits like the real service.
	settings := &stubSettingsService{
		job: &entity.JobSettings{Execution: &entity.JobExecutionSettings{ReadBatch: 256, ReadBufferMax: 4096, WriteBufferMax: 4096, WriteBatchSize: 256, FlushIntervalMs: 1000}},
		update: func(request *entity.SettingsValue) (*entity.SettingsValue, error) {
			job := request.GetJob()
			if job.GetExecution().GetReadBatch() < 1 {
				return nil, status.Error(codes.InvalidArgument, "Job execution read batch must be at least 1")
			}
			if job.GetExecution().GetWriteBatchSize() < 1 ||
				job.GetExecution().GetWriteBatchSize() > job.GetExecution().GetWriteBufferMax() {
				return nil, status.Error(codes.InvalidArgument, "Job execution write batch must be between 1 and the write buffer")
			}
			return request, nil
		},
	}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)

	// The group's command reads and writes only that typed group.
	filename := filepath.Join(t.TempDir(), "execution.json")
	require.NoError(t, os.WriteFile(filename, []byte(`{"readBatch":8,"readBufferMax":64,"writeBufferMax":32,"writeBatchSize":16,"flushIntervalMs":250}`), 0o600))
	exit, stdout, stderr := executeTestCLI(server.URL, "", "settings", "job", "--execution-json", filename)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Empty(t, stderr)
	require.JSONEq(t, `{"execution":{"read_batch":8,"read_buffer_max":64,"write_buffer_max":32,"write_batch_size":16,"flush_interval_ms":250}}`, stdout)
	require.Equal(t, 1, recorder.count(entity.SettingsService_Update_FullMethodName))
	require.True(t, proto.Equal(&entity.JobExecutionSettings{ReadBatch: 8, ReadBufferMax: 64, WriteBufferMax: 32, WriteBatchSize: 16, FlushIntervalMs: 250}, settings.updated.GetJob().Execution))

	// Malformed JSON is a usage error, and a value the service rejects keeps its Connect code.
	require.NoError(t, os.WriteFile(filename, []byte(`{"readBatch":`), 0o600))
	exit, stdout, stderr = executeTestCLI(server.URL, "", "settings", "job", "--execution-json", filename)
	require.Equal(t, exitUsage, exit)
	require.Empty(t, stdout)
	var malformed errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &malformed))
	require.Equal(t, "usage", malformed.Code)
	rejected := filepath.Join(t.TempDir(), "rejected.json")
	require.NoError(t, os.WriteFile(rejected, []byte(`{"readBatch":0,"readBufferMax":64,"writeBufferMax":32,"writeBatchSize":16,"flushIntervalMs":250}`), 0o600))
	exit, stdout, stderr = executeTestCLI(server.URL, "", "settings", "job", "--execution-json", rejected)
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	var invalid errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &invalid))
	require.Equal(t, "invalid_argument", invalid.Code)
	require.Contains(t, invalid.Error, "read batch")
}

func TestCLIPreviewSettingsUseTheirOwnGroup(t *testing.T) {
	// A Preview edit carries only that group, so the Library group is never rewritten by it.
	settings := &stubSettingsService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)
	filename := filepath.Join(t.TempDir(), "preview.json")
	require.NoError(t, os.WriteFile(filename, []byte(`{"enabled":true,"concurrency":4,"timeoutSeconds":600,"maxInputPixels":"64000000"}`), 0o600))

	exit, stdout, stderr := executeTestCLI(server.URL, "", "settings", "preview", "--preview-json", filename)
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"enabled":true,"concurrency":4,"timeout_seconds":600,"max_input_pixels":"64000000"}`, stdout)
	require.Equal(t, 1, recorder.count(entity.SettingsService_Update_FullMethodName))
	require.True(t, settings.updated.GetPreview().Enabled)
	require.Equal(t, int32(4), settings.updated.GetPreview().Concurrency)

	// Reading a group returns the effective typed value; unrelated flags remain errors.
	exit, stdout, stderr = executeTestCLI(server.URL, "", "settings", "preview")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"concurrency":2`)
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "library", "--execution-json", filename)
	require.Equal(t, exitUsage, exit)
	require.Contains(t, stderr, "unknown flag")
}
