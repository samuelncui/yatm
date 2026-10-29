package executor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/protobuf/proto"
)

func (e *Executor) openRawTape(ctx context.Context, jobID int64, device string) (*tape.Device, error) {
	// Raw access must use the configured identity captured by this active attempt's lease.
	if !e.configuredDevice(device) {
		return nil, fmt.Errorf("Tape device is not configured, device=%q", device)
	}
	resolved := e.resolveTapeDevices()[device]
	if resolved.err != nil {
		return nil, resolved.err
	}
	if resolved.identity.Key == "" || resolved.key != tapeResource(resolved.identity.Key) {
		return nil, fmt.Errorf("Tape access identity changed")
	}
	e.attemptsLock.Lock()
	held := false
	if current := e.attempts[jobID]; current != nil {
		for _, resource := range current.resources {
			if resource.key == resolved.key {
				held = true
				break
			}
		}
	}
	e.attemptsLock.Unlock()
	if !held {
		return nil, fmt.Errorf("raw Tape access requires the active attempt's device lease")
	}

	// Open only after complete host ownership evidence; construction owns transport cleanup.
	if err := e.tapeProvider.CheckOwnership(ctx, resolved.identity); err != nil {
		return nil, err
	}
	transport, err := e.tapeProvider.Open(resolved.identity)
	if err != nil {
		return nil, err
	}
	prepared, err := tape.NewDevice(ctx, resolved.identity, transport)
	if errors.Is(err, tape.ErrUncertain) {
		e.keepTapeDeviceUnavailable(jobID, device)
	}
	return prepared, err
}

func (e *Executor) cleanupRawTape(ctx context.Context, jobID int64, device string, prepared *tape.Device) error {
	return e.releaseRawTape(ctx, jobID, device, prepared, false)
}

func (e *Executor) releaseRawTape(ctx context.Context, jobID int64, device string, prepared *tape.Device, complete bool) error {
	// Cleanup is a mandatory gate, including controls changed before a Session could be returned.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	finish := prepared.Cleanup
	if complete {
		finish = prepared.Finish
	}
	err := finish(cleanup)
	if err != nil {
		e.keepTapeDeviceUnavailable(jobID, device)
	}
	return err
}

