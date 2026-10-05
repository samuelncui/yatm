package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestMutatingCommandsReachTransportWithoutLocalConfirmation(t *testing.T) {
	// A bare invocation writes; the request itself carries the dry-run decision.
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		output.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	// These mutating commands reach transport without a local confirmation gate.
	tests := []struct {
		name string
		args []string
	}{
		{name: "File remove", args: []string{"rm", "--library", "--source", "1"}},
		{name: "File move", args: []string{"mv", "--library", "--source", "1", "--destination", "0"}},
		{name: "Library trim", args: []string{"library", "trim", "--files"}},
		{name: "Media delete", args: []string{"media", "delete", "1"}},
		{name: "Job delete", args: []string{"job", "delete", "1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requests.Load()
			exit, _, _ := executeTestCLI(server.URL, "", test.args...)
			require.Equal(t, exitFailure, exit)
			require.Greater(t, requests.Load(), before, "the command must not stop at a local gate")
		})
	}
}

// TestTapeFormatStillRequiresTheInspectedBarcode keeps the physical-write gate that is not a
// dry-run decision: FORMAT must match the barcode returned by the inspection.
func TestTapeFormatStillRequiresTheInspectedBarcode(t *testing.T) {
	var requests atomic.Int64
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{mediaInspect: func(
			_ context.Context,
			_ *entity.InspectMediaRequest,
		) (*entity.InspectMediaResponse, error) {
			requests.Add(1)
			return &entity.InspectMediaResponse{Identity: "ABC123"}, nil
		}})
	}, nil, nil)

	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "write", "tape", "format", "1",
		"--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tape A")
	require.Equal(t, exitUsage, exit)
	require.Contains(t, stderr, "confirm-format")
}

func TestFileRemoveRetainsAllTargetsAndMutatesOnce(t *testing.T) {
	// Capture the operation submitted to the streamed executor.
	var operation *entity.FileOperationSpec
	var executions atomic.Int64
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, &fileOperationStub{execute: func(request *entity.FileOperationSpec, stream operationTestStream) error {
			executions.Add(1)
			operation = proto.Clone(request).(*entity.FileOperationSpec)
			return stream.Send(&entity.FileOperationResult{Summary: &entity.FileOperationSummary{Completed: true, TotalItemCount: 2, SucceededCount: 2}})
		}})
	}, nil, nil)

	// The common server planner owns deduplication; CLI does not silently discard selections.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "rm", "--library", "--source", "7", "--source", "8", "--source", "7")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"completed":true`)
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
	require.EqualValues(t, 1, executions.Load())
	require.Equal(t, entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, operation.Kind)
	require.Len(t, operation.Sources, 3)
	require.EqualValues(t, 7, operation.Sources[0].GetFileId())
	require.EqualValues(t, 8, operation.Sources[1].GetFileId())
}

func TestMediaAndJobDeleteValidateTargetsAndMutateOnce(t *testing.T) {
	// Return every requested target from each read-only preflight.
	var mediaDeleted *entity.DeleteMediaRequest
	var jobsDeleted *entity.DeleteJobsRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{
			mediaList: func(
				_ context.Context,
				request *entity.ListMediaRequest,
			) (*entity.ListMediaResponse, error) {
				media := make([]*entity.Media, 0, len(request.GetIds().Ids))
				for _, id := range request.GetIds().Ids {
					media = append(media, &entity.Media{Id: id})
				}
				return &entity.ListMediaResponse{Media: media}, nil
			},
			mediaDelete: func(
				_ context.Context,
				request *entity.DeleteMediaRequest,
			) (*entity.DeleteMediaResponse, error) {
				mediaDeleted = proto.Clone(request).(*entity.DeleteMediaRequest)
				return &entity.DeleteMediaResponse{}, nil
			},
		})
		entity.RegisterJobServiceServer(server, &stubJobService{
			get: func(
				_ context.Context,
				request *entity.GetJobRequest,
			) (*entity.GetJobResponse, error) {
				return &entity.GetJobResponse{Job: &entity.Job{Id: request.Id}}, nil
			},
			delete: func(
				_ context.Context,
				request *entity.DeleteJobsRequest,
			) (*entity.DeleteJobsResponse, error) {
				jobsDeleted = proto.Clone(request).(*entity.DeleteJobsRequest)
				return &entity.DeleteJobsResponse{}, nil
			},
		})
	}, nil, nil)

	// Delete the resolved Media set with one MGet and one mutation.
	exit, _, stderr := executeTestCLI(server.URL, "", "media", "delete", "3", "4")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{3, 4}, mediaDeleted.Ids)
	require.Equal(t, 1, recorder.count(entity.MediaService_List_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.MediaService_Delete_FullMethodName))

	require.False(t, mediaDeleted.Dryrun)

	// Delete the resolved Job set after one lookup per retained Job.
	exit, _, stderr = executeTestCLI(server.URL, "", "job", "delete", "5", "6")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{5, 6}, jobsDeleted.Ids)
	require.False(t, jobsDeleted.Dryrun)
	require.Equal(t, 2, recorder.count(entity.JobService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.JobService_Delete_FullMethodName))

	// A dry run carries the same resolved targets and marks the request instead of changing it.
	exit, _, stderr = executeTestCLI(server.URL, "", "media", "delete", "3", "--dryrun")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{3}, mediaDeleted.Ids)
	require.True(t, mediaDeleted.Dryrun)

	exit, _, stderr = executeTestCLI(server.URL, "", "job", "delete", "5", "--dryrun")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{5}, jobsDeleted.Ids)
	require.True(t, jobsDeleted.Dryrun)
}

