package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/protobuf/proto"
)

type ltfsReadSnapshot struct {
	settings    *entity.DeviceSettings
	defaults    *entity.TapeDeviceSettings
	report      *entity.InspectDeviceResponse
	labels      *mediapkg.LTFSLabelFacts
	labelReason string
	endpoints   []tape.WrapEndpoint
	boundary    entity.TapePositionEndpointConvention
	source      string
	observedAt  time.Time
	reason      string
}

var _ mediapkg.OrderedReadSession = (*tapeReadSession)(nil)

type ltfsSessionReadOrder struct {
	provider mediapkg.ReadOrderProvider
	reason   string
	prepared int64
	valid    atomic.Bool
}

func (b *mediaBackend) observeLTFSReadOrder(ctx context.Context, device string,
	profile *entity.TapeMediaProfile,
) (*ltfsReadSnapshot, error) {
	// Old adapters and callers without an admitted preference retain their original preparation path.
	value, _ := ctx.Value(deviceSettingsKey{}).(*entity.DeviceSettings)
	if value == nil {
		return nil, nil
	}
	snapshot := &ltfsReadSnapshot{settings: value, defaults: settingspkg.TapeDefaults(value, device)}
	if settingspkg.TapeDefaultReadOrder(snapshot.defaults) != entity.TapeReadOrder_TAPE_READ_ORDER_AUTO {
		snapshot.reason = "LOGICAL read order selected"
		return snapshot, nil
	}
	if profile.GetFormat() != library.TapeFormatLTFSV1 && profile.GetFormat() != library.TapeFormatLTFSV0 {
		snapshot.reason = "The Media profile is not LTFS"
		return snapshot, nil
	}

	// The existing attempt owner acquires optional facts before LTFS owns physical I/O.
	prepared, err := b.executor.openRawTape(ctx, b.jobID, device)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, tape.ErrUncertain) {
			return nil, err
		}
		snapshot.reason = fmt.Sprintf("LTFS pre-mount read model observation is unavailable: %v", err)
		return snapshot, nil
	}
	started := time.Now()
	geometry, observeErr := prepared.Geometry(ctx)
	snapshot.report = prepared.Report()
	for _, feature := range snapshot.report.GetTape().GetFeatures() {
		if feature.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER {
			b.logger.WithContext(ctx).WithFields(map[string]any{
				"device": device, "elapsed_ms": time.Since(started).Milliseconds(), "detail": feature.Detail,
			}).Info("LTFS read directory preparation")
			break
		}
	}
	if observeErr == nil && geometry.Valid() && geometry.AllWraps {
		snapshot.endpoints = append([]tape.WrapEndpoint(nil), geometry.Endpoints...)
		snapshot.source, snapshot.observedAt = geometry.Source, geometry.ObservedAt
		snapshot.boundary = geometry.EndpointConvention
	}
	if observeErr == nil {
		// Reading both Labels establishes physical-to-symbolic partition identities without an adapter change.
		started := time.Now()
		labels, err := prepared.ReadLTFSLabels(ctx)
		if err == nil {
			snapshot.labels, err = mediapkg.ParseLTFSLabels(labels)
		} else if !tape.CleanUnsupported(err) {
			observeErr = err
		}
		if err != nil {
			snapshot.labelReason = fmt.Sprintf("LTFS pre-mount Label mapping is unavailable: %v", err)
		}
		b.logger.WithContext(ctx).WithFields(map[string]any{
			"device": device, "elapsed_ms": time.Since(started).Milliseconds(),
			"label_0_bytes": len(labels[0]), "label_1_bytes": len(labels[1]),
			"labels_available": snapshot.labels != nil, "label_reason": snapshot.labelReason,
		}).Info("LTFS pre-mount Label observation")
	}

	// Close the raw owner even after a failed observation; no mount may overlap its commands or cleanup.
	if err := errors.Join(observeErr, b.executor.cleanupRawTape(ctx, b.jobID, device, prepared)); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *tapeReadSession) prepareReadOrder(ctx context.Context, stored *library.Media,
	snapshot *ltfsReadSnapshot,
) error {
	// Construction always leaves the native path available; only cancellation interrupts optional facts.
	if snapshot == nil {
		return nil
	}
	s.readOrder = &ltfsSessionReadOrder{reason: snapshot.reason}
	s.readOrder.valid.Store(true)
	requested := settingspkg.TapeDefaultReadOrder(snapshot.defaults)
	optimization := &entity.TapePreparedRead{PreparedAtNs: time.Now().UnixNano(), JobId: s.backend.jobID,
		MediaId: stored.ID, Identity: stored.Identity, RequestedOrder: requested.String(),
		EffectiveOrder: entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL.String(), ParametersSource: "unavailable",
		CoverageDetail: "Initial access is unknown; every route retains the same first file"}
	s.readOrder.prepared = optimization.PreparedAtNs
	defer func() {
		optimization.ApplicabilityReason = s.readOrder.reason
		s.backend.executor.recordPreparedRead(s.device, optimization)
		s.backend.logger.WithContext(ctx).WithFields(map[string]any{
			"device": s.device, "requested_read_order": optimization.RequestedOrder,
			"effective_read_order": optimization.EffectiveOrder, "read_order_reason": optimization.ApplicabilityReason,
			"read_parameters_source": optimization.ParametersSource, "position_source": optimization.PositionSource,
		}).Info("LTFS read order")
	}()
	if snapshot.reason != "" {
		return nil
	}
	facts, err := readLTFSReadFacts(ctx, snapshot.labels, func(name string, limit int) ([]byte, error) {
		return mountedLTFSAttribute(s.mountPoint, name, limit)
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.readOrder.reason = err.Error()
		if snapshot.labels == nil && snapshot.labelReason != "" {
			s.readOrder.reason = snapshot.labelReason + "; " + s.readOrder.reason
		}
		return nil
	}

	// Model timing keeps its observed record/drive applicability; no LTFS file latency is fabricated.
	scope := ltfsReadScope(snapshot.report, stored.Profile.GetTape(), facts)
	parameters, source := selectTapeReadParameters(snapshot.settings, s.device, snapshot.report, scope)
	optimization.ParametersSource, optimization.ParametersReference = source, parameters.GetSourceReference()
	if reason := tapeReadParametersReason(parameters, scope); reason != "" {
		s.readOrder.reason = reason
		return nil
	}
	value, observed, reason := ltfsReadPositionMap(parameters, stored, facts, snapshot)
	if value == nil {
		s.readOrder.reason = reason
		return nil
	}
	dataEnd, reason := ltfsReadDataEnd(facts, value, func(name string, limit int) ([]byte, error) {
		return mountedLTFSAttribute(s.mountPoint, name, limit)
	})
	if dataEnd == nil {
		s.readOrder.reason = reason
		return ctx.Err()
	}
	optimization.PositionSource = value.Source
	model, reason := compileTapePositionMap(parameters, value, s.readOrder.valid.Load)
	if model == nil {
		s.readOrder.reason = reason
		return nil
	}
	s.readOrder.provider, s.readOrder.reason = mediapkg.NewLTFSReadOrderProvider(mediapkg.LTFSReadOptions{
		BlockBytes: facts.blockBytes, Compression: proto.Bool(facts.compression), Partitions: facts.partitions,
		Model: model, Timing: parameters.GetReading(), PlanningBudget: settingspkg.TapeReadPlanningBudget(snapshot.defaults),
		Completion: tapeReadCompletion(parameters), DataEnd: dataEnd,
		ReadIndex: func(ctx context.Context, yield func(*mediapkg.LTFSIndexEntry) error) error {
			return s.readCurrentIndex(ctx, facts.uuid, facts.generation, yield)
		},
	})
	if s.readOrder.provider == nil {
		return nil
	}
	optimization.EffectiveOrder = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO.String()
	optimization.CoverageDetail = fmt.Sprintf("%d partition maps, %d reported wraps; route comparison assumes no Index rewrite and includes rewind/unload; mounted read-only and clean state are not observed; common initial access is unquoted; cross-partition routes retain LOGICAL order; native Tape timing estimates do not validate LTFS whole-file latency",
		len(value.Partitions), len(value.Wraps))

	// Geometry is owned by this mounted UUID/index; a save failure cannot veto the available native read.
	if observed {
		updated, err := s.backend.executor.Lib().UpdateMediaTapePositionMap(ctx, stored, value)
		if err != nil {
			s.backend.logger.WithContext(ctx).WithError(err).Warn("save observed LTFS position map failed")
		} else {
			s.media = describeMedia(updated)
		}
	}
	return ctx.Err()
}

func (s *tapeReadSession) readCurrentIndex(ctx context.Context, uuid string, generation uint64,
	yield func(*mediapkg.LTFSIndexEntry) error,
) (returnErr error) {
	// Only the Index captured by this live mount may fill missing planning extents.
	if s.readOrder == nil || !s.readOrder.valid.Load() {
		return fmt.Errorf("LTFS read Session has finalized")
	}
	if err := waitForTapeIndex(ctx, s.indexPath); err != nil {
		return err
	}
	file, err := os.Open(s.indexPath)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()

	// Scan once without stat or source reads; the planner joins paths and sizes in its existing index.
	return mediapkg.ParseLTFSReadIndex(ctx, file, uuid, generation, yield)
}

func (s *tapeReadSession) ReadOrder(ctx context.Context) (mediapkg.ReadOrderProvider, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.readOrder == nil {
		return nil, "LOGICAL read order selected", nil
	}
	if !s.readOrder.valid.Load() {
		return nil, "LTFS read Session has finalized", nil
	}
	return s.readOrder.provider, s.readOrder.reason, nil
}

// ReadOrderPlanned records the effective order once the planner or its fallback has settled.
func (s *tapeReadSession) ReadOrderPlanned(order entity.TapeReadOrder, reason string) {
	if s.readOrder != nil {
		s.backend.executor.settlePreparedRead(s.device, s.readOrder.prepared, order, reason)
	}
}

func ltfsReadScope(report *entity.InspectDeviceResponse, profile *entity.TapeMediaProfile,
	facts *ltfsReadFacts,
) *entity.TapeReadApplicability {
	// The label supplies written record/compression policy; preparation supplies current drive identity.
	scope := &entity.TapeReadApplicability{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(),
		RecordBytes: proto.Uint32(facts.blockBytes), HardwareCompression: proto.Bool(facts.compression)}
	if report != nil {
		scope.DriveSerial, scope.Firmware = report.SerialNumber, report.Firmware
		if report.GetTape() != nil {
			scope.DensityCode = report.GetTape().DensityCode
		}
	}
	if profile.GetEncryption() == "" {
		scope.EncryptionAlgorithm = proto.String("")
		return scope
	}

	// A configured encrypted reader needs an observed algorithm; a key string alone cannot name one.
	for _, feature := range report.GetTape().GetFeatures() {
		limits := feature.GetEncryption()
		if limits == nil || limits.CurrentAlgorithmIndex == nil {
			continue
		}
		for _, algorithm := range limits.Algorithms {
			if algorithm.Index == limits.GetCurrentAlgorithmIndex() {
				scope.EncryptionAlgorithm = proto.String(algorithm.Name)
				return scope
			}
		}
	}
	return scope
}