func (b *mediaBackend) newTarReadSession(ctx context.Context, access *entity.ReadTapeTarget, expected *entity.ReadMediaTarget) (session mediapkg.ReadSession, returnErr error) {
	// Resolve the trusted immutable Media before reserving hardware or exposing a content endpoint.
	if access == nil || expected == nil || expected.ExpectedMediaId <= 0 {
		return nil, fmt.Errorf("tar read requires frozen Media expectations")
	}
	device := strings.TrimSpace(access.Device)
	if access.MediaId != nil && access.GetMediaId() != expected.ExpectedMediaId {
		return nil, fmt.Errorf("tar Media hint differs from the frozen expectation")
	}
	stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, expected.ExpectedIdentity)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.DestroyedAtNS != nil {
		return nil, fmt.Errorf("tar read Media is unavailable")
	}
	if err := validateReadExpectation(stored, expected); err != nil {
		return nil, err
	}
	if err := entity.ValidateTarMediaProfile(stored.Profile.GetTape()); err != nil {
		return nil, err
	}

	// Freeze model selection independently of the cartridge identity checks.
	deviceSettings, err := b.deviceSettings(ctx)
	if err != nil {
		return nil, err
	}
	defaults := settingspkg.TapeDefaults(deviceSettings, device)
	readOrder := settingspkg.TapeDefaultReadOrder(defaults)
	key, err := decodeTarKey(stored.Profile.GetTape().Encryption)
	if err != nil {
		return nil, err
	}
	clear(key)

	// Preparation and all cleanup share the existing queued attempt lease.
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, device, onWait)
	}); err != nil {
		return nil, err
	}
	prepared, err := b.executor.openRawTape(ctx, b.jobID, device)
	if err != nil {
		return nil, err
	}

	// One session owns release and the lifetime of its optional learning observations.
	valid := new(atomic.Bool)
	valid.Store(true)
	finishLearning := func() {}
	release := func(ctx context.Context, complete bool) error {
		if prepared == nil {
			return nil
		}
		err := b.executor.releaseRawTape(ctx, b.jobID, device, prepared, complete)
		finishLearning()
		valid.Store(false)
		return err
	}
	cleanup := func(ctx context.Context) error { return release(ctx, false) }
	defer func() {
		if session == nil {
			returnErr = errors.Join(returnErr, cleanup(ctx))
		}
	}()
	if err := crossCheckTarBarcode(ctx, prepared, stored.Identity, false); err != nil {
		return nil, err
	}

	// Reject a changed cartridge before invoking its adapter, including after an explicit reload.
	if access.GetRefreshPositionMap() {
		if err := prepared.RefreshPositionMap(ctx); err != nil {
			return nil, err
		}
		if err := crossCheckTarBarcode(ctx, prepared, stored.Identity, false); err != nil {
			return nil, err
		}
	}

	// Give the adapter exclusive device access, then observe a fresh descriptor after its LOAD.
	err = b.executor.cleanupRawTape(ctx, b.jobID, device, prepared)
	prepared = nil
	if err != nil {
		return nil, err
	}
	if err := b.prepareTarEncryption(ctx, device, stored, false); err != nil {
		return nil, err
	}
	prepared, err = b.executor.openRawTape(ctx, b.jobID, device)
	if err != nil {
		return nil, err
	}
	if err := crossCheckTarBarcode(ctx, prepared, stored.Identity, false); err != nil {
		return nil, err
	}

	// Apply the frozen record policy after the script hands the device back to a fresh descriptor.
	profile := stored.Profile.GetTape().Tar
	records, err := prepared.Configure(ctx, profile, tape.Read)
	if err != nil {
		return nil, err
	}
	b.logTarControls(ctx, "READ", prepared)
	if err := records.Rewind(ctx); err != nil {
		return nil, err
	}
	label, err := mediapkg.ReadTarLabel(ctx, records, records.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateTarLabel(label, stored); err != nil {
		return nil, err
	}
	navigator, err := b.tarNavigator(ctx, prepared)
	if err != nil {
		return nil, err
	}

	// Optional estimates use the admitted settings and the actual prepared drive, never its path.
	parameters, parameterSource := selectTarReadParameters(deviceSettings, device, prepared.Report(), stored.Profile.GetTape())
	parameterReason := tarReadParametersReason(parameters, prepared.Report(), stored.Profile.GetTape())
	var timing *entity.TapeReadTiming
	var initialPosition *entity.TapeBytePosition
	if parameterReason == "" {
		timing = parameters.GetReading()
		if timing != nil && timing.BytesPerSecond != nil && navigator != nil {
			position, err := navigator.Position(ctx)
			switch {
			case tape.CleanUnsupported(err) && ctx.Err() == nil:
				navigator = nil
			case err != nil:
				return nil, errors.Join(mediapkg.ErrReadUnavailable, err)
			case position.Beginning || position.FileKnown && position.File != 1:
				return nil, errors.Join(mediapkg.ErrReadUnavailable, errors.New("tar position differs from the label's Tape file"))
			case !position.ObjectKnown || navigator.AddressBits() == 32 && position.Object > math.MaxUint32:
				navigator = nil
			case position.Partition != 0:
				return nil, errors.Join(mediapkg.ErrReadUnavailable, errors.New("tar position is not in partition zero"))
			default:
				initialPosition = &entity.TapeBytePosition{Partition: position.Partition, LogicalObject: position.Object}
			}
		}
	}
	b.logTarPolicy(ctx, device, "READ", stored.Profile.GetTape())

	// Both read orders share action costing; only AUTO exposes the optional ordering provider.
	var readModel *mediapkg.ReadCostModel
	var learningModel *mediapkg.ReadCostModel
	var learningMap *entity.TapePositionMap
	positionSource := ""
	readReason := parameterReason
	if parameterReason == "" && navigator != nil {
		started := time.Now()
		geometry, err := prepared.Geometry(ctx)
		if err != nil {
			return nil, err
		}
		for _, feature := range prepared.Report().GetTape().GetFeatures() {
			if feature.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER {
				b.logger.WithContext(ctx).WithFields(map[string]any{
					"device": device, "elapsed_ms": time.Since(started).Milliseconds(), "detail": feature.Detail,
				}).Info("Tape read directory preparation")
				break
			}
		}

		// The observation owns endpoint interpretation; retained maps keep their own convention.
		var boundary mediapkg.ReadEndpointConvention
		if geometry != nil {
			boundary = mediapkg.ReadEndpointConvention(geometry.EndpointConvention)
		}
		var observed *entity.TapePositionMap
		if (!geometry.Valid() || !geometry.AllWraps || len(geometry.Endpoints) == 0) &&
			stored.TapePositionMap.GetSource() == "A3/1F/45" &&
			stored.TapePositionMap.GetEndpointConvention() == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE &&
			tape.REOWPEndpointConvention(prepared.Report()) == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE {
			copy := *stored
			copy.TapePositionMap = nil
			stored = &copy
		}
		readModel, observed, readReason = compileTarMediaReadModel(parameters, stored, geometry, boundary, valid.Load)
		learningMap = stored.TapePositionMap
		positionSource = stored.TapePositionMap.GetSource()
		if observed != nil {
			learningMap = observed
			positionSource = observed.Source
			updated, err := b.executor.Lib().UpdateMediaTapePositionMap(ctx, stored, observed)
			if err != nil {
				b.logger.WithContext(ctx).WithError(err).Warn("save observed Tape position map failed")
			} else {
				stored = updated
			}
		}
		if readModel != nil {
			// Completed-command learning may drain after unload invalidates the live Device map.
			// Its immutable geometry remains limited to this identified read-only Session.
			learningModel, _ = compileTarPositionMap(parameters, profile, learningMap, valid.Load)
		}
	} else if navigator == nil {
		readReason = "An eligible standard READ POSITION/LOCATE pairing is unavailable"
	}

	// Report the effective ordering decision without changing the frozen model mid-session.
	effectiveReadOrder := entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
	if readModel != nil && tapeReadCompletion(parameters) == nil {
		readReason = "Normal Tape unload timing is unavailable"
	}
	if readOrder == entity.TapeReadOrder_TAPE_READ_ORDER_AUTO && readModel != nil && tapeReadCompletion(parameters) != nil {
		effectiveReadOrder = readOrder
	}
	b.logger.WithContext(ctx).WithFields(map[string]any{
		"device": device, "requested_read_order": readOrder.String(),
		"effective_read_order": effectiveReadOrder.String(), "read_order_reason": readReason,
		"read_parameters_source": parameterSource, "position_source": positionSource,
		"read_model_available": readModel != nil,
	}).Info("tar read order")
	optimization := &entity.TapePreparedRead{
		PreparedAtNs: time.Now().UnixNano(), JobId: b.jobID, MediaId: stored.ID, Identity: stored.Identity,
		RequestedOrder: readOrder.String(), EffectiveOrder: effectiveReadOrder.String(),
		ParametersSource: parameterSource, ParametersReference: parameters.GetSourceReference(),
		ApplicabilityReason: parameterReason, PositionSource: positionSource, CoverageDetail: readReason,
	}
	if parameterSource == "learned" {
		for _, learned := range deviceSettings.GetLearnedReadParameters() {
			if learned.GetDevice() == device && proto.Equal(learned.GetParameters().GetAppliesTo(), parameters.GetAppliesTo()) {
				optimization.LearnedMaeSeconds = proto.Float64(learned.MaeSeconds)
				optimization.LearnedMaxErrorSeconds = proto.Float64(learned.MaxErrorSeconds)
				break
			}
		}
	}
	b.executor.recordPreparedRead(device, optimization)

	// Transfer the prepared reader and its observation lifetime to the content session.
	if parameterReason == "" {
		finishLearning = b.startTarReadLearning(ctx, prepared, device, stored.Identity, deviceSettings, parameters,
			learningModel, learningMap, initialPosition)
	}
	return mediapkg.NewTarReadSession(records, mediapkg.TarSessionOptions{
		Media: describeMedia(stored), Mode: records.Mode, TapeFile: 1, Cleanup: cleanup, Navigator: navigator,
		Finish:    func(ctx context.Context) error { return release(ctx, true) },
		ReadOrder: readOrder, ReadOrderReason: readReason, ReadModel: readModel,
		ReadPlanningBudget: settingspkg.TapeReadPlanningBudget(defaults),
		ReadOrderPlanned: func(order entity.TapeReadOrder, reason string) {
			b.executor.settlePreparedRead(device, optimization.PreparedAtNs, order, reason)
		},
		ReadTiming: timing, ReadInitialPosition: initialPosition, ReadInitialFileKnown: true,
		ReadCompletion: tapeReadCompletion(parameters),
	})
}

