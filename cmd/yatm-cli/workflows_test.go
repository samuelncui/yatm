package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestJobWaitStopsWithoutTakingAction(t *testing.T) {
	// Exercise completion, intervention and timeout with observations only.
	for _, test := range []struct {
		name   string
		status entity.JobStatus
		phase  entity.JobPhase
		code   string
	}{
		{"completed", entity.JobStatus_COMPLETED, entity.JobPhase_JOB_PHASE_COMPLETED, ""},
		{"finalizing", entity.JobStatus_COMPLETED, entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA, "deadline_exceeded"},
		{"media", entity.JobStatus_PENDING, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, "action_required"},
		{"retry", entity.JobStatus_INDEXING, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, "action_required"},
		{"timeout", entity.JobStatus_INDEXING, entity.JobPhase_JOB_PHASE_INDEXING, "deadline_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Return one stable state and record every public operation.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobReply, error) {
					return &entity.GetJobReply{Job: &entity.Job{Id: 42, Status: test.status, Phase: test.phase}}, nil
				}})
			}, nil, nil)

			// Waiting emits the final observation even when an operator action is needed.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "wait", "42", "--wait-timeout", "250ms", "--poll-interval", "100ms")
			require.Contains(t, stdout, `"id":"42"`)
			if test.code == "" {
				require.Equal(t, exitSuccess, exit, stderr)
			} else {
				require.Equal(t, exitFailure, exit)
				var output errorOutput
				require.NoError(t, json.Unmarshal([]byte(stderr), &output))
				require.Equal(t, test.code, output.Code)
			}
			require.Positive(t, recorder.count(entity.JobService_Get_FullMethodName))
			require.Len(t, recorder.calls, 1, "wait must not retry, cancel, format or select Media")
		})
	}
}

func TestJobWaitWaitsForRunnerCompletion(t *testing.T) {
	// The durable checkpoint can precede resource release and the runner's final phase.
	var observations atomic.Int64
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobReply, error) {
			job := &entity.Job{Id: 3, Status: entity.JobStatus_COMPLETED, Phase: entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA}
			if observations.Add(1) >= 3 {
				job.Phase = entity.JobPhase_JOB_PHASE_COMPLETED
			}
			return &entity.GetJobReply{Job: job}, nil
		}})
	}, nil, nil)

	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "wait", "3", "--wait-timeout", "2s", "--poll-interval", "100ms")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, int64(3), observations.Load())
	require.Contains(t, stdout, "JOB_PHASE_COMPLETED")
	require.Len(t, recorder.calls, 1, "wait must only observe the Job")
}

func TestJobWaitUsesSeparateOverallDeadline(t *testing.T) {
	// Each network request is fast, but completion takes longer than one request timeout.
	var observations atomic.Int64
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobReply, error) {
			job := &entity.Job{Id: 3, Status: entity.JobStatus_INDEXING, Phase: entity.JobPhase_JOB_PHASE_INDEXING}
			if observations.Add(1) >= 3 {
				job.Status = entity.JobStatus_COMPLETED
				job.Phase = entity.JobPhase_JOB_PHASE_COMPLETED
			}
			return &entity.GetJobReply{Job: job}, nil
		}})
	}, nil, nil)

	// Do not apply the short per-request budget to the whole wait loop.
	started := time.Now()
	exit, _, stderr := executeTestCLI(server.URL, "", "--timeout", "100ms", "job", "wait", "3", "--wait-timeout", "2s", "--poll-interval", "100ms")
	require.Equal(t, exitSuccess, exit, stderr)
	require.GreaterOrEqual(t, time.Since(started), 200*time.Millisecond)
}

func TestJobWaitRetainsObservationWhenPollingTimesOut(t *testing.T) {
	var observations atomic.Int64
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(ctx context.Context, _ *entity.GetJobRequest) (*entity.GetJobReply, error) {
			if observations.Add(1) == 1 {
				return &entity.GetJobReply{Job: &entity.Job{Id: 3, Status: entity.JobStatus_INDEXING, Phase: entity.JobPhase_JOB_PHASE_INDEXING}}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	}, nil, nil)

	// Expiry during an in-flight poll must preserve the same last observation as expiry between polls.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "wait", "3", "--wait-timeout", "1s", "--poll-interval", "100ms")
	require.Equal(t, exitFailure, exit)
	require.GreaterOrEqual(t, observations.Load(), int64(2))
	require.Contains(t, stdout, `"id":"3"`)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "deadline_exceeded", output.Code)
}

type stubSettingsService struct {
	entity.UnimplementedSettingsServiceServer
	updated *entity.UpdateLibrarySettingsRequest
	browsed *entity.BrowsePathsRequest
}

func (*stubSettingsService) GetLibrary(context.Context, *entity.GetLibrarySettingsRequest) (*entity.LibrarySettings, error) {
	return &entity.LibrarySettings{IncludeUnbackedFiles: true, Revision: 9}, nil
}

func (s *stubSettingsService) UpdateLibrary(_ context.Context, request *entity.UpdateLibrarySettingsRequest) (*entity.UpdateLibrarySettingsReply, error) {
	s.updated = request
	return &entity.UpdateLibrarySettingsReply{Settings: request.Settings}, nil
}

func (s *stubSettingsService) BrowsePaths(_ context.Context, request *entity.BrowsePathsRequest) (*entity.BrowsePathsReply, error) {
	s.browsed = request
	return &entity.BrowsePathsReply{}, nil
}

func TestSettingsCLIExplicitFalseAndScopedBrowsing(t *testing.T) {
	// Capture a false preference separately from an omitted mutation.
	settings := &stubSettingsService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "settings", "library")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Nil(t, settings.updated)
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "library", "--include-unbacked=false", "--revision", "9")
	require.Equal(t, exitSuccess, exit, stderr)
	require.False(t, settings.updated.Settings.IncludeUnbackedFiles)
	require.EqualValues(t, 9, settings.updated.Settings.Revision)

	// Preserve Location-relative target browsing and bounded page state.
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "browse", "--location-id", "7", "--path", "restored/2026", "--cursor", "cursor", "--limit", "4")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 7, settings.browsed.LocationId)
	require.Equal(t, "restored/2026", settings.browsed.Path)
	require.EqualValues(t, 4, settings.browsed.Limit)
	require.Equal(t, "cursor", settings.browsed.Cursor)
	require.Equal(t, 1, recorder.count(entity.SettingsService_UpdateLibrary_FullMethodName))
}

func TestRemovedRawSourceAndApplyCommandsAreNotAvailable(t *testing.T) {
	// Obsolete Draft workflows cannot silently reach the network.
	for _, args := range [][]string{{"source", "list", "/"}, {"scan", "apply", "1"}, {"archive", "create", "raw/path"}, {"preview", "create", "raw/path"}} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}

func TestScanResultsUseOneTypedService(t *testing.T) {
	// Every result is read from the same Scan manifest regardless of source or policy.
	syncJobs := &stubAnalyzeJobService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(_ context.Context, request *entity.GetJobRequest) (*entity.GetJobReply, error) {
			return &entity.GetJobReply{Job: &entity.Job{Id: request.Id, Kind: entity.JobKind_SCAN}}, nil
		}})
		entity.RegisterScanJobServiceServer(server, syncJobs)
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "scan", "results", "8", "--after-id", "9", "--limit", "3")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 8, syncJobs.page.Id)
	require.EqualValues(t, 9, syncJobs.page.GetAfterId())
	require.EqualValues(t, 3, syncJobs.page.Limit)
	require.Equal(t, 1, recorder.count(entity.ScanJobService_ListEntries_FullMethodName))
}
