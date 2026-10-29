package executor

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/samuelncui/yatm/internal/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (b *mediaBackend) startTarReadLearning(ctx context.Context, prepared *tape.Device, device, barcode string,
	settings *entity.DeviceSettings, parameters *entity.TapeReadParameters,
	model *mediapkg.ReadCostModel, positionMap *entity.TapePositionMap, position *entity.TapeBytePosition,
) func() {
	// Optional learning cannot create missing applicability, change this Session, or start measurement I/O.
	if !settingspkg.TapePassiveReadLearning(settingspkg.TapeDefaults(settings, device)) || parameters == nil ||
		parameters.GetAppliesTo() == nil || parameters.GetAppliesTo().GetHardwareCompression() || b.executor.settings == nil {
		return func() {}
	}
	observations := prepared.ObserveCommands()
	if observations == nil {
		return func() {}
	}
	parameters = proto.Clone(parameters).(*entity.TapeReadParameters)
	scope, err := protojson.Marshal(parameters.AppliesTo)
	if err != nil {
		observations.Stop()
		b.logger.WithContext(ctx).WithError(err).Warn("encode Tape read learning applicability failed")
		return func() {}
	}

	// Every record shares this Job's cartridge and the position map that resolved its points.
	template := tapeReadObservation{Device: device, Scope: scope, JobID: b.jobID, Barcode: barcode}
	if model != nil && positionMap != nil {
		template.MapSource, template.MapObservedAtNS = positionMap.Source, proto.Int64(positionMap.ObservedAtNs)
		template.MinLPOS, template.MaxLPOS = positionMap.MinLpos, positionMap.MaxLpos
	}

	// This consumer only buffers records; the file is written after native I/O settles, then the
	// refit runs in the background so it never delays releasing the drive.
	finished := make(chan []tapeReadObservation, 1)
	go func() { finished <- collectTapeReadObservations(observations.Values(), template, model, position) }()
	return func() {
		observations.Stop()
		values := <-finished
		if dropped := observations.Dropped(); dropped != 0 {
			b.logger.WithContext(ctx).WithField("observations_dropped", dropped).Warn("Tape read learning queue was full")
		}
		if ctx.Err() != nil || len(values) == 0 {
			return
		}
		if err := b.executor.appendTapeReadObservations(ctx, b.jobID, barcode, values); err != nil {
			b.logger.WithContext(ctx).WithError(err).Warn("save Tape read learning observations failed")
			return
		}
		// The refit outlives this Job, so it logs to the service log rather than the Job log.
		b.executor.startTapeReadRefit(logrus.StandardLogger(), device, parameters)
	}
}

type tapeReadCursor struct {
	partition uint32
	object    uint64
}

// tapeReadCollector derives records from completed native commands. Its logical cursor comes only
// from successful motion, READ POSITION and complete records; any gap withdraws it.
type tapeReadCollector struct {
	template tapeReadObservation
	model    *mediapkg.ReadCostModel
	records  []tapeReadObservation

	cursor      tapeReadCursor
	file        *uint64
	known       bool
	first       bool
	mark        *tapeReadObservation // A filemark READ awaiting its data READ.
	segment     *tapeReadObservation // The open READ segment.
	previous    observationAction
	idle        *float64
	previousEnd time.Time
}

func collectTapeReadObservations(values <-chan tape.CommandObservation, template tapeReadObservation,
	model *mediapkg.ReadCostModel, initial *entity.TapeBytePosition,
) []tapeReadObservation {
	c := &tapeReadCollector{template: template, model: model, known: initial != nil}
	if initial != nil {
		c.cursor = tapeReadCursor{partition: initial.Partition, object: initial.LogicalObject}
	}
	sequence := uint64(0)
	for value := range values {
		// A dropped or failed command leaves the cursor, idle time and preceding action unknown.
		if sequence != 0 && value.Sequence != sequence+1 || !value.Known {
			c.flush()
			c.known, c.first, c.mark, c.file, c.previous, c.previousEnd = false, false, nil, nil, "", time.Time{}
		}
		sequence = value.Sequence
		c.idle = nil
		if !c.previousEnd.IsZero() && !value.StartedAt.IsZero() {
			c.idle = proto.Float64(max(value.StartedAt.Sub(c.previousEnd).Seconds(), 0))
		}
		if !value.StartedAt.IsZero() {
			c.previousEnd = value.StartedAt.Add(value.Elapsed)
		}
		if !value.Known {
			continue
		}
		c.previous = c.observe(value)
	}
	c.flush()
	return c.records
}