func (b *mediaBackend) newTarWriteSession(ctx context.Context, target *entity.ArchiveTapeTarget) (session mediapkg.WriteSession, returnErr error) {
	// Device cadence is attempt-local for FORMAT and APPEND, including direct backend calls.
	defaults, err := b.tapeDefaults(ctx, target.Device)
	if err != nil {
		return nil, err
	}
	interval := settingspkg.TapeCapacityPollInterval(defaults)

	// Registered profiles own APPEND; a registered identity cannot be silently reformatted.
	stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, target.Barcode)
	if err != nil {
		return nil, err
	}
	format := target.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT
	if format && stored != nil {
		return nil, fmt.Errorf("FORMAT Tape already exists in Library; delete it first")
	}
	if !format && (stored == nil || stored.DestroyedAtNS != nil || stored.Profile.GetTape().GetFormat() != library.TapeFormatTarPAXV1) {
		return nil, fmt.Errorf("tar APPEND Media is unavailable")
	}
	if !format {
		key, err := decodeTarKey(stored.Profile.GetTape().Encryption)
		if err != nil {
			return nil, err
		}
		clear(key)
	}

	// The attempt holds the device through initialization, adapter handoff and content settlement.
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, target.Device, onWait)
	}); err != nil {
		return nil, err
	}
	prepared, err := b.executor.openRawTape(ctx, b.jobID, target.Device)
	if err != nil {
		return nil, err
	}
	finishGeometry := func() (*entity.TapePositionMap, string) { return nil, "" }
	var descriptor *mediapkg.Descriptor
	release := func(ctx context.Context, complete bool) error {
		// Release the active descriptor before retaining any successfully observed position map.
		if prepared == nil {
			return nil
		}
		positionMap, reason := finishGeometry()
		err := b.executor.releaseRawTape(ctx, b.jobID, target.Device, prepared, complete)
		if err == nil {
			b.retainTarWriteGeometry(ctx, stored, descriptor, positionMap)
		}
		if reason != "" {
			b.logger.WithContext(ctx).WithField("reason", reason).Info("Tape WRITE geometry was not updated")
		}
		return err
	}
	cleanup := func(ctx context.Context) error { return release(ctx, false) }
	defer func() {
		if session == nil {
			returnErr = errors.Join(returnErr, cleanup(ctx))
		}
	}()
	if err := crossCheckTarBarcode(ctx, prepared, target.Barcode, format); err != nil {
		return nil, err
	}

	// Apply qualified recommendations to still-omitted controls before any mutation.
	operation := tape.Read
	if format {
		options := mergeTarOptions(prepared.Report().GetTape().GetRecommendedWriteOptions(), target.TarOptions)
		if err := settingspkg.ValidateTarWriteOptions(options); err != nil {
			return nil, err
		}
		stored = &library.Media{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: target.Barcode, Name: strings.TrimSpace(target.Name),
			CreatedAtNS: time.Now().UnixNano(), Profile: (&entity.TapeMediaProfile{
				Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
					InitializationId: uuid.NewString(), RecordMode: options.GetRecordMode(), RecordBytes: options.GetRecordBytes(),
					HardwareCompression: options.GetHardwareCompression(),
				},
			}).Pack(),
		}
	}
	if err := entity.ValidateTarMediaProfile(stored.Profile.GetTape()); err != nil {
		return nil, err
	}

	// FORMAT initializes the partition before any script can load a key or reposition the medium.
	profile := stored.Profile.GetTape().Tar
	if format {
		if _, err := prepared.Configure(ctx, profile, tape.Format); err != nil {
			return nil, err
		}
		if err := prepared.Initialize(ctx); err != nil {
			return nil, err
		}
		if err := assignTarBarcode(ctx, prepared, stored.Identity); err != nil {
			return nil, err
		}
	}

	// Run the existing adapter once before the fresh label or APPEND tail observations.
	err = b.executor.cleanupRawTape(ctx, b.jobID, target.Device, prepared)
	prepared = nil
	if err != nil {
		return nil, err
	}
	if err := b.prepareTarEncryption(ctx, target.Device, stored, format); err != nil {
		return nil, err
	}
	prepared, err = b.executor.openRawTape(ctx, b.jobID, target.Device)
	if err != nil {
		return nil, err
	}
	// FORMAT has assigned the barcode by now, so every reopened descriptor requires it.
	if err := crossCheckTarBarcode(ctx, prepared, stored.Identity, false); err != nil {
		return nil, err
	}
	if format {
		profile.EncryptionAlgorithm = observedTapeEncryptionAlgorithm(prepared.Report())
		operation = tape.Append
	}

	// Apply the cartridge record policy to the descriptor that will perform actual data I/O.
	b.logTarPolicy(ctx, target.Device, target.Mode.String(), stored.Profile.GetTape())
	records, err := prepared.Configure(ctx, profile, operation)
	if err != nil {
		return nil, err
	}
	if !format {
		b.logTarControls(ctx, "APPEND READ", prepared)
	}

	// A completed identity file or proven physical tail establishes the next standalone archive.
	file := uint64(1)
	var initialStop error
	complete := func(ctx context.Context) error {
		err := records.Filemarks(ctx, 1)
		if err == tape.ErrEarlyWarning {
			b.logger.WithContext(ctx).Warn("tar completion filemark reported early warning with confirmed count")
		}
		return err
	}
	if format {
		if err := records.Rewind(ctx); err != nil {
			return nil, err
		}
		b.logTarControls(ctx, "FORMAT", prepared)
		if err := mediapkg.WriteTarLabel(ctx, records, records.Mode, &mediapkg.TarLabel{
			Format: library.TapeFormatTarPAXV1, Barcode: stored.Identity, Name: stored.Name, Profile: stored.Profile.GetTape().Tar,
		}); err != nil {
			return nil, err
		}
		if err := complete(ctx); err != nil {
			if err != tape.ErrEarlyWarning {
				return nil, err
			}
			// The identity is physically complete; EW closes content without another data or mark command.
			initialStop = errors.Join(mediapkg.ErrTargetNoSpace, tape.ErrEarlyWarning)
			complete = func(context.Context) error { return nil }
		}
	} else {
		if err := records.Rewind(ctx); err != nil {
			return nil, err
		}
		label, err := mediapkg.ReadTarLabel(ctx, records, records.Mode)
		if err != nil {
			return nil, err
		}
		if err := validateTarLabel(label, stored); err != nil {
			return nil, err
		}

		started := time.Now()
		proven, err := prepared.VerifyAppendTail(ctx)
		if err != nil {
			return nil, err
		}
		file = proven
		b.logger.WithContext(ctx).WithFields(map[string]any{
			"device": target.Device, "elapsed_ms": time.Since(started).Milliseconds(),
			"next_archive": file,
		}).Info("tar APPEND tail verified")

		// Only a validated identity and proven tail may establish the immutable APPEND write controls.
		records, err = prepared.Configure(ctx, stored.Profile.GetTape().Tar, tape.Append)
		if err != nil {
			return nil, err
		}
		b.logTarControls(ctx, "APPEND WRITE", prepared)
	}

	// Prepare optional navigation and capacity only while this stream can still accept members.
	var navigator mediapkg.TarNavigator
	var capacity *mediapkg.TarCapacityOptions
	if initialStop == nil {
		navigator, err = b.tarNavigator(ctx, prepared)
		if err != nil {
			return nil, err
		}
		capacity, err = b.tarCapacity(ctx, prepared, interval)
		if err != nil {
			return nil, err
		}
	}

	// Transfer cleanup ownership only after the real Session is complete; test wrappers cannot replace preparation.
	descriptor = describeMedia(stored)
	session, err = mediapkg.NewTarWriteSession(records, mediapkg.TarSessionOptions{
		Media: descriptor, Mode: records.Mode, TapeFile: file,
		Complete: complete, Cleanup: cleanup, Stop: initialStop, Navigator: navigator, Capacity: capacity,
		Finish: func(ctx context.Context) error { return release(ctx, true) },
	})
	if err != nil {
		return nil, err
	}
	if initialStop == nil && navigator != nil {
		finishGeometry = b.startTarWriteGeometry(prepared, stored)
	}
	return session, nil
}

