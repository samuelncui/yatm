package executor

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

func (b *mediaBackend) startTarWriteGeometry(prepared *tape.Device, stored *library.Media) func() (*entity.TapePositionMap, string) {
	// Native observations share the already required start/final position checks; learning adds no commands.
	profile := stored.Profile.GetTape().GetTar()
	if profile == nil || profile.HardwareCompression {
		return func() (*entity.TapePositionMap, string) {
			return nil, "Written compression excludes linear geometry inference"
		}
	}
	observations := prepared.ObserveCommands()
	if observations == nil {
		return func() (*entity.TapePositionMap, string) { return nil, "Native WRITE observations are unavailable" }
	}
	report := prepared.Report()
	profile = proto.Clone(profile).(*entity.TarMediaProfile)
	var prefix *entity.TapePositionMap
	if stored.TapePositionMap != nil {
		prefix = proto.Clone(stored.TapePositionMap).(*entity.TapePositionMap)
	}
	type result struct {
		value  *entity.TapePositionMap
		reason string
	}
	finished := make(chan result, 1)
	go func() {
		value, reason := collectTarWriteGeometry(observations.Values(), report, profile, prefix)
		finished <- result{value, reason}
	}()
	return func() (*entity.TapePositionMap, string) {
		observations.Stop()
		result := <-finished
		if observations.Dropped() != 0 {
			return nil, "Native WRITE observations were dropped; retained geometry is unchanged"
		}
		return result.value, result.reason
	}
}

func collectTarWriteGeometry(values <-chan tape.CommandObservation, report *entity.InspectDeviceResponse,
	profile *entity.TarMediaProfile, prefix *entity.TapePositionMap,
) (*entity.TapePositionMap, string) {
	var learning *tarWriteGeometryLearning
	var end tape.Position
	started := false
	reason := "No settled start position was observed"
	for value := range values {
		if !started && value.Opcode == 0x34 && value.Known {
			started = true
			learning, reason = newTarWriteGeometryLearning(report, profile, value.Position, prefix)
		}
		if learning == nil {
			continue
		}
		learning.Observe(value)
		switch {
		case value.Opcode == 0x34 && value.Known:
			end = value.Position
		case value.Opcode == 0x10 && value.Known:
			// A known sync-only FILEMARK command does not change the settled cursor.
		default:
			end.ObjectKnown = false
		}
	}
	if learning == nil {
		return nil, reason
	}
	return learning.Finish(end, 0)
}

func (b *mediaBackend) retainTarWriteGeometry(ctx context.Context, stored *library.Media,
	descriptor *mediapkg.Descriptor, value *entity.TapePositionMap,
) {
	// Physical cleanup has succeeded. Derived-map failures remain optional and cannot fail content publication.
	if value == nil || descriptor == nil {
		return
	}
	if err := library.ValidateTapePositionMap(stored, value); err != nil {
		b.logger.WithContext(ctx).WithError(err).Warn("discard invalid inferred Tape position map")
		return
	}
	if stored.ID == 0 {
		descriptor.TapePositionMap = value
		return
	}
	updated, err := b.executor.Lib().UpdateMediaTapePositionMap(ctx, stored, value)
	if err != nil {
		b.logger.WithContext(ctx).WithError(err).Warn("save inferred Tape position map failed")
		return
	}
	descriptor.TapePositionMap = updated.TapePositionMap
}
