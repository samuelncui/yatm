package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type archiveCreationRPC struct {
	entity.UnimplementedArchiveJobServiceServer
	reply *entity.GetArchiveJobCreationResponse
}

func (s *archiveCreationRPC) GetCreation(
	_ context.Context, req *entity.GetArchiveJobCreationRequest,
) (*entity.GetArchiveJobCreationResponse, error) {
	reply := proto.Clone(s.reply).(*entity.GetArchiveJobCreationResponse)
	reply.Request.Priority = req.Id
	return reply, nil
}

type restoreCreationRPC struct {
	entity.UnimplementedRestoreJobServiceServer
	reply *entity.GetRestoreJobCreationResponse
}

func (s *restoreCreationRPC) GetCreation(
	_ context.Context, req *entity.GetRestoreJobCreationRequest,
) (*entity.GetRestoreJobCreationResponse, error) {
	reply := proto.Clone(s.reply).(*entity.GetRestoreJobCreationResponse)
	reply.Request.Priority = req.Id
	return reply, nil
}

type scanCreationRPC struct {
	entity.UnimplementedScanJobServiceServer
	reply *entity.GetScanJobCreationResponse
}

func (s *scanCreationRPC) GetCreation(
	_ context.Context, req *entity.GetScanJobCreationRequest,
) (*entity.GetScanJobCreationResponse, error) {
	reply := proto.Clone(s.reply).(*entity.GetScanJobCreationResponse)
	reply.Request.Priority = req.Id
	return reply, nil
}

func TestJobCreationCommandsReadTypedInputsWithoutCreating(t *testing.T) {
	// Echo the requested Job ID as priority to verify exact 64-bit request/response transport.
	const id = int64(9_007_199_254_740_993)
	archive := &entity.GetArchiveJobCreationResponse{Request: &entity.CreateArchiveJobRequest{
		Priority: id, ForceRehash: true, PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL,
		Spec: &entity.ArchiveJobSpec{Selections: []*entity.FileSelection{{
			Target: &entity.FileSelection_Location{
				Location: &entity.LocationSelection{LocationId: 13, Path: "selected/file"},
			},
			Scope: entity.FileScope_FILE_SCOPE_ALL,
		}}},
	}}
	restore := &entity.GetRestoreJobCreationResponse{Request: &entity.CreateRestoreJobRequest{
		Priority: id, Spec: &entity.RestoreJobSpec{
			FileVersionIds: []int64{31, 32}, Destination: &entity.RestoreDestination{LocationId: 41, Path: "output"},
			VersionPolicy:      &entity.RestoreVersionPolicy{BeforeAtNs: proto.Int64(id)},
			AllowDamagedCopies: true, SkipUnmatchedVersions: true,
		},
	}}
	scan := &entity.GetScanJobCreationResponse{
		Request: &entity.CreateScanJobRequest{Priority: id, Spec: &entity.ScanJobSpec{
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
			PreviewPolicy:   entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY,
		}},
		UnavailableReason: "Original sources are unavailable; select them again.",
	}

	// Each command has only its typed read service available, so lookup, Create and execution cannot be hidden follow-ups.
	for _, test := range []struct {
		domain   string
		method   string
		reply    proto.Message
		register func(*grpc.Server)
	}{
		{domain: "archive", method: entity.ArchiveJobService_GetCreation_FullMethodName, reply: archive,
			register: func(server *grpc.Server) {
				entity.RegisterArchiveJobServiceServer(server, &archiveCreationRPC{reply: archive})
			}},
		{domain: "restore", method: entity.RestoreJobService_GetCreation_FullMethodName, reply: restore,
			register: func(server *grpc.Server) {
				entity.RegisterRestoreJobServiceServer(server, &restoreCreationRPC{reply: restore})
			}},
		{domain: "scan", method: entity.ScanJobService_GetCreation_FullMethodName, reply: scan,
			register: func(server *grpc.Server) {
				entity.RegisterScanJobServiceServer(server, &scanCreationRPC{reply: scan})
			}},
	} {
		t.Run(test.domain, func(t *testing.T) {
			// Use the production parser and gRPC-Web transport, retaining incomplete-input explanations in JSON.
			server, recorder := newGRPCWebTestServer(t, test.register, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", test.domain, "creation", "9007199254740993")
			require.Equal(t, exitSuccess, exit, stderr)
			require.Empty(t, stderr)
			decoded := test.reply.ProtoReflect().New().Interface()
			require.NoError(t, protojson.Unmarshal([]byte(stdout), decoded))
			require.True(t, proto.Equal(test.reply, decoded), "%s", stdout)
			require.Contains(t, stdout, `"9007199254740993"`)
			if test.domain == "restore" {
				var output map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &output))
				policy := output["request"].(map[string]any)["spec"].(map[string]any)["version_policy"].(map[string]any)
				require.Equal(t, "9007199254740993", policy["before_at_ns"])
				require.NotContains(t, policy, "before_at_ms")
			}
			recorder.mu.Lock()
			calls := make(map[string]int, len(recorder.calls))
			for method, count := range recorder.calls {
				calls[method] = count
			}
			recorder.mu.Unlock()
			require.Equal(t, map[string]int{test.method: 1}, calls)

			// Bad IDs are rejected locally without another read or any creation request.
			exit, stdout, stderr = executeTestCLI(server.URL, "", test.domain, "creation", "0")
			require.Equal(t, exitUsage, exit, stderr)
			require.Empty(t, stdout)
			require.Contains(t, stderr, "positive")
			require.Equal(t, 1, recorder.count(test.method))
		})
	}
}