func TestTapeWriteInspectsBarcodeBeforeMutation(t *testing.T) {
	// Return physical identities and one registered append-compatible Tape profile.
	var writes []*entity.WriteArchiveMediaRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{mediaInspect: func(
			_ context.Context,
			request *entity.InspectMediaRequest,
		) (*entity.InspectMediaResponse, error) {
			switch request.GetIdentity() {
			case "ABC123":
				return &entity.InspectMediaResponse{Identity: "ABC123"}, nil
			case "XYZ789":
				return &entity.InspectMediaResponse{
					Identity: "XYZ789",
					Media: &entity.Media{
						Id:      8,
						Profile: (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack(),
					},
				}, nil
			default:
				return &entity.InspectMediaResponse{Identity: "ABC123"}, nil
			}
		}})
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(
			_ context.Context,
			request *entity.WriteArchiveMediaRequest,
		) (*entity.WriteArchiveMediaResponse, error) {
			writes = append(writes, proto.Clone(request).(*entity.WriteArchiveMediaRequest))
			return &entity.WriteArchiveMediaResponse{}, nil
		}})
	}, nil, nil)

	// FORMAT binds the confirmation to the barcode returned by the physical inspection.
	exit, _, stderr := executeTestCLI(
		server.URL,
		"",
		"archive", "write", "tape", "format", "31",
		"--device", " /dev/nst0 ", "--barcode", "ABC123", "--name", "Tape A",
		"--confirm-format", "ABC123",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Len(t, writes, 1)
	formatTarget := writes[0].Target.GetTape()
	require.Equal(t, "/dev/nst0", formatTarget.Device)
	require.Equal(t, "ABC123", formatTarget.Barcode)
	require.Equal(t, "Tape A", formatTarget.Name)
	require.Equal(t, entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, formatTarget.Mode)

	// Append uses the inspected, registered ltfs_v1 Media profile.
	exit, _, stderr = executeTestCLI(
		server.URL,
		"",
		"archive", "write", "tape", "append", "31",
		"--device", "/dev/nst0", "--barcode", "XYZ789",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Len(t, writes, 2)
	appendTarget := writes[1].Target.GetTape()
	require.Equal(t, "XYZ789", appendTarget.Barcode)
	require.Equal(t, entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND, appendTarget.Mode)

	// The confirmation must match the identity returned by that inspection.
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		"",
		"archive", "write", "tape", "format", "31",
		"--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tape B",
		"--confirm-format", "WRONG1",
	)
	require.Equal(t, exitUsage, exit)
	require.Empty(t, stdout)
	require.Len(t, writes, 2)

	// A mismatched physical barcode stops before WriteMedia.
	exit, stdout, stderr = executeTestCLI(
		server.URL,
		"",
		"archive", "write", "tape", "format", "31",
		"--device", "/dev/nst0", "--barcode", "MNO456", "--name", "Tape B",
		"--confirm-format", "MNO456",
	)
	require.Equal(t, exitUsage, exit)
	require.Empty(t, stdout)
	require.Len(t, writes, 2)
	require.Equal(t, 4, recorder.count(entity.MediaService_Inspect_FullMethodName))
	require.Equal(t, 2, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
}
