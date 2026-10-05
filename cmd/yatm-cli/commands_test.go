package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestHelpUsesExecutableName(t *testing.T) {
	// Render root help through the production parser.
	var stdout bytes.Buffer
	exit := run([]string{"--help"}, nil, &stdout, new(bytes.Buffer))

	// Keep the installed executable name visible in the public interface.
	require.Equal(t, exitSuccess, exit)
	require.Contains(t, stdout.String(), "Usage:\n  yatm-cli [OPTIONS] <command>")
}

type stubService struct {
	entity.UnimplementedMediaServiceServer
	entity.UnimplementedLibraryServiceServer
	mediaList        func(context.Context, *entity.ListMediaRequest) (*entity.ListMediaResponse, error)
	mediaInspect     func(context.Context, *entity.InspectMediaRequest) (*entity.InspectMediaResponse, error)
	mediaDelete      func(context.Context, *entity.DeleteMediaRequest) (*entity.DeleteMediaResponse, error)
	volumeInitialize func(context.Context, *entity.InitializeVolumeRequest) (*entity.InitializeVolumeResponse, error)
	volumeRegister   func(context.Context, *entity.RegisterVolumeRequest) (*entity.RegisterVolumeResponse, error)
	deviceList       func(context.Context, *entity.ListDevicesRequest) (*entity.ListDevicesResponse, error)
	libraryTrim      func(context.Context, *entity.TrimLibraryRequest) (*entity.TrimLibraryResponse, error)
}

func (s *stubService) List(
	ctx context.Context,
	request *entity.ListMediaRequest,
) (*entity.ListMediaResponse, error) {
	if s.mediaList != nil {
		return s.mediaList(ctx, request)
	}
	return &entity.ListMediaResponse{}, nil
}

func (s *stubService) Inspect(
	ctx context.Context,
	request *entity.InspectMediaRequest,
) (*entity.InspectMediaResponse, error) {
	if s.mediaInspect != nil {
		return s.mediaInspect(ctx, request)
	}
	return &entity.InspectMediaResponse{}, nil
}

func (s *stubService) Delete(
	ctx context.Context,
	request *entity.DeleteMediaRequest,
) (*entity.DeleteMediaResponse, error) {
	if s.mediaDelete != nil {
		return s.mediaDelete(ctx, request)
	}
	return &entity.DeleteMediaResponse{}, nil
}

func (s *stubService) InitializeVolume(
	ctx context.Context,
	request *entity.InitializeVolumeRequest,
) (*entity.InitializeVolumeResponse, error) {
	if s.volumeInitialize != nil {
		return s.volumeInitialize(ctx, request)
	}
	return &entity.InitializeVolumeResponse{}, nil
}

func (s *stubService) RegisterVolume(
	ctx context.Context,
	request *entity.RegisterVolumeRequest,
) (*entity.RegisterVolumeResponse, error) {
	if s.volumeRegister != nil {
		return s.volumeRegister(ctx, request)
	}
	return &entity.RegisterVolumeResponse{}, nil
}

func (s *stubService) ListDevices(
	ctx context.Context,
	request *entity.ListDevicesRequest,
) (*entity.ListDevicesResponse, error) {
	if s.deviceList != nil {
		return s.deviceList(ctx, request)
	}
	return &entity.ListDevicesResponse{}, nil
}

func (s *stubService) Trim(
	ctx context.Context,
	request *entity.TrimLibraryRequest,
) (*entity.TrimLibraryResponse, error) {
	if s.libraryTrim != nil {
		return s.libraryTrim(ctx, request)
	}
	return &entity.TrimLibraryResponse{}, nil
}