func (b *mediaBackend) tarCapacity(ctx context.Context, prepared *tape.Device, interval time.Duration) (*mediapkg.TarCapacityOptions, error) {
	// Obtain the real preparation observation before deriving the optional polling policy.
	provider, err := prepared.Capacity(ctx)
	if err != nil {
		return nil, err
	}

	// A missed optional preparation sample must not prevent later capacity samples. Only confirmed
	// unsupported commands disable recurrence; the prepared Device still owns fatal failures.
	if provider == nil {
		b.logger.WithContext(ctx).Info("tar capacity unknown; continuing with EW/error handling")
		return nil, nil
	}
	var observed *entity.DeviceFeature
	for _, feature := range prepared.Report().GetTape().GetFeatures() {
		if feature.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING {
			observed = feature
			break
		}
	}
	unsupported := observed.GetSupport() == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED
	sample := provider.Initial
	options := &mediapkg.TarCapacityOptions{
		Initial: sample,
		Cost: func(encodedBytes, _ uint64, _ uint32) (uint64, error) {
			return encodedBytes, nil
		},
	}
	// Logging belongs to the optional observer, after native settlement and outside WRITE locks.
	// Its failure count has the same single-worker lifetime as the observer itself.
	failures := uint64(0)
	logObservation := func(ctx context.Context, sample *tape.CapacitySample, issued bool, err error) {
		if !issued && err == nil || ctx.Err() == context.Canceled && (err == nil || tarCapacityCanceled(err)) {
			return
		}
		if err == nil && (sample == nil || sample.RemainingBytes == nil || sample.Quality == tape.CapacityUnknown) {
			err = errors.New("capacity sample returned no remaining-space value")
		}
		if err != nil {
			failures++
			b.logger.WithContext(ctx).WithField("consecutive_failures", failures).WithError(err).
				Warn("tar capacity sample failed")
			return
		}
		if failures != 0 {
			b.logger.WithContext(ctx).WithField("consecutive_failures", failures).
				Info("tar capacity sample recovered")
			failures = 0
		}
	}
	if interval > 0 && !unsupported {
		options.Interval = interval
		options.Observe = func(ctx context.Context, onIssue func() error) (*tape.CapacitySample, bool, error) {
			sample, issued, err := provider.Observe(ctx, onIssue)
			logObservation(ctx, sample, issued, err)
			return sample, issued, err
		}
	}
	if !unsupported && (sample == nil || sample.RemainingBytes == nil || sample.Quality == tape.CapacityUnknown) {
		var observationErr error
		if sample == nil && observed.GetDetail() != "" {
			observationErr = errors.New(observed.GetDetail())
		}
		logObservation(ctx, sample, true, observationErr)
	}
	if (sample == nil || sample.RemainingBytes == nil) && options.Observe == nil {
		b.logger.WithContext(ctx).Info("tar capacity unknown; continuing with EW/error handling")
		return nil, nil
	}

	// Report estimates and cadence without claiming bounded native cost, error, lag or latency.
	remaining := "unknown"
	partition := uint32(0)
	if sample != nil {
		partition = sample.Partition
	}
	if sample != nil && sample.RemainingBytes != nil {
		remaining = fmt.Sprintf("%d", *sample.RemainingBytes)
	}
	b.logger.WithContext(ctx).Infof(
		"tar capacity estimate, remaining_native_bytes=%s partition=%d observation_error=unknown unreflected_writes=unknown native_cost_bound=false poll_interval=%s",
		remaining, partition, options.Interval,
	)
	return options, nil
}

