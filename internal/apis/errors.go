package apis

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/fileops"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// statusCodes maps domain sentinels to the gRPC codes every service returns for them.
var statusCodes = []struct {
	err  error
	code codes.Code
}{
	{executor.ErrAccessExcluded, codes.PermissionDenied},
	{library.ErrLocationBusy, codes.FailedPrecondition},
	{library.ErrLocationConflict, codes.Aborted},
	{library.ErrLocationUnverified, codes.FailedPrecondition},
	{settingspkg.ErrInvalid, codes.InvalidArgument},
	{settingspkg.ErrStored, codes.FailedPrecondition},
	{gorm.ErrRecordNotFound, codes.NotFound},
	{library.ErrFileNotFound, codes.NotFound},
	{executor.ErrJobNotFound, codes.NotFound},
	{fileops.ErrKeepNotMember, codes.InvalidArgument},
}

// StatusError maps a handler error to its gRPC status. An error that already carries a status,
// directly or wrapped with %w, keeps it; an unknown error is returned unchanged.
func StatusError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	for _, mapping := range statusCodes {
		if errors.Is(err, mapping.err) {
			return status.Error(mapping.code, err.Error())
		}
	}
	return err
}

// UnaryServerInterceptor applies StatusError to every unary handler, including Job services.
func UnaryServerInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	reply, err := handler(ctx, req)
	return reply, StatusError(err)
}

// StreamServerInterceptor applies StatusError to every streaming handler.
func StreamServerInterceptor(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return StatusError(handler(srv, stream))
}
