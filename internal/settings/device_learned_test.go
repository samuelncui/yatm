package settings

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func learnedTapeModel(device string) *entity.TapeLearnedReadParameters {
	return &entity.TapeLearnedReadParameters{
		Device: device, FitVersion: "locate-linear-1", UpdatedAtNs: 1792581049000000000,
		TrainingObservations: 32, MaeSeconds: 2, MaxErrorSeconds: 5,
		Parameters: &entity.TapeReadParameters{
			AppliesTo: &entity.TapeReadApplicability{
				DriveSerial: proto.String("drive"), Firmware: proto.String("firmware"), DensityCode: proto.Uint32(0x58),
				RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(), RecordBytes: proto.Uint32(512 * 1024),
				HardwareCompression: proto.Bool(false), EncryptionAlgorithm: proto.String(""),
			},
			Positioning:     &entity.TapeReadPositioning{BaseSeconds: proto.Float64(3)},
			SourceReference: "ordinary completed LOCATE observations",
		},
	}
}

func TestDeviceSavePreservesServerOwnedLearning(t *testing.T) {
	// The editor reads a snapshot before a completed task publishes its independently validated model.
	ctx := context.Background()
	module := testModule(t)
	editor, err := module.Device.Current(ctx)
	require.NoError(t, err)
	model := learnedTapeModel("tape")
	applied, err := module.Device.PublishTapeReadModel(ctx, nil, model)
	require.NoError(t, err)
	require.True(t, applied)

	// An old form can replace user-owned settings but cannot erase or forge automatic observations.
	editor.Devices = []*entity.DeviceConfig{{Device: "tape", Options: &entity.DeviceConfig_Tape{
		Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO},
	}}}
	editor.LearnedReadParameters = []*entity.TapeLearnedReadParameters{{Device: "forged"}}
	saved, err := module.Device.Save(ctx, editor)
	require.NoError(t, err)
	require.Len(t, saved.LearnedReadParameters, 1)
	require.True(t, proto.Equal(model, saved.LearnedReadParameters[0]))
	require.Equal(t, "forged", editor.LearnedReadParameters[0].Device)
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, saved.Devices[0].GetTape().ReadOrder)

	// Removing user-owned settings retains learning without recreating a DeviceConfig, including after restart.
	_, err = module.Device.Save(ctx, &entity.DeviceSettings{})
	require.NoError(t, err)
	reopened := New(module.db, module.previewDefinition)
	current, err := reopened.Device.Current(ctx)
	require.NoError(t, err)
	require.Empty(t, current.Devices)
	require.True(t, proto.Equal(model, current.LearnedReadParameters[0]))
	model.Parameters.Positioning.BaseSeconds = proto.Float64(999)
	require.Equal(t, 3.0, current.LearnedReadParameters[0].Parameters.Positioning.GetBaseSeconds())
}

func TestLearnedReadParametersPublicationSkipsStaleBase(t *testing.T) {
	// The validated candidate names the exact incumbent it was compared with.
	ctx := context.Background()
	module := testModule(t)
	first := learnedTapeModel("tape")
	applied, err := module.Device.PublishTapeReadModel(ctx, nil, first)
	require.NoError(t, err)
	require.True(t, applied)
	second := proto.Clone(first).(*entity.TapeLearnedReadParameters)
	second.Parameters.Positioning.BaseSeconds = proto.Float64(2)
	second.UpdatedAtNs++
	applied, err = module.Device.PublishTapeReadModel(ctx, first, second)
	require.NoError(t, err)
	require.True(t, applied)

	// A concurrent fit against the old base cannot overwrite the newer model or mutate other scope.
	stale := proto.Clone(first).(*entity.TapeLearnedReadParameters)
	stale.Parameters.Positioning.BaseSeconds = proto.Float64(1)
	applied, err = module.Device.PublishTapeReadModel(ctx, first, stale)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = module.Device.PublishTapeReadModel(ctx, nil, stale)
	require.NoError(t, err)
	require.False(t, applied)
	stale.Parameters.AppliesTo.RecordBytes = proto.Uint32(256 * 1024)
	applied, err = module.Device.PublishTapeReadModel(ctx, first, stale)
	require.ErrorIs(t, err, ErrInvalid)
	require.False(t, applied)
	applied, err = module.Device.PublishTapeReadModel(ctx, nil, stale)
	require.NoError(t, err)
	require.True(t, applied)
	current, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Len(t, current.LearnedReadParameters, 2)
	require.True(t, proto.Equal(second, current.LearnedReadParameters[0]))
}

func TestConcurrentDeviceWritersRetainBothOwners(t *testing.T) {
	// User preference updates and independent device models share the same transaction-backed row.
	ctx := context.Background()
	module := testModule(t)
	errors := make(chan error, 10)
	var pending sync.WaitGroup
	for i := 0; i < 10; i++ {
		pending.Add(1)
		go func() {
			defer pending.Done()
			if i%2 == 0 {
				_, err := module.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{
					{Device: "operator", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{}}},
				}})
				errors <- err
				return
			}
			_, err := module.Device.PublishTapeReadModel(ctx, nil, learnedTapeModel(fmt.Sprintf("tape-%d", i)))
			errors <- err
		}()
	}
	pending.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	// No completed save loses another writer's data, regardless of scheduling order.
	current, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Len(t, current.LearnedReadParameters, 5)
	require.Len(t, current.Devices, 1)
	require.Equal(t, "operator", current.Devices[0].Device)
}