// observe applies one known command and returns the action it belongs to, or empty for another command.
func (c *tapeReadCollector) observe(value tape.CommandObservation) observationAction {
	switch value.Opcode {
	case 0x34:
		c.flush()
		c.cursor = tapeReadCursor{partition: value.Position.Partition, object: value.Position.Object}
		c.file, c.known = nil, true
		if value.Position.FileKnown {
			c.file = proto.Uint64(value.Position.File)
		}
		return ""
	case 1:
		c.flush()
		target := tapeReadCursor{}
		if c.known {
			record := c.start(value, observationRewind)
			record.To = c.point(target, proto.Uint64(0))
			c.records = append(c.records, *record)
		}
		c.cursor, c.file, c.known, c.first, c.mark = target, proto.Uint64(0), true, false, nil
		return observationRewind
	case 0x2b, 0x92:
		c.flush()
		target := tapeReadCursor{partition: value.Position.Partition, object: value.Position.Object}
		if c.known {
			record := c.start(value, observationLocate)
			record.To = c.point(target, nil)
			c.records = append(c.records, *record)
		}
		c.cursor, c.file, c.known, c.first, c.mark = target, nil, true, true, nil
		return observationLocate
	case 0x08:
		return c.read(value)
	case 0x1b:
		c.flush()
		if c.known {
			c.records = append(c.records, *c.start(value, observationUnload))
		}
		c.known, c.first, c.mark = false, false, nil
		return observationUnload
	case 0x55, 0xb5, 0x10:
		// Proven nonmoving restoration/synchronization preserves position, not READ continuity.
		c.flush()
		c.first, c.mark = false, nil
		return ""
	default:
		c.flush()
		return ""
	}
}

func (c *tapeReadCollector) read(value tape.CommandObservation) observationAction {
	if !c.known || c.cursor.object == math.MaxUint64 {
		c.flush()
		return observationRead
	}

	// A filemark READ waits for the following data READ; a later filemark replaces it.
	if value.Filemark {
		c.flush()
		c.mark = c.start(value, observationFilemarkRead)
		c.cursor.object++
		if c.file != nil {
			c.file = proto.Uint64(*c.file + 1)
		}
		c.mark.To = c.point(c.cursor, c.file)
		return observationFilemarkRead
	}
	if value.EOD || value.Bytes == 0 {
		c.flush()
		return observationRead
	}

	// First and filemark READs are single records; other data READs extend one segment per discard label.
	switch {
	case c.first:
		record := c.start(value, observationFirstRead)
		c.cursor.object++
		record.To, record.Bytes = c.point(c.cursor, c.file), uint64(value.Bytes)
		c.records = append(c.records, *record)
		c.first, c.mark = false, nil
		return observationFirstRead
	case c.mark != nil:
		c.cursor.object++
		c.extend(c.mark, value)
		c.records = append(c.records, *c.mark)
		c.mark = nil
		return observationFilemarkRead
	}
	if c.segment != nil && c.segment.Discard != value.Discard {
		c.flush()
	}
	if c.segment == nil {
		c.segment = c.start(value, observationRead)
		c.cursor.object++
		c.segment.To, c.segment.Bytes, c.segment.Discard = c.point(c.cursor, c.file), uint64(value.Bytes), value.Discard
		return observationRead
	}
	c.cursor.object++
	c.extend(c.segment, value)
	return observationRead
}

