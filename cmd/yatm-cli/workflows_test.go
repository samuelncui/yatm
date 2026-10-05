package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestJobWaitStopsWithoutTakingAction(t *testing.T) {
	// Exercise completion, intervention, attempt outcomes and timeout with observations only.
	for _, test := range []struct {
		name   string
		status entity.JobStatus
		phase  entity.JobPhase
		reason string
		code   string
	}{
		{"completed", entity.JobStatus_JOB_STATUS_COMPLETED, entity.JobPhase_JOB_PHASE_COMPLETED, "", ""},
		{"completed-without-runner", entity.JobStatus_JOB_STATUS_COMPLETED, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "", ""},
		{"finalizing", entity.JobStatus_JOB_STATUS_COMPLETED, entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA, "", "deadline_exceeded"},
		{"media", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "", "action_required"},
		{"media-failed", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "Media unavailable", "action_required"},
		{"queued", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_QUEUED, "", "deadline_exceeded"},
		{"queued-preparing", entity.JobStatus_JOB_STATUS_PREPARING, entity.JobPhase_JOB_PHASE_QUEUED, "", "deadline_exceeded"},
		// A settled failure ends the wait with its reason instead of running to the timeout.
		{"failed", entity.JobStatus_JOB_STATUS_FAILED, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "injected failure", "action_required"},
		{"preparing", entity.JobStatus_JOB_STATUS_PREPARING, entity.JobPhase_JOB_PHASE_INDEXING, "", "deadline_exceeded"},
		// A ready Job whose runner is working is executing, not waiting for its operator.
		{"working", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, "", "deadline_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Return one stable state and record every public operation.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobResponse, error) {
					return &entity.GetJobResponse{Job: &entity.Job{Id: 42, Status: test.status, Phase: test.phase, Error: test.reason}}, nil
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
				if test.reason != "" {
					require.Contains(t, output.Error, test.reason)
				}
			}
			require.Positive(t, recorder.count(entity.JobService_Get_FullMethodName))
			require.Len(t, recorder.calls, 1, "wait must not retry, cancel, format or select Media")
		})
	}
}

func TestJobWaitObservesQueuedAttemptSettlement(t *testing.T) {
	// A queued attempt keeps its admission until it completes or needs another operator action.
	for _, test := range []struct {
		name   string
		status entity.JobStatus
		phase  entity.JobPhase
		reason string
		code   string
	}{
		{"completed", entity.JobStatus_JOB_STATUS_COMPLETED, entity.JobPhase_JOB_PHASE_COMPLETED, "", ""},
		{"media-failed", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "Media unavailable", "action_required"},
		{"idle", entity.JobStatus_JOB_STATUS_READY, entity.JobPhase_JOB_PHASE_UNSPECIFIED, "", "action_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Keep the first two observations queued before exposing the settled state.
			var observations atomic.Int64
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobResponse, error) {
					job := &entity.Job{Id: 42, Status: entity.JobStatus_JOB_STATUS_READY, Phase: entity.JobPhase_JOB_PHASE_QUEUED}
					if observations.Add(1) >= 3 {
						job.Status, job.Phase, job.Error = test.status, test.phase, test.reason
					}
					return &entity.GetJobResponse{Job: job}, nil
				}})
			}, nil, nil)

			// Waiting reports the settled observation without performing any follow-up operation.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "wait", "42", "--wait-timeout", "2s", "--poll-interval", "100ms")
			require.Equal(t, int64(3), observations.Load())
			require.Contains(t, stdout, test.status.String())
			if test.code == "" {
				require.Equal(t, exitSuccess, exit, stderr)
			} else {
				require.Equal(t, exitFailure, exit)
				var output errorOutput
				require.NoError(t, json.Unmarshal([]byte(stderr), &output))
				require.Equal(t, test.code, output.Code)
				require.Contains(t, stderr, test.reason)
			}
			require.Equal(t, 3, recorder.count(entity.JobService_Get_FullMethodName))
			require.Len(t, recorder.calls, 1, "wait must only observe the Job")
		})
	}
}