func TestArchiveCreatePreservesSelectionsAndUsesProtoJSON(t *testing.T) {
	// Capture logical and physical selections without resolving raw filesystem paths.
	var created *entity.CreateArchiveJobRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{create: func(
			_ context.Context,
			request *entity.CreateArchiveJobRequest,
		) (*entity.CreateArchiveJobResponse, error) {
			created = proto.Clone(request).(*entity.CreateArchiveJobRequest)
			return &entity.CreateArchiveJobResponse{
				Job: &entity.Job{Id: 9_007_199_254_740_993},
			}, nil
		}})
	}, nil, nil)

	// Create the Job through the real parser and gRPC-Web transport.
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		"",
		"archive", "create", "--file-id", "12", "--location", "4:clip.mov", "--scope", "saved",
		"--priority", "7", "--preview-policy", "missing-only", "--force-rehash",
	)

	// Verify source scope, flags, and string-encoded 64-bit JSON fields.
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"job":{"id":"9007199254740993"}}`, stdout)
	require.Empty(t, stderr)
	require.Equal(t, 1, recorder.count(entity.ArchiveJobService_Create_FullMethodName))
	require.Equal(t, int64(7), created.Priority)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY, created.PreviewPolicy)
	require.True(t, created.ForceRehash)
	require.Len(t, created.Spec.Selections, 2)
	require.EqualValues(t, 12, created.Spec.Selections[0].GetLibrary().FileId)
	require.Equal(t, entity.FileScope_FILE_SCOPE_SAVED, created.Spec.Selections[0].Scope)
	require.EqualValues(t, 4, created.Spec.Selections[1].GetLocation().LocationId)
	require.Equal(t, "clip.mov", created.Spec.Selections[1].GetLocation().Path)
}

func TestListPaginationRequests(t *testing.T) {
	// Capture page controls across the three public pagination shapes.
	var mediaRequest *entity.ListMediaRequest
	var jobRequests []*entity.ListJobsRequest
	var scanRequest *entity.ListScanJobEntriesRequest
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{mediaList: func(
			_ context.Context,
			request *entity.ListMediaRequest,
		) (*entity.ListMediaResponse, error) {
			mediaRequest = proto.Clone(request).(*entity.ListMediaRequest)
			return &entity.ListMediaResponse{HasMore: true}, nil
		}})
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(_ context.Context, request *entity.GetJobRequest) (*entity.GetJobResponse, error) {
			return &entity.GetJobResponse{Job: &entity.Job{Id: request.Id, Kind: entity.JobKind_JOB_KIND_SCAN}}, nil
		}, list: func(
			_ context.Context,
			request *entity.ListJobsRequest,
		) (*entity.ListJobsResponse, error) {
			jobRequests = append(jobRequests, proto.Clone(request).(*entity.ListJobsRequest))
			return &entity.ListJobsResponse{}, nil
		}})
		entity.RegisterScanJobServiceServer(server, &stubScanJobService{listEntries: func(
			_ context.Context,
			request *entity.ListScanJobEntriesRequest,
		) (*entity.ListScanJobEntriesResponse, error) {
			scanRequest = proto.Clone(request).(*entity.ListScanJobEntriesRequest)
			return &entity.ListScanJobEntriesResponse{}, nil
		}})
	}, nil, nil)

	// Execute one bounded page request for each shape.
	commands := [][]string{
		{"media", "list", "--kind", "volume", "--limit", "5", "--offset", "2"},
		{"job", "list", "--limit", "4", "--before-id", "99", "--snapshot-revision", "12", "--location-id", "7", "--media-id", "8"},
		{"job", "changes", "--after-revision", "10", "--limit", "3"},
		{"scan", "results", "8", "--limit", "6", "--cursor", "9", "--order", "desc", "--include-total"},
	}
	for _, args := range commands {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitSuccess, exit, stderr)
	}

	// Preserve every caller-provided cursor and bound in the corresponding protobuf request.
	require.Equal(t, []entity.MediaKind{entity.MediaKind_MEDIA_KIND_VOLUME}, mediaRequest.GetList().Kinds)
	require.Equal(t, int64(5), *mediaRequest.GetList().Limit)
	require.Equal(t, int64(2), *mediaRequest.GetList().Offset)
	require.Len(t, jobRequests, 2)
	require.EqualValues(t, 7, jobRequests[0].Filter.GetLocationId())
	require.EqualValues(t, 8, jobRequests[0].Filter.GetMediaId())
	require.Equal(t, int64(4), *jobRequests[0].Filter.Limit)
	require.Equal(t, int64(99), *jobRequests[0].Filter.BeforeId)
	require.Equal(t, int64(12), *jobRequests[0].Filter.SnapshotRevision)
	require.Equal(t, int64(10), *jobRequests[1].Filter.ChangedAfterRevision)
	require.Equal(t, int64(3), *jobRequests[1].Filter.Limit)
	require.Equal(t, int64(8), scanRequest.Id)
	require.Equal(t, int32(6), scanRequest.Limit)
	require.EqualValues(t, "9", scanRequest.GetCursor())
	require.Equal(t, entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING, scanRequest.GetOrder())
	require.True(t, scanRequest.GetIncludeTotal())
}

func TestVolumeAndTapeDeviceCommands(t *testing.T) {
	// Capture both Volume mutations and serve one available Tape device.
	var initialized *entity.InitializeVolumeRequest
	var registered *entity.RegisterVolumeRequest
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{
			volumeInitialize: func(
				_ context.Context,
				request *entity.InitializeVolumeRequest,
			) (*entity.InitializeVolumeResponse, error) {
				initialized = proto.Clone(request).(*entity.InitializeVolumeRequest)
				return &entity.InitializeVolumeResponse{}, nil
			},
			volumeRegister: func(
				_ context.Context,
				request *entity.RegisterVolumeRequest,
			) (*entity.RegisterVolumeResponse, error) {
				registered = proto.Clone(request).(*entity.RegisterVolumeRequest)
				return &entity.RegisterVolumeResponse{}, nil
			},
			deviceList: func(
				context.Context,
				*entity.ListDevicesRequest,
			) (*entity.ListDevicesResponse, error) {
				return &entity.ListDevicesResponse{Devices: []string{"/dev/nst0"}}, nil
			},
		})
	}, nil, nil)

	// Initialize the mounted server path with its immutable Volume profile.
	exit, _, stderr := executeTestCLI(
		server.URL,
		"",
		"volume", "initialize", "/srv/volumes/disk-a",
		"--name", "Disk A", "--type", "hm-smr", "--serial-number", "SER-7",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "/srv/volumes/disk-a", initialized.MountPoint)
	require.Equal(t, "Disk A", initialized.Name)
	require.Equal(t, "SER-7", initialized.Profile.SerialNumber)
	require.Equal(t, entity.VolumeType_VOLUME_TYPE_HM_SMR, initialized.Profile.Type)

	// Register the same server path and preserve its user-facing name.
	exit, _, stderr = executeTestCLI(
		server.URL,
		"",
		"volume", "register", "/srv/volumes/disk-a", "--name", "Disk A",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "/srv/volumes/disk-a", registered.MountPoint)
	require.Equal(t, "Disk A", registered.Name)

	// Return configured, currently available Tape devices as ProtoJSON.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "list")
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"devices":["/dev/nst0"]}`, stdout)
}

