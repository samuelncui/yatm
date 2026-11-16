package apis_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrorInterceptorsMapSentinelsAndKeepStatuses(t *testing.T) {
	// Unary and stream handlers share one mapping; existing statuses are never rewritten.
	created := status.Error(codes.OutOfRange, "created deep inside")
	tests := []struct {
		name    string
		err     error
		code    codes.Code
		message string
	}{
		{name: "sentinel", err: executor.ErrJobNotFound, code: codes.NotFound, message: executor.ErrJobNotFound.Error()},
		{name: "wrapped sentinel", err: fmt.Errorf("get job failed, id=7, %w", executor.ErrJobNotFound), code: codes.NotFound,
			message: "get job failed, id=7, job not found"},
		{name: "status", err: created, code: codes.OutOfRange, message: "created deep inside"},
		{name: "wrapped status", err: fmt.Errorf("refresh failed: %w", created), code: codes.OutOfRange},
		{name: "status beside sentinel", err: errors.Join(created, library.ErrLocationUnverified), code: codes.OutOfRange},
		{name: "unknown", err: errors.New("plain failure"), code: codes.Unknown, message: "plain failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, unary := apis.UnaryServerInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{},
				func(context.Context, any) (any, error) { return nil, test.err })
			stream := apis.StreamServerInterceptor(nil, nil, &grpc.StreamServerInfo{},
				func(any, grpc.ServerStream) error { return test.err })
			for _, err := range []error{unary, stream} {
				require.Equal(t, test.code, status.Code(err))
				if test.message != "" {
					require.Equal(t, test.message, status.Convert(err).Message())
				}
			}
		})
	}
	_, err := apis.UnaryServerInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil })
	require.NoError(t, err)
}
