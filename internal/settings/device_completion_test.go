package settings

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestDeviceReadCompletionPreservesPartialSettingsAndJSON(t *testing.T) {
	for name, completion := range map[string]*entity.TapeReadCompletion{
		"absent":          nil,
		"empty":           {},
		"full span only":  {FullSpanSeconds: proto.Float64(107)},
		"fixed zero only": {FixedSeconds: proto.Float64(0)},
		"complete":        {FullSpanSeconds: proto.Float64(107), FixedSeconds: proto.Float64(3.5)},
	} {
		t.Run(name, func(t *testing.T) {
			// Partial inputs survive the real Settings replacement and protobuf JSON storage boundary.
			module := testModule(t)
			ctx := context.Background()
			value := deviceTestValue()
			value.Devices[0].GetTape().ReadParameters = &entity.TapeReadParameters{Completion: completion}
			want := proto.Clone(value).(*entity.DeviceSettings)
			saved, err := module.Device.Save(ctx, value)
			require.NoError(t, err)
			var document row
			require.NoError(t, module.db.First(&document, "key = ?", "devices").Error)
			var decoded entity.DeviceSettings
			require.NoError(t, protojson.Unmarshal(document.Value, &decoded))
			require.True(t, proto.Equal(want, &decoded), "unknown fields must remain absent rather than become zero")
			if completion != nil {
				for field, supplied := range map[string]*float64{
					"fullSpanSeconds": completion.FullSpanSeconds, "fixedSeconds": completion.FixedSeconds,
				} {
					if supplied == nil {
						require.NotContains(t, string(document.Value), `"`+field+`"`)
					} else {
						require.Contains(t, string(document.Value), `"`+field+`"`)
					}
				}
				saved.Devices[0].GetTape().ReadParameters.Completion.FixedSeconds = proto.Float64(99)
			}

			// Returned and attempt-default messages must not mutate committed completion measurements.
			stored, err := module.Device.Current(ctx)
			require.NoError(t, err)
			require.True(t, proto.Equal(want, stored))
			defaults := TapeDefaults(stored, "/dev/inactive")
			if defaults.ReadParameters.Completion != nil {
				defaults.ReadParameters.Completion.FixedSeconds = proto.Float64(99)
			}
			require.True(t, proto.Equal(want, stored))
			require.Nil(t, stored.Devices[0].GetTape().ReadParameters.Positioning)
			require.Nil(t, stored.Devices[0].GetTape().ReadParameters.Reading)
		})
	}
}

func TestDeviceReadCompletionRejectsInvalidReplacement(t *testing.T) {
	// Invalid completion edits cannot replace previously committed measurements for an inactive device.
	module := testModule(t)
	ctx := context.Background()
	baseline := deviceTestValue()
	baseline.Devices[0].GetTape().ReadParameters = &entity.TapeReadParameters{
		Completion: &entity.TapeReadCompletion{FullSpanSeconds: proto.Float64(107)},
	}
	_, err := module.Device.Save(ctx, baseline)
	require.NoError(t, err)
	for field, set := range map[string]func(*entity.TapeReadCompletion, float64){
		"full span": func(value *entity.TapeReadCompletion, seconds float64) {
			value.FullSpanSeconds = proto.Float64(seconds)
		},
		"fixed": func(value *entity.TapeReadCompletion, seconds float64) { value.FixedSeconds = proto.Float64(seconds) },
	} {
		invalid := []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1e20}
		if field == "full span" {
			invalid = append(invalid, 0)
		}
		for _, seconds := range invalid {
			t.Run(fmt.Sprintf("%s/%v", field, seconds), func(t *testing.T) {
				// Validation rejects nonfinite, negative, overflowing and zero full-span inputs before storage.
				value := proto.Clone(baseline).(*entity.DeviceSettings)
				set(value.Devices[0].GetTape().ReadParameters.Completion, seconds)
				require.Error(t, ValidateTapeReadParameters(value.Devices[0].GetTape().ReadParameters))
				_, err := module.Device.Save(ctx, value)
				require.ErrorIs(t, err, ErrInvalid)
				stored, err := module.Device.Current(ctx)
				require.NoError(t, err)
				require.True(t, proto.Equal(baseline, stored))
			})
		}
	}
}