func TestRPCErrorUsesConnectCode(t *testing.T) {
	// Return one typed gRPC NotFound error through the gRPC-Web wrapper.
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{get: func(
			context.Context,
			*entity.GetJobRequest,
		) (*entity.GetJobResponse, error) {
			return nil, status.Error(codes.NotFound, "missing Job")
		}})
	}, nil, nil)

	// Preserve the Connect code in the CLI error envelope.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "get", "41")
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "not_found", output.Code)
}

func TestJobLogReturnsTextAndNextOffset(t *testing.T) {
	// Serve a bounded binary log page and capture the requested byte offset.
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{getLog: func(
			_ context.Context,
			request *entity.GetJobLogRequest,
		) (*entity.GetJobLogResponse, error) {
			require.NotNil(t, request.Offset)
			require.Equal(t, int64(4), *request.Offset)
			return &entity.GetJobLogResponse{Logs: []byte("next line\n"), Offset: 14}, nil
		}})
	}, nil, nil)

	// Expose log bytes as text and keep the next offset string-safe for agents.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "log", "7", "--offset", "4")
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"logs":"next line\n","offset":"14"}`, stdout)
}

func TestInvalidArgumentsReturnUsageExit(t *testing.T) {
	// Cover validation decisions that must finish before any transport call.
	tests := []struct {
		name string
		args []string
	}{
		{name: "negative timeout", args: []string{"--timeout", "-1s", "status"}},
		{name: "invalid File ID", args: []string{"files", "get", "--file-id=-2"}},
		{name: "zero limit", args: []string{"media", "list", "--limit", "0"}},
		{name: "metadata root", args: []string{"files", "metadata", "--file-id", "0", "--note", "x"}},
		{name: "Preview option dependency", args: []string{"archive", "create", "a", "--force-rehash"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exit, stdout, stderr := executeTestCLI("http://127.0.0.1:1", "", test.args...)
			require.Equal(t, exitUsage, exit)
			require.Empty(t, stdout)
			var output errorOutput
			require.NoError(t, json.Unmarshal([]byte(stderr), &output))
			require.Equal(t, "usage", output.Code)
		})
	}
}

func registerTestServices(server *grpc.Server, service *stubService) {
	entity.RegisterMediaServiceServer(server, service)
	entity.RegisterLibraryServiceServer(server, service)
}
