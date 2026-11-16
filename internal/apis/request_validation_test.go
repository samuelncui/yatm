package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMalformedRequestsReturnInvalidArgument(t *testing.T) {
	// Each request is rejected before any Library, log or device access, so no resources are configured.
	api := apis.New(nil, executor.New(nil, nil, nil, executor.Paths{}, executor.Scripts{}, nil))
	ctx := context.Background()
	media := entity.NewMediaServiceClient(domainConnection(t, api))
	negative := int64(-1)

	for name, call := range map[string]func() error{
		"log lines cursor": func() error {
			_, err := api.ListLogLines(ctx, &entity.ListJobLogLinesRequest{Id: 1, Cursor: &negative,
				Direction: entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER})
			return apis.StatusError(err)
		},
		"log lines direction": func() error {
			_, err := api.ListLogLines(ctx, &entity.ListJobLogLinesRequest{Id: 1})
			return apis.StatusError(err)
		},
		"log lines level": func() error {
			_, err := api.ListLogLines(ctx, &entity.ListJobLogLinesRequest{Id: 1, Level: "loud",
				Direction: entity.JobLogDirection_JOB_LOG_DIRECTION_NEWER})
			return apis.StatusError(err)
		},
		"log offset": func() error {
			_, err := api.GetLog(ctx, &entity.GetJobLogRequest{Id: 1, Offset: &negative})
			return apis.StatusError(err)
		},
		"Media positions limit": func() error {
			_, err := media.ListPositions(ctx, &entity.ListMediaPositionsRequest{Id: 1, Limit: &negative})
			return err
		},
		"device write target": func() error {
			_, err := media.InspectDevice(ctx, &entity.InspectDeviceRequest{Device: "/dev/a",
				WriteTarget: &entity.ArchiveTapeTarget{Device: "/dev/b"}})
			return err
		},
		"inspect empty Tape device": func() error {
			_, err := media.Inspect(ctx, (&entity.InspectMediaTapeTarget{Device: " "}).Pack())
			return err
		},
		"inspect Volume UUID": func() error {
			_, err := media.Inspect(ctx, (&entity.InspectMediaVolumeTarget{Uuid: "not-a-uuid"}).Pack())
			return err
		},
		"Volume name": func() error {
			_, err := media.InitializeVolume(ctx, &entity.InitializeVolumeRequest{MountPoint: "/mnt/a", Name: "a/b"})
			return err
		},
		"Volume register name": func() error {
			_, err := media.RegisterVolume(ctx, &entity.RegisterVolumeRequest{MountPoint: "/mnt/a", Name: ".."})
			return err
		},
	} {
		require.Equal(t, codes.InvalidArgument, status.Code(call()), name)
	}
}
