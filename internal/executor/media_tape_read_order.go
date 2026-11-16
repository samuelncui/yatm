package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
	settings   *entity.DeviceSettings
	defaults   *entity.TapeDeviceSettings
	report     *entity.InspectDeviceResponse
	endpoints  []tape.WrapEndpoint
	source     string
	observedAt time.Time
	reason     string
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
	value, err := b.executor.deviceSettings(ctx)
	if err != nil {
		return nil, err
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
	b.logReadDirectoryPreparation(ctx, device, started, snapshot.report, "LTFS read directory preparation")
	if observeErr == nil && geometry.Complete() {
		snapshot.endpoints = append([]tape.WrapEndpoint(nil), geometry.Endpoints...)
		snapshot.source, snapshot.observedAt = geometry.Source, geometry.ObservedAt
	}
	// Close the raw owner even after a failed observation; no mount may overlap its commands or cleanup.
	if err := errors.Join(observeErr, b.executor.cleanupRawTape(ctx, b.jobID, device, prepared)); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// logReadDirectoryPreparation reports the drive's read-directory preparation detail.
func (b *mediaBackend) logReadDirectoryPreparation(ctx context.Context, device string, started time.Time,
	report *entity.InspectDeviceResponse, message string,
) {
	for _, feature := range report.GetTape().GetFeatures() {
		if feature.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER {
			b.logger.WithContext(ctx).WithFields(map[string]any{
				"device": device, "elapsed_ms": time.Since(started).Milliseconds(), "detail": feature.Detail,
			}).Info(message)
			return
		}
	}
}

func (s *tapeReadSession) prepareReadOrder(ctx context.Context, snapshot *ltfsReadSnapshot) error {
	// Construction always leaves the native path available; only cancellation interrupts optional facts.
	s.readOrder = &ltfsSessionReadOrder{reason: snapshot.reason}
	s.readOrder.valid.Store(true)
	requested := settingspkg.TapeDefaultReadOrder(snapshot.defaults)
	optimization := &entity.TapePreparedRead{PreparedAtNs: time.Now().UnixNano(), JobId: s.backend.jobID,
		MediaId: s.media.ID, Identity: s.media.Identity, RequestedOrder: requested.String(),
		EffectiveOrder: entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL.String(), ParametersSource: "unavailable",
		CoverageDetail: "Initial access is unknown; every route retains the same first file"}
	s.readOrder.prepared = optimization.PreparedAtNs
	parameterReason := ""
	defer func() {
		// A configured set that does not apply stays visible beside the read-order reason.
		optimization.ApplicabilityReason = s.readOrder.reason
		if parameterReason != "" {
			optimization.ApplicabilityReason = strings.TrimSuffix(parameterReason+"; "+s.readOrder.reason, "; ")
		}
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
	facts, err := readLTFSReadFacts(ctx, func(name string, limit int) ([]byte, error) {
		return mountedLTFSAttribute(s.mountPoint, name, limit)
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.readOrder.reason = err.Error()
		return nil
	}

	// Model timing uses the volume's block size; no LTFS file latency is fabricated.
	parameters, source, reason := selectTapeReadParameters(snapshot.settings, s.device, snapshot.report)
	optimization.ParametersSource, optimization.ParametersReference = source, parameters.GetSourceReference()
	if parameters == nil {
		s.readOrder.reason = reason
		return nil
	}
	parameterReason = reason
	value, reason := ltfsReadPositionMap(parameters, facts, snapshot)
	if value == nil {
		s.readOrder.reason = reason
		return nil
	}
	optimization.PositionSource = value.source
	model, reason := compileTapePositionMap(parameters, facts.blockBytes, value, s.readOrder.valid.Load)
	if model == nil {
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
	s.readOrder.provider, s.readOrder.reason = mediapkg.NewLTFSReadOrderProvider(mediapkg.LTFSReadOptions{
		BlockBytes: facts.blockBytes, Compression: proto.Bool(facts.compression), Partitions: facts.partitions,
		Model: model, PlanningBudget: settingspkg.TapeReadPlanningBudget(snapshot.defaults),
		Completion: tapeReadCompletion(parameters), DataEnd: dataEnd,
		ReadIndex: func(ctx context.Context, yield func(*mediapkg.LTFSIndexEntry) error) error {
			return s.readCurrentIndex(ctx, facts.uuid, facts.generation, yield)
		},
	})
	if s.readOrder.provider == nil {
		return nil
	}
	optimization.EffectiveOrder = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO.String()
	initial := "common initial access is unquoted"
	if _, assumed, _ := s.readOrder.provider.Current(ctx); assumed {
		initial = "initial access is priced from the assumed post-mount Data partition start"
	}
	optimization.CoverageDetail = fmt.Sprintf("%d partition maps, %d reported wraps; route comparison assumes no Index rewrite and includes rewind/unload; mounted read-only and clean state are not observed; %s; cross-partition routes retain LOGICAL order; native Tape timing estimates do not validate LTFS whole-file latency",
		len(value.eod), len(value.wraps), initial)
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
