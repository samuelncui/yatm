package settings

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const learnedReadParametersBytes = 8 << 20

// DeviceGroup keeps user-owned Device settings and learned read parameters in one typed Settings row.
// Each writer replaces only the part it owns, using the transaction's latest committed value.
type DeviceGroup struct {
	group Group[*entity.DeviceSettings]
}

func (g DeviceGroup) Current(ctx context.Context) (*entity.DeviceSettings, error) {
	return g.group.Current(ctx)
}

func (g DeviceGroup) Read(ctx context.Context) (Stored[*entity.DeviceSettings], error) {
	return g.group.Read(ctx)
}

func (g DeviceGroup) Save(ctx context.Context, value *entity.DeviceSettings) (*entity.DeviceSettings, error) {
	// Client values describe user-owned settings only; stale or forged learning fields cannot replace facts.
	if value == nil {
		return nil, fmt.Errorf("%w, group=devices is missing", ErrInvalid)
	}
	next := clone(value)
	next.LearnedReadParameters = nil
	if err := ValidateDevice(next); err != nil {
		return nil, fmt.Errorf("%w, group=devices: %v", ErrInvalid, err)
	}

	// Read through the writer transaction, never through an independent committed-read pool.
	var saved *entity.DeviceSettings
	err := g.group.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		group := newGroup(tx, g.group.definition)
		current, err := group.Current(ctx)
		if err != nil {
			return err
		}
		next.LearnedReadParameters = current.LearnedReadParameters
		saved, err = group.Save(ctx, next)
		return err
	})
	return saved, err
}

// PublishTapeReadModel replaces one exact applicability only if its prior model still matches.
// expected nil means this configuration had no model when the refit began. A stale fit is skipped.
// The caller owns fitting; this method owns isolated, conflict-safe persistence.
func (g DeviceGroup) PublishTapeReadModel(ctx context.Context, expected, next *entity.TapeLearnedReadParameters) (bool, error) {
	// Freeze the result before accessing storage and reject cross-configuration replacement.
	if err := validateLearnedReadParameters(next); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if proto.Size(next) > learnedReadParametersBytes {
		return false, fmt.Errorf("%w: learned read parameters exceed their device budget", ErrInvalid)
	}
	next = clone(next)
	if expected != nil {
		expected = clone(expected)
		if !sameTapeModelScope(expected, next) {
			return false, fmt.Errorf("%w: learned read parameters applicability changed", ErrInvalid)
		}
	}

	// Retain the latest user-owned settings and other configurations while comparing the fitted base.
	applied := false
	err := g.group.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		group := newGroup(tx, g.group.definition)
		current, err := group.Current(ctx)
		if err != nil {
			return err
		}
		index := -1
		for i, model := range current.GetLearnedReadParameters() {
			if sameTapeModelScope(model, next) {
				index = i
				break
			}
		}
		var previous *entity.TapeLearnedReadParameters
		if index >= 0 {
			previous = current.LearnedReadParameters[index]
		}
		if !proto.Equal(previous, expected) {
			return nil
		}
		if index < 0 {
			current.LearnedReadParameters = append(current.LearnedReadParameters, next)
		} else {
			current.LearnedReadParameters[index] = next
		}
		current.LearnedReadParameters = retainTapeReadModels(current.LearnedReadParameters, next, learnedReadParametersBytes)
		if _, err := group.Save(ctx, current); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied && err == nil, err
}

func retainTapeReadModels(values []*entity.TapeLearnedReadParameters, next *entity.TapeLearnedReadParameters, limit int) []*entity.TapeLearnedReadParameters {
	// Configuration changes cannot grow automatic results without bound. Retire the oldest results
	// for this device only; manual read parameters and the just-fitted result are never evicted.
	bytes := 0
	var candidates []int
	for i, value := range values {
		if value.GetDevice() != next.Device {
			continue
		}
		bytes += proto.Size(value)
		if value != next {
			candidates = append(candidates, i)
		}
	}
	if bytes <= limit {
		return values
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return values[candidates[i]].UpdatedAtNs < values[candidates[j]].UpdatedAtNs
	})
	for _, index := range candidates {
		bytes -= proto.Size(values[index])
		values[index] = nil
		if bytes <= limit {
			break
		}
	}
	retained := values[:0]
	for _, value := range values {
		if value != nil {
			retained = append(retained, value)
		}
	}
	clear(values[len(retained):])
	return retained
}

func sameTapeModelScope(left, right *entity.TapeLearnedReadParameters) bool {
	return left.GetDevice() == right.GetDevice() &&
		proto.Equal(left.GetParameters().GetAppliesTo(), right.GetParameters().GetAppliesTo())
}

func validateLearnedReadParameters(value *entity.TapeLearnedReadParameters) error {
	// A published model needs observed applicability, provenance and retained observations.
	if strings.TrimSpace(value.GetDevice()) == "" || strings.TrimSpace(value.GetFitVersion()) == "" ||
		value.GetUpdatedAtNs() <= 0 || value.GetTrainingObservations() == 0 {
		return fmt.Errorf("learned read parameters require device, version, time and training observations")
	}
	parameters := value.GetParameters()
	if err := ValidateTapeReadParameters(parameters); err != nil {
		return fmt.Errorf("learned Tape parameters: %w", err)
	}
	scope := parameters.GetAppliesTo()
	if scope == nil || scope.DriveSerial == nil || scope.Firmware == nil || scope.DensityCode == nil ||
		scope.RecordMode == nil || scope.RecordBytes == nil || scope.HardwareCompression == nil || scope.EncryptionAlgorithm == nil {
		return fmt.Errorf("learned read parameters require complete observed applicability")
	}
	if parameters.Positioning == nil && parameters.Reading == nil && parameters.Completion == nil {
		return fmt.Errorf("learned read parameters have no measured timings")
	}

	// Stored error summaries are finite nonnegative seconds, not signed regression coefficients.
	for _, seconds := range []float64{value.MaeSeconds, value.MaxErrorSeconds} {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return fmt.Errorf("learned read parameters error must be finite and nonnegative")
		}
	}
	if value.MaxErrorSeconds < value.MaeSeconds {
		return fmt.Errorf("learned read parameters maximum error is below their mean")
	}
	return nil
}