func TestJobWaitWaitsForRunnerCompletion(t *testing.T) {
	// The durable checkpoint can precede resource release and the runner's final phase.
	var observations atomic.Int64
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobResponse, error) {
			job := &entity.Job{Id: 3, Status: entity.JobStatus_JOB_STATUS_COMPLETED, Phase: entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA}
			if observations.Add(1) >= 3 {
				job.Phase = entity.JobPhase_JOB_PHASE_COMPLETED
			}
			return &entity.GetJobResponse{Job: job}, nil
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
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(context.Context, *entity.GetJobRequest) (*entity.GetJobResponse, error) {
			job := &entity.Job{Id: 3, Status: entity.JobStatus_JOB_STATUS_PREPARING, Phase: entity.JobPhase_JOB_PHASE_INDEXING}
			if observations.Add(1) >= 3 {
				job.Status = entity.JobStatus_JOB_STATUS_COMPLETED
				job.Phase = entity.JobPhase_JOB_PHASE_COMPLETED
			}
			return &entity.GetJobResponse{Job: job}, nil
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
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(ctx context.Context, _ *entity.GetJobRequest) (*entity.GetJobResponse, error) {
			if observations.Add(1) == 1 {
				return &entity.GetJobResponse{Job: &entity.Job{Id: 3, Status: entity.JobStatus_JOB_STATUS_PREPARING, Phase: entity.JobPhase_JOB_PHASE_INDEXING}}, nil
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
	updated *entity.SettingsValue
	library *entity.LibrarySettings
	preview *entity.PreviewSettings
	job     *entity.JobSettings
	update  func(*entity.SettingsValue) (*entity.SettingsValue, error)
}

func (s *stubSettingsService) Get(_ context.Context, request *entity.GetSettingsRequest) (*entity.GetSettingsResponse, error) {
	switch request.GetGroup() {
	case entity.SettingsGroup_SETTINGS_GROUP_LIBRARY:
		value := s.library
		if value == nil {
			value = &entity.LibrarySettings{IncludeUnbackedFiles: true, ConfirmRemove: true}
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Library{Library: value}}}, nil
	case entity.SettingsGroup_SETTINGS_GROUP_PREVIEW:
		value := s.preview
		if value == nil {
			value = &entity.PreviewSettings{Concurrency: 2, TimeoutSeconds: 600, MaxInputPixels: 64_000_000}
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Preview{Preview: value}}}, nil
	case entity.SettingsGroup_SETTINGS_GROUP_JOB:
		value := s.job
		if value == nil {
			value = &entity.JobSettings{Execution: settingspkg.DefaultJobExecution()}
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: value}}}, nil
	default:
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{}}, nil
	}
}

func (s *stubSettingsService) Update(_ context.Context, request *entity.UpdateSettingsRequest) (*entity.UpdateSettingsResponse, error) {
	s.updated = request.Value
	if s.update != nil {
		value, err := s.update(request.Value)
		return &entity.UpdateSettingsResponse{Value: value}, err
	}
	return &entity.UpdateSettingsResponse{Value: request.Value}, nil
}

type stubSettingsLocationService struct {
	entity.UnimplementedLocationServiceServer
	browsed *entity.BrowsePathsRequest
}

func (s *stubSettingsLocationService) BrowsePaths(_ context.Context, request *entity.BrowsePathsRequest) (*entity.BrowsePathsResponse, error) {
	s.browsed = request
	return &entity.BrowsePathsResponse{}, nil
}

func TestSettingsCLIExplicitFalseAndScopedBrowsing(t *testing.T) {
	// Capture a false preference separately from an omitted mutation.
	settings, locations := &stubSettingsService{}, &stubSettingsLocationService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterSettingsServiceServer(server, settings)
		entity.RegisterLocationServiceServer(server, locations)
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "settings", "library")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Nil(t, settings.updated)
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "library", "--include-unbacked=false")
	require.Equal(t, exitSuccess, exit, stderr)
	require.False(t, settings.updated.GetLibrary().IncludeUnbackedFiles)

	// Administrator discovery uses absolute paths and bounded page state.
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "browse", "--path", "/restored/2026", "--cursor", "cursor", "--limit", "4")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "/restored/2026", locations.browsed.Path)
	require.EqualValues(t, 4, locations.browsed.Limit)
	require.Equal(t, "cursor", locations.browsed.Cursor)
	require.Equal(t, 1, recorder.count(entity.SettingsService_Update_FullMethodName))
	exit, _, _ = executeTestCLI(server.URL, "", "settings", "browse", "--location-id", "7")
	require.Equal(t, exitUsage, exit, "registered directories use ls, not administrator discovery")
}

func TestScanResultsUseOneTypedService(t *testing.T) {
	// Every result is read from the same Scan manifest regardless of source or policy.
	scanJobs := &stubAnalyzeJobService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(_ context.Context, request *entity.GetJobRequest) (*entity.GetJobResponse, error) {
			return &entity.GetJobResponse{Job: &entity.Job{Id: request.Id, Kind: entity.JobKind_JOB_KIND_SCAN}}, nil
		}})
		entity.RegisterScanJobServiceServer(server, scanJobs)
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "scan", "results", "8", "--cursor", "9", "--limit", "3")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 8, scanJobs.page.Id)
	require.EqualValues(t, "9", scanJobs.page.GetCursor())
	require.EqualValues(t, 3, scanJobs.page.Limit)
	require.Equal(t, 1, recorder.count(entity.ScanJobService_ListEntries_FullMethodName))
}
