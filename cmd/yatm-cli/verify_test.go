package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type verifyCommandServer struct {
	entity.UnimplementedScanJobServiceServer
	create  atomic.Pointer[entity.CreateScanJobRequest]
	run     atomic.Pointer[entity.ReadScanMediaRequest]
	entries atomic.Pointer[entity.ListScanJobEntriesRequest]
}

func (s *verifyCommandServer) Create(_ context.Context, request *entity.CreateScanJobRequest) (*entity.CreateScanJobReply, error) {
	s.create.Store(request)
	return &entity.CreateScanJobReply{Job: &entity.Job{Id: 3, Kind: entity.JobKind_SCAN}}, nil
}

func (s *verifyCommandServer) ReadMedia(_ context.Context, request *entity.ReadScanMediaRequest) (*entity.ReadScanMediaReply, error) {
	s.run.Store(request)
	return &entity.ReadScanMediaReply{}, nil
}

func (s *verifyCommandServer) ListEntries(_ context.Context, request *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesReply, error) {
	s.entries.Store(request)
	return &entity.ListScanJobEntriesReply{Entries: []*entity.ScanEntry{{Id: 5, Finding: entity.ScanFinding_MISMATCH}}}, nil
}

func TestVerifyCommandsPreserveExplicitMediaAndPagedFindings(t *testing.T) {
	// Exercise the shipped parser and transport without starting any physical operation.
	service := &verifyCommandServer{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterScanJobServiceServer(server, service)
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "verify", "create", "7", "--priority", "2")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 7, service.create.Load().Spec.MediaId)
	require.EqualValues(t, 2, service.create.Load().Priority)
	require.Equal(t, entity.ScanSignaturePolicy_FORCE_READ, service.create.Load().Spec.SignaturePolicy)
	require.Equal(t, entity.ScanResultPolicy_VERIFY_COPIES, service.create.Load().Spec.ResultPolicy)
	exit, _, stderr = executeTestCLI(server.URL, "", "verify", "run", "3", "--uuid", "139a1d3d-a54a-4b9b-8ee9-9f103f01d6b6")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 3, service.run.Load().Id)
	require.Equal(t, "139a1d3d-a54a-4b9b-8ee9-9f103f01d6b6", service.run.Load().Target.GetVolume().Uuid)
	require.Empty(t, service.run.Load().Target.ExpectedIdentity)
	exit, stdout, stderr := executeTestCLI(server.URL, "", "verify", "entries", "3", "--limit", "2", "--after-id", "4")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "MISMATCH")
	require.EqualValues(t, 2, service.entries.Load().Limit)
	require.EqualValues(t, 4, service.entries.Load().GetAfterId())
}

func TestVerifyRejectsAmbiguousTargetsBeforeTransport(t *testing.T) {
	for _, args := range [][]string{
		{"verify", "run", "3"},
		{"verify", "run", "3", "--uuid", "invalid", "--device", "/dev/example"},
		{"verify", "entries", "3", "--limit", "1001"},
		{"verify", "create", "0"},
	} {
		exit, _, _ := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, args)
	}
}
