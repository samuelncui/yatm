package main

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestMkdirCLIPreservesLiteralUTF8Names(t *testing.T) {
	// Both namespaces must forward the exact component supplied by the shell.
	for _, library := range []bool{true, false} {
		for _, name := range []string{" ", "\t\n", " leading ", `back\slash`, "quote'\"\n照片"} {
			t.Run(fmt.Sprintf("library=%t/name=%q", library, name), func(t *testing.T) {
				// Capture the mutation after any physical destination observation.
				var calls atomic.Int64
				server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
					entity.RegisterFilesServiceServer(server, &fileOperationStub{
						execute: func(request *entity.FileOperationSpec, stream operationTestStream) error {
							calls.Add(1)
							require.Equal(t, entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR, request.Kind)
							require.Equal(t, name, request.Name)
							return stream.Send(&entity.FileOperationResult{Summary: &entity.FileOperationSummary{
								Completed: true, TotalItemCount: 1, SucceededCount: 1,
							}})
						},
					})
				}, nil, nil)

				// Whitespace is a name, including when every character is whitespace.
				args := []string{"mkdir", "--location", "4", "--destination", ".", "--name", name}
				if library {
					args = []string{"mkdir", "--library", "--destination", "0", "--name", name}
				}
				exit, _, stderr := executeTestCLI(server.URL, "", args...)
				require.Equal(t, exitSuccess, exit, stderr)
				require.EqualValues(t, 1, calls.Load())
			})
		}
	}
}
