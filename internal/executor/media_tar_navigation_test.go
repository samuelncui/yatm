package executor

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarFactoryNavigationFallbackAndIssuedFailures(t *testing.T) {
	positionErr, locateErr := errors.New("fixture position failed"), errors.New("fixture LOCATE failed")
	for _, tc := range []struct {
		name      string
		prepare   func(*tarDeviceFixture)
		runtime   func(*tarDeviceFixture)
		anchors   bool
		locates   int
		wantError error
	}{
		{
			name: "unknown pair", prepare: func(f *tarDeviceFixture) {
				value := byte(0)
				f.navigationSupport = &value
			},
		},
		{
			name: "unsupported pair", prepare: func(f *tarDeviceFixture) {
				value := byte(1)
				f.navigationSupport = &value
			},
		},
		{
			name:    "reported pair contradicts rejected position",
			prepare: func(f *tarDeviceFixture) { f.positionUnsupported = true },
		},
		{
			name: "unknown write position", prepare: func(f *tarDeviceFixture) { f.positionUnknown = true },
		},
		{
			name: "fresh unknown position", anchors: true,
			runtime: func(f *tarDeviceFixture) { f.positionUnknown = true },
		},
		{
			name: "fresh unsupported position", anchors: true,
			runtime: func(f *tarDeviceFixture) { f.positionUnsupported = true },
		},
		{
			name: "fresh native position failure", anchors: true, wantError: positionErr,
			runtime: func(f *tarDeviceFixture) { f.positionError = positionErr },
		},
		{
			name: "fresh contradictory position", anchors: true, wantError: tape.ErrMalformed,
			runtime: func(f *tarDeviceFixture) { f.positionBadBOP = true },
		},
		{
			name: "issued LOCATE failure", anchors: true, locates: 1, wantError: locateErr,
			runtime: func(f *tarDeviceFixture) { f.locateError = locateErr },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Create an ordinary completed archive; optional discovery cannot deny baseline record access.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			// Keep this fault matrix on the unknown-cursor path. With bootstrap timings the
			// prepared first record is already known and no new position/LOCATE command is needed.
			_, err := exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
				Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					ReadParameters: &entity.TapeReadParameters{SourceReference: "Explicit incomplete inputs for unknown-cursor fault checks"},
				}},
			}}})
			require.NoError(t, err)
			if tc.prepare != nil {
				tc.prepare(fixture)
			}
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			data := []byte("standard logical-object navigation with sequential fallback")
			copied := tarWriteFixtureMember(t, write, "data.bin", data)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			var member *mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
			require.NotNil(t, member)
			require.Equal(t, tc.anchors, member.Storage.Metadata.GetTar().Header != nil)
			require.Equal(t, tc.anchors, member.Storage.Metadata.GetTar().End != nil)

			// A fresh descriptor validates the same frozen identity/profile before consuming the stored locator.
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
			})
			require.NoError(t, err)
			target := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(stored.ID)}).Pack()
			target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
			read, err := backend.NewReadSession(ctx, target)
			require.NoError(t, err)
			if tc.runtime != nil {
				tc.runtime(fixture)
			}
			source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
			require.NoError(t, err)

			// Missing optional evidence uses physical filemarks; issued failures retain their cause and stop the lane.
			file, err := source.Content.Open(ctx)
			if tc.wantError != nil {
				require.Nil(t, file)
				require.ErrorIs(t, err, tc.wantError)
				require.ErrorIs(t, err, mediapkg.ErrReadUnavailable)
				queries := len(fixture.positionCommands)
				fixture.positionError, fixture.locateError = nil, nil
				_, err = source.Content.Open(ctx)
				require.ErrorIs(t, err, mediapkg.ErrReadUnavailable)
				require.Len(t, fixture.positionCommands, queries)
			} else {
				require.NoError(t, err)
				actual, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, data, actual)
				hash := sha256.Sum256(actual)
				require.Equal(t, copied.SHA256, hash[:])
			}
			require.Len(t, fixture.locateCommands, tc.locates)
			require.NoError(t, read.Finalize(ctx, nil))
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

func TestTarFactoryCompletedPositionControlsOptionalAnchors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*tarDeviceFixture)
		failed bool
	}{
		{"GOOD unknown", func(f *tarDeviceFixture) { f.positionUnknown = true }, false},
		{"clean unsupported", func(f *tarDeviceFixture) { f.positionUnsupported = true }, false},
		{"contradictory accepted count", func(f *tarDeviceFixture) { f.positionDelta = 1 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Freeze an exact start and physically accepted member before changing the final observation.
			ctx := context.Background()
			_, backend, fixture, _ := tarFactoryFixture(t)
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			tarWriteFixtureMember(t, write, "data.bin", []byte("completed content"))
			tc.inject(fixture)

			// Unknown optional evidence withdraws both anchors; a real count contradiction cannot publish success.
			result, err := write.Finalize(ctx, nil)
			if tc.failed {
				require.Nil(t, result)
				require.ErrorContains(t, err, "completed position contradicts accepted records and filemark")
			} else {
				require.NoError(t, err)
				var count int
				require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error {
					count++
					require.Nil(t, value.Storage.Metadata.GetTar().Header)
					require.Nil(t, value.Storage.Metadata.GetTar().End)
					return nil
				}))
				require.Equal(t, 1, count)
			}
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}