func TestTapeLearningAndPlanningSettings(t *testing.T) {
	// Defaults are resolved without manufacturing stored user settings.
	require.True(t, TapePassiveReadLearning(nil))
	require.Equal(t, 5*time.Minute, TapeReadPlanningBudget(nil))
	prefs := &entity.TapeDeviceSettings{PassiveReadLearning: proto.Bool(false), ReadPlanningSeconds: proto.Uint32(3600)}
	require.False(t, TapePassiveReadLearning(prefs))
	require.Equal(t, time.Hour, TapeReadPlanningBudget(prefs))
	value := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{
		{Device: "tape", Options: &entity.DeviceConfig_Tape{Tape: prefs}},
	}}
	require.NoError(t, ValidateDevice(value))
	prefs.ReadPlanningSeconds = proto.Uint32(0)
	require.ErrorContains(t, ValidateDevice(value), "must be positive")
}

func TestLearnedReadParametersRequireObservedScopeAndObservations(t *testing.T) {
	for name, mutate := range map[string]func(*entity.TapeLearnedReadParameters){
		"unknown density": func(value *entity.TapeLearnedReadParameters) { value.Parameters.AppliesTo.DensityCode = nil },
		"unobserved":      func(value *entity.TapeLearnedReadParameters) { value.TrainingObservations = 0 },
		"nonfinite error": func(value *entity.TapeLearnedReadParameters) { value.MaeSeconds = math.NaN() },
		"negative error":  func(value *entity.TapeLearnedReadParameters) { value.MaxErrorSeconds = -1 },
		"reversed errors": func(value *entity.TapeLearnedReadParameters) { value.MaxErrorSeconds = 1 },
		"no model":        func(value *entity.TapeLearnedReadParameters) { value.Parameters.Positioning = nil },
	} {
		t.Run(name, func(t *testing.T) {
			model := learnedTapeModel("tape")
			mutate(model)
			require.Error(t, ValidateDevice(&entity.DeviceSettings{LearnedReadParameters: []*entity.TapeLearnedReadParameters{model}}))
		})
	}
}

func TestTapeReadModelRetentionPreservesOwnersAndNewestAcceptance(t *testing.T) {
	// Use the same byte accounting with a small limit, independent of the wall clock's ordering.
	oldest, recent, next := learnedTapeModel("tape"), learnedTapeModel("tape"), learnedTapeModel("tape")
	oldest.UpdatedAtNs, recent.UpdatedAtNs, next.UpdatedAtNs = 10, 20, 5
	other := learnedTapeModel("another-device")
	values := []*entity.TapeLearnedReadParameters{recent, other, oldest, next}
	retained := retainTapeReadModels(values, next, proto.Size(recent)+proto.Size(next))
	require.Equal(t, []*entity.TapeLearnedReadParameters{recent, other, next}, retained)
	require.Nil(t, values[len(retained)], "retired models cannot remain held by the backing slice")

	// Applying the bound again does not disturb retained order or another device's model.
	again := retainTapeReadModels(retained, next, proto.Size(recent)+proto.Size(next))
	require.Equal(t, retained, again)
}

func TestLearnedReadParametersDuplicateScopeIncludesDeviceAndPresence(t *testing.T) {
	first, second := learnedTapeModel("first"), learnedTapeModel("second")
	value := &entity.DeviceSettings{LearnedReadParameters: []*entity.TapeLearnedReadParameters{first, second}}
	require.NoError(t, ValidateDevice(value))
	second.Device = "first"
	require.ErrorContains(t, ValidateDevice(value), "duplicated")
	second.Parameters.AppliesTo.RecordBytes = proto.Uint32(256 * 1024)
	require.NoError(t, ValidateDevice(value))
}

func TestDeviceReadIgnoresRetiredFields(t *testing.T) {
	// Rows saved by earlier alpha builds keep loading; the next save rewrites them without those fields.
	ctx := context.Background()
	module := testModule(t)
	stored := `{"devices":[{"device":"tape","tape":{"readOrder":"TAPE_READ_ORDER_AUTO","readLearningAdmission":null}}],` +
		`"learnedReadParameters":[{"device":"tape","fitVersion":"v","trainingObservations":3,"validationObservations":2,` +
		`"updatedAtNs":"1","parameters":{"appliesTo":{"driveSerial":"d","firmware":"f","densityCode":88,` +
		`"recordMode":"TAPE_RECORD_MODE_VARIABLE","recordBytes":524288,"hardwareCompression":false,"encryptionAlgorithm":""},` +
		`"positioning":{"baseSeconds":3,"uncertaintySeconds":9}}}]}`
	require.NoError(t, module.db.Create(&row{Key: "devices", Value: []byte(stored)}).Error)
	value, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, value.Devices[0].GetTape().GetReadOrder())
	require.Len(t, value.LearnedReadParameters, 1)
	require.Equal(t, 3.0, value.LearnedReadParameters[0].Parameters.Positioning.GetBaseSeconds())

	_, err = module.Device.Save(ctx, value)
	require.NoError(t, err)
	var document row
	require.NoError(t, module.db.First(&document, "key = ?", "devices").Error)
	for _, retired := range []string{"readLearningAdmission", "validationObservations", "uncertaintySeconds"} {
		require.NotContains(t, string(document.Value), retired)
	}
}
