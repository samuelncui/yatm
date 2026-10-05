package main

import (
	"bufio"
	"math"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type filesErrorServer struct {
	entity.UnimplementedFilesServiceServer
	failure error
}

func (s *filesErrorServer) List(_ *entity.ListFilesRequest, stream entity.FilesService_ListServer) error {
	// Separate batches establish that a failed child never ends consumption of usable later rows.
	total := int64(3)
	for index, row := range []*entity.FilesEntry{
		{Name: "before", Reference: locationReference(1, "before"), MtimeNs: proto.Int64(math.MinInt64)},
		{Name: `"bad-\xff"`, Path: `"bad-\xff"`, Error: "name is not valid UTF-8"},
		{Name: "after", Reference: locationReference(1, "after"), MtimeNs: proto.Int64(0)},
	} {
		reply := &entity.ListFilesResponse{Entries: []*entity.FilesEntry{row}}
		if index == 0 {
			reply.TotalEntryCount = &total
		}
		if err := stream.Send(reply); err != nil {
			return err
		}
	}
	return s.failure
}

func TestLSChildErrorsDrainStreamAndFail(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		code    string
	}{
		{name: "child errors", code: "incomplete"},
		{name: "transport error", failure: status.Error(codes.Unavailable, "directory transport lost"), code: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Use actual argument parsing and gRPC-web framing for the complete CLI read.
			server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterFilesServiceServer(server, &filesErrorServer{failure: test.failure})
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "--location-id", "1")
			require.NotEqual(t, exitSuccess, exit)
			require.Contains(t, stderr, `"code":"`+test.code+`"`)

			// Every row remains machine readable on stdout, including the row after the error.
			var rows []*entity.FilesEntry
			scanner := bufio.NewScanner(strings.NewReader(stdout))
			batches := 0
			for scanner.Scan() {
				var reply entity.ListFilesResponse
				require.NoError(t, protojson.Unmarshal(scanner.Bytes(), &reply))
				if batches == 0 {
					require.EqualValues(t, 3, reply.GetTotalEntryCount())
				}
				rows = append(rows, reply.Entries...)
				batches++
			}
			require.NoError(t, scanner.Err())
			require.Equal(t, 3, batches)
			require.Len(t, rows, 3)
			require.Equal(t, "before", rows[0].Name)
			require.Equal(t, proto.Int64(math.MinInt64), rows[0].MtimeNs)
			require.NotEmpty(t, rows[1].Error)
			require.Nil(t, rows[1].Reference)
			require.Nil(t, rows[1].MtimeNs)
			require.Equal(t, "after", rows[2].Name)
			require.Equal(t, proto.Int64(0), rows[2].MtimeNs)
		})
	}
}