// start opens a record for one command beginning at the current cursor.
func (c *tapeReadCollector) start(value tape.CommandObservation, action observationAction) *tapeReadObservation {
	record := c.template
	record.Opcode, record.Action, record.PreviousAction = value.Opcode, action, c.previous
	if !value.StartedAt.IsZero() {
		record.StartedAtNS = value.StartedAt.UnixNano()
	}
	record.IdleSeconds = c.idle
	record.From = c.point(c.cursor, c.file)
	record.Commands, record.DeviceSeconds = 1, value.Elapsed.Seconds()
	return &record
}

// extend adds one contiguous command that ended at the already advanced cursor.
func (c *tapeReadCollector) extend(record *tapeReadObservation, value tape.CommandObservation) {
	gap := 0.0
	if c.idle != nil {
		gap = *c.idle
	}
	record.To = c.point(c.cursor, c.file)
	record.Commands++
	record.Bytes += uint64(value.Bytes)
	record.DeviceSeconds += value.Elapsed.Seconds()
	record.HostSeconds += gap
	record.MaxHostGapSeconds = max(record.MaxHostGapSeconds, gap)
}

func (c *tapeReadCollector) flush() {
	if c.segment != nil {
		c.records = append(c.records, *c.segment)
		c.segment = nil
	}
}

// point records the logical address and, when the position map resolves it, its physical coordinate.
func (c *tapeReadCollector) point(position tapeReadCursor, file *uint64) observationPoint {
	value := observationPoint{Partition: proto.Uint32(position.partition),
		Object: proto.Uint64(position.object), File: file}
	if c.model == nil {
		return value
	}
	if physical, ok := c.model.Point(&entity.TapeBytePosition{Partition: position.partition,
		LogicalObject: position.object}); ok {
		value.Wrap, value.Offset, value.Span = proto.Uint32(physical.Wrap), proto.Uint64(physical.Offset), proto.Uint64(physical.Span)
	}
	return value
}

// startTapeReadRefit refits in the background for the process lifetime; shutdown waits for it.
func (e *Executor) startTapeReadRefit(logger *logrus.Logger, device string, parameters *entity.TapeReadParameters) {
	tools.Working()
	go func() {
		defer tools.Done()
		e.refitTapeReadModel(tools.ShutdownContext, logger, device, parameters)
	}()
}

// refitTapeReadModel fits every retained observation of one device and applicability and publishes
// the result. Refits of one device are serialized, so the stored model read here is the one replaced.
func (e *Executor) refitTapeReadModel(ctx context.Context, logger *logrus.Logger, device string,
	parameters *entity.TapeReadParameters,
) {
	lock, _ := e.readRefits.LoadOrStore(device, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	settings, err := e.settings.Device.Current(ctx)
	if err != nil {
		logger.WithContext(ctx).WithError(err).Warn("read learned Tape read parameters failed")
		return
	}
	var expected *entity.TapeLearnedReadParameters
	for _, value := range settings.GetLearnedReadParameters() {
		if value.GetDevice() == device && proto.Equal(value.GetParameters().GetAppliesTo(), parameters.AppliesTo) {
			expected = value
			break
		}
	}
	// A model published by an earlier refit supplies the values that this log cannot identify.
	base := parameters
	if expected != nil {
		base = expected.Parameters
	}
	refit := newTapeReadRefit(base)
	if err := e.visitTapeReadObservations(ctx, logger, device, parameters.AppliesTo, refit.add); err != nil {
		logger.WithContext(ctx).WithError(err).Warn("read Tape read learning observations failed")
		return
	}
	model := refit.model(device, time.Now())
	if model == nil {
		return
	}
	applied, err := e.settings.Device.PublishTapeReadModel(ctx, expected, model)
	if err != nil {
		logger.WithContext(ctx).WithError(err).Warn("save learned Tape read parameters failed")
		return
	}
	if applied {
		logger.WithContext(ctx).WithFields(map[string]any{
			"device": device, "training_observations": model.TrainingObservations,
			"mae_seconds": model.MaeSeconds, "max_error_seconds": model.MaxErrorSeconds,
		}).Info("learned Tape read parameters are available for later operations")
	}
}