func tarCapacityCanceled(err error) bool {
	// A joined device failure must remain visible even when shutdown also canceled the query.
	if err == context.Canceled {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		if len(joined.Unwrap()) == 0 {
			return false
		}
		for _, cause := range joined.Unwrap() {
			if !tarCapacityCanceled(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return tarCapacityCanceled(wrapped.Unwrap())
	}
	return false
}

func (b *mediaBackend) tarNavigator(ctx context.Context, prepared *tape.Device) (mediapkg.TarNavigator, error) {
	// The prepared provider owns protocol eligibility independently of empirical physical qualification.
	navigator, err := prepared.Navigation(ctx)
	if err != nil {
		return nil, err
	}
	if navigator == nil {
		b.logger.WithContext(ctx).Info("tar member seek unavailable; using sequential navigation because an eligible standard SSC pair is unavailable")
		return nil, nil
	}
	b.logger.WithContext(ctx).Info("tar member seek uses standard SSC logical objects; physical validation is reported separately")
	return navigator, nil
}

func (b *mediaBackend) logTarPolicy(ctx context.Context, device, action string, profile *entity.TapeMediaProfile) {
	// Whitelist public recovery facts; encoded keys and full profiles never enter diagnostics.
	value := profile.Tar
	b.logger.WithContext(ctx).Infof(
		"prepare tar Tape, device=%q format=%q action=%s record_mode=%s record_bytes=%d immutable_write_compression=%t initialization_id=%q",
		device, profile.Format, action, value.RecordMode, value.RecordBytes, value.HardwareCompression,
		value.InitializationId,
	)
}

func (b *mediaBackend) logTarControls(ctx context.Context, action string, prepared *tape.Device) {
	// Log only observed public controls, keeping absence distinct from false or algorithm zero.
	report := prepared.Report().GetTape()
	compression, decompression, writeAlgorithm, readAlgorithm := "unknown", "unknown", "unknown", "unknown"
	if report.CompressionEnabled != nil {
		compression = fmt.Sprint(*report.CompressionEnabled)
	}
	if report.DecompressionEnabled != nil {
		decompression = fmt.Sprint(*report.DecompressionEnabled)
	}
	for _, feature := range report.Features {
		limits := feature.GetCompression()
		if limits == nil || limits.CurrentAlgorithm == nil {
			continue
		}
		switch feature.Feature {
		case entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION:
			writeAlgorithm = fmt.Sprint(*limits.CurrentAlgorithm)
		case entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION:
			readAlgorithm = fmt.Sprint(*limits.CurrentAlgorithm)
		}
	}
	b.logger.WithContext(ctx).Infof(
		"tar hardware controls, action=%q write_compression=%s read_decompression=%s observed_write_algorithm=%s observed_read_algorithm=%s",
		action, compression, decompression, writeAlgorithm, readAlgorithm,
	)
}

func crossCheckTarBarcode(ctx context.Context, prepared *tape.Device, expected string, format bool) error {
	// The cartridge memory barcode is the Tape identity; only FORMAT may precede an assigned barcode.
	value, err := prepared.Barcode(ctx)
	if err != nil {
		return err
	}
	barcode, err := normalizeReportedTapeBarcode(value)
	if err != nil {
		return err
	}
	if barcode == "" && !format {
		return fmt.Errorf("tar Tape identity is unavailable, requested=%q", expected)
	}
	if barcode != "" && barcode != expected {
		return fmt.Errorf("tar Tape barcode differs, requested=%q device=%q", expected, barcode)
	}
	return nil
}

// assignTarBarcode gives a FORMAT cartridge without a cartridge memory barcode the requested identity, as mkltfs does.
// An existing barcode was already cross-checked and is never rewritten; failure precedes any data write.
func assignTarBarcode(ctx context.Context, prepared *tape.Device, expected string) error {
	barcode, err := NormalizeTapeBarcode(expected)
	if err != nil {
		return err
	}
	value, err := prepared.Barcode(ctx)
	if err != nil {
		return err
	}
	reported, err := normalizeReportedTapeBarcode(value)
	if err != nil {
		return err
	}
	if reported != "" {
		if reported != barcode {
			return fmt.Errorf("tar Tape barcode differs, requested=%q device=%q", barcode, reported)
		}
		return nil
	}
	if err := prepared.WriteBarcode(ctx, barcode); err != nil {
		return fmt.Errorf("write tar Tape barcode failed, %w", err)
	}
	value, err = prepared.Barcode(ctx)
	if err != nil {
		return fmt.Errorf("read back tar Tape barcode failed, %w", err)
	}
	if value != barcode {
		return fmt.Errorf("tar Tape barcode readback differs, requested=%q device=%q", barcode, value)
	}
	return nil
}

func validateTarLabel(label *mediapkg.TarLabel, stored *library.Media) error {
	if label == nil || label.Format != library.TapeFormatTarPAXV1 || label.Barcode != stored.Identity ||
		!proto.Equal(label.Profile, stored.Profile.GetTape().Tar) {
		return fmt.Errorf("tar label identity or immutable profile differs from selected Media")
	}
	return nil
}

// prepareTarEncryption uses the existing adapter without retaining another copy of the key.
// Its caller owns the device lease and closes every raw descriptor before invoking it.
func (b *mediaBackend) prepareTarEncryption(ctx context.Context, device string, stored *library.Media, format bool) error {
	// FORMAT generates the cartridge key; READ and APPEND restore the saved one.
	tapeDir, err := b.executor.EnsureTapeWorkPath(ctx, b.jobID, stored.Identity)
	if err != nil {
		return err
	}

	// Keep the same key representation, temporary file and script context used by LTFS.
	var keyPath string
	var recycle func()
	if format {
		stored.Profile.GetTape().Encryption, keyPath, recycle, err = b.executor.NewKey()
	} else {
		keyPath, recycle, err = b.executor.RestoreKey(stored.Profile.GetTape().Encryption)
	}
	if err != nil {
		return err
	}
	defer recycle()
	return b.configureTape(ctx, device, keyPath, stored.Identity, stored.Name, tapeDir)
}

// observedTapeEncryptionAlgorithm names only one unambiguous current algorithm from an existing report.
func observedTapeEncryptionAlgorithm(report *entity.InspectDeviceResponse) *string {
	var name string
	for _, feature := range report.GetTape().GetFeatures() {
		limits := feature.GetEncryption()
		if limits == nil || limits.CurrentAlgorithmIndex == nil || limits.EncryptionMode == nil || limits.GetEncryptionMode() == 0 {
			continue
		}
		for _, algorithm := range limits.Algorithms {
			if algorithm.Index != limits.GetCurrentAlgorithmIndex() || algorithm.Name == "" {
				continue
			}
			if name != "" && name != algorithm.Name {
				return nil
			}
			name = algorithm.Name
		}
	}
	if name == "" {
		return nil
	}
	return &name
}
