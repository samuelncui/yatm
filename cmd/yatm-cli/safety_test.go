package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestHighRiskCommandsRequireConfirmationBeforeRequests(t *testing.T) {
	// Count every request that could cross the confirmation boundary.
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		output.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	// Exercise every high-risk command without its required confirmation.
	tests := []struct {
		name string
		args []string
	}{
		{name: "File delete", args: []string{"file", "delete", "1"}},
		{name: "Media delete", args: []string{"media", "delete", "1"}},
		{name: "Job delete", args: []string{"job", "delete", "1"}},
		{name: "Library import", args: []string{"library", "import", "--input", "-"}},
		{name: "Library trim", args: []string{"library", "trim", "--files"}},
		{
			name: "Tape FORMAT",
			args: []string{
				"archive", "write", "tape", "format", "1",
				"--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tape A",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requests.Load()
			exit, stdout, stderr := executeTestCLI(server.URL, "", test.args...)
			require.Equal(t, exitUsage, exit)
			require.Empty(t, stdout)
			var output errorOutput
			require.NoError(t, json.Unmarshal([]byte(stderr), &output))
			require.Contains(t, []string{"safety", "usage"}, output.Code)
			require.Equal(t, before, requests.Load())
		})
	}
}

func TestFileDeleteValidatesEveryTargetAndMutatesOnce(t *testing.T) {
	// Resolve all requested Files and capture the eventual delete request.
	var deleted *entity.ExecuteFileOperationRequest
	var executions atomic.Int64
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{
			fileGet: func(
				_ context.Context,
				request *entity.FileGetRequest,
			) (*entity.FileGetReply, error) {
				return &entity.FileGetReply{File: &entity.File{Id: request.Id}}, nil
			},
		})
		entity.RegisterFileOperationServiceServer(server, &fileOperationStub{execute: func(request *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
			executions.Add(1)
			deleted = proto.Clone(request).(*entity.ExecuteFileOperationRequest)
			return stream.Send(&entity.FileOperationUpdate{Summary: &entity.FileOperationSummary{Completed: true, TotalItems: 2, Succeeded: 2}})
		}})
	}, nil, nil)

	// Deduplicate targets while retaining one read per effective ID and one mutation.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "file", "delete", "7", "8", "7", "--confirm")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"completed":true`)
	require.Equal(t, 2, recorder.count(entity.Service_FileGet_FullMethodName))
	require.EqualValues(t, 1, executions.Load())
	require.Equal(t, entity.FileOperationKind_DELETE, deleted.Spec.Kind)
	require.True(t, deleted.ConfirmDelete)
	require.Len(t, deleted.Spec.Sources, 2)
	require.EqualValues(t, 7, deleted.Spec.Sources[0].GetFileId())
	require.EqualValues(t, 8, deleted.Spec.Sources[1].GetFileId())
}

func TestMediaAndJobDeleteValidateTargetsAndMutateOnce(t *testing.T) {
	// Return every requested target from each read-only preflight.
	var mediaDeleted *entity.MediaDeleteRequest
	var jobsDeleted *entity.DeleteJobsRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{
			mediaList: func(
				_ context.Context,
				request *entity.MediaListRequest,
			) (*entity.MediaListReply, error) {
				media := make([]*entity.Media, 0, len(request.GetMget().Ids))
				for _, id := range request.GetMget().Ids {
					media = append(media, &entity.Media{Id: id})
				}
				return &entity.MediaListReply{Media: media}, nil
			},
			mediaDelete: func(
				_ context.Context,
				request *entity.MediaDeleteRequest,
			) (*entity.MediaDeleteReply, error) {
				mediaDeleted = proto.Clone(request).(*entity.MediaDeleteRequest)
				return &entity.MediaDeleteReply{}, nil
			},
		})
		entity.RegisterJobServiceServer(server, &stubJobService{
			get: func(
				_ context.Context,
				request *entity.GetJobRequest,
			) (*entity.GetJobReply, error) {
				return &entity.GetJobReply{Job: &entity.Job{Id: request.Id}}, nil
			},
			delete: func(
				_ context.Context,
				request *entity.DeleteJobsRequest,
			) (*entity.DeleteJobsReply, error) {
				jobsDeleted = proto.Clone(request).(*entity.DeleteJobsRequest)
				return &entity.DeleteJobsReply{}, nil
			},
		})
	}, nil, nil)

	// Delete the resolved Media set with one MGet and one mutation.
	exit, _, stderr := executeTestCLI(server.URL, "", "media", "delete", "3", "4", "--confirm")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{3, 4}, mediaDeleted.Ids)
	require.Equal(t, 1, recorder.count(entity.Service_MediaList_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.Service_MediaDelete_FullMethodName))

	// Delete the resolved Job set after one lookup per retained Job.
	exit, _, stderr = executeTestCLI(server.URL, "", "job", "delete", "5", "6", "--confirm")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{5, 6}, jobsDeleted.Ids)
	require.Equal(t, 2, recorder.count(entity.JobService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.JobService_Delete_FullMethodName))
}

func TestTapeWriteInspectsBarcodeBeforeMutation(t *testing.T) {
	// Return physical identities and one registered append-compatible Tape profile.
	var writes []*entity.WriteArchiveMediaRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{mediaInspect: func(
			_ context.Context,
			request *entity.MediaInspectRequest,
		) (*entity.MediaInspectReply, error) {
			switch request.GetIdentity() {
			case "ABC123":
				return &entity.MediaInspectReply{Identity: "ABC123"}, nil
			case "XYZ789":
				return &entity.MediaInspectReply{
					Identity: "XYZ789",
					Media: &entity.Media{
						Id:      8,
						Profile: (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack(),
					},
				}, nil
			default:
				return &entity.MediaInspectReply{Identity: "ABC123"}, nil
			}
		}})
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(
			_ context.Context,
			request *entity.WriteArchiveMediaRequest,
		) (*entity.WriteArchiveMediaReply, error) {
			writes = append(writes, proto.Clone(request).(*entity.WriteArchiveMediaRequest))
			return &entity.WriteArchiveMediaReply{}, nil
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
	require.Equal(t, 4, recorder.count(entity.Service_MediaInspect_FullMethodName))
	require.Equal(t, 2, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
}
