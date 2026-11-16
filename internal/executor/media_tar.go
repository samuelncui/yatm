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
	return e.rawTape(ctx, jobID, device, false)
}

// loadRawTape opens the leased device like openRawTape after loading its cartridge, so tar work and
// Calibration pick up a cartridge an earlier step ejected, as the LTFS readinfo adapter does.
func (e *Executor) loadRawTape(ctx context.Context, jobID int64, device string) (*tape.Device, error) {
	return e.rawTape(ctx, jobID, device, true)
}

func (e *Executor) rawTape(ctx context.Context, jobID int64, device string, load bool) (*tape.Device, error) {
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
	open := tape.NewDevice
	if load {
		open = tape.LoadDevice
	}
	prepared, err := open(ctx, resolved.identity, transport)
	if errors.Is(err, tape.ErrUncertain) {
		e.keepTapeDeviceUnavailable(jobID, device)
	}
	return prepared, err
}

// cleanupRawTape releases a descriptor that only looked or hands the drive to a Tape script; the
// cartridge stays loaded.
func (e *Executor) cleanupRawTape(ctx context.Context, jobID int64, device string, prepared *tape.Device) error {
	return e.releaseRawTape(ctx, jobID, device, prepared, false)
}

// releaseRawTape restores the descriptor's controls and, with unload, then unloads the cartridge.
// A Media step unloads whether its work succeeded, failed or was cancelled, as the LTFS unmount does.
func (e *Executor) releaseRawTape(ctx context.Context, jobID int64, device string, prepared *tape.Device, unload bool) error {
	// Cleanup is a mandatory gate, including controls changed before a Session could be returned.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	finish := prepared.Cleanup
	if unload {
		finish = prepared.Finish
	}
	err := finish(cleanup)
	if err != nil {
		e.keepTapeDeviceUnavailable(jobID, device)
	}
	return err
}

func (b *mediaBackend) newTarReadSession(ctx context.Context, access *entity.ReadTapeTarget, media *mediapkg.Descriptor) (session mediapkg.ReadSession, returnErr error) {
	// The Job admitted this Media's identity, profile and key before any hardware is reserved.
	if access == nil || media == nil {
		return nil, fmt.Errorf("tar read requires the selected Media")
	}
	device := strings.TrimSpace(access.Device)

	// Freeze model selection independently of the cartridge identity checks.
	deviceSettings, err := b.executor.deviceSettings(ctx)
	if err != nil {
		return nil, err
	}
	defaults := settingspkg.TapeDefaults(deviceSettings, device)
	readOrder := settingspkg.TapeDefaultReadOrder(defaults)

	// Preparation and all cleanup share the existing queued attempt lease.
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, device, onWait)
	}); err != nil {
		return nil, err
	}
	prepared, err := b.executor.loadRawTape(ctx, b.jobID, device)
	if err != nil {
		return nil, err
	}

	// One session owns release and invalidates its position map. A different cartridge stays loaded;
	// once its identity matches, every release unloads it.
	valid := new(atomic.Bool)
	valid.Store(true)
	admitted := false
	release := func(ctx context.Context) error {
		if prepared == nil {
			return nil
		}
		err := b.executor.releaseRawTape(ctx, b.jobID, device, prepared, admitted)
		valid.Store(false)
		return err
	}
	defer func() {
		if session == nil {
			returnErr = errors.Join(returnErr, release(ctx))
		}
	}()
	if err := crossCheckTarBarcode(ctx, prepared, media.Identity, false); err != nil {
		return nil, err
	}
	admitted = true

	// Give the adapter exclusive device access, then observe a fresh descriptor after its LOAD. A
	// failed script holds no descriptor, so the matching cartridge is unloaded through a new one.
	err = b.executor.cleanupRawTape(ctx, b.jobID, device, prepared)
	prepared = nil
	if err != nil {
		return nil, err
	}
	if err := b.prepareTarEncryption(ctx, device, media, false); err != nil {
		return nil, errors.Join(err, b.unloadAfterFailure(ctx, device, err))
	}
	prepared, err = b.executor.openRawTape(ctx, b.jobID, device)
	if err != nil {
		// No descriptor is held here either, so the admitted cartridge is unloaded through a new one.
		return nil, errors.Join(err, b.unloadAfterFailure(ctx, device, err))
	}
	if err := crossCheckTarBarcode(ctx, prepared, media.Identity, false); err != nil {
		return nil, err
	}

	// Apply the frozen record policy after the script hands the device back to a fresh descriptor.
	profile := media.Profile.GetTape().Tar
	records, err := prepared.Configure(ctx, profile, tape.Read)
	if err != nil {
		return nil, err
	}
	if err := records.Rewind(ctx); err != nil {
		return nil, err
	}
	label, err := mediapkg.ReadTarLabel(ctx, records, records.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateTarLabel(label, media); err != nil {
		return nil, err
	}
	navigator, err := b.tarNavigator(ctx, prepared)
	if err != nil {
		return nil, err
	}

	// Optional estimates use the admitted settings and the actual prepared drive, never its path.
	parameters, parameterSource, parameterReason := selectTapeReadParameters(deviceSettings, device, prepared.Report())
	var timing *entity.TapeReadTiming
	var initialPosition *entity.TapeBytePosition
	if parameters != nil {
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
	b.logTarPolicy(ctx, device, "READ", media.Profile.GetTape())

	// Both read orders share action costing; only AUTO exposes the optional ordering provider.
	var readModel *mediapkg.ReadCostModel
	positionSource := ""
	readReason := parameterReason
	if parameters != nil && navigator != nil {
		started := time.Now()
		geometry, err := prepared.Geometry(ctx)
		if err != nil {
			return nil, err
		}
		b.logReadDirectoryPreparation(ctx, device, started, prepared.Report(), "Tape read directory preparation")

		var positionMap *tapePositionMap
		readModel, positionMap, readReason = compileTarMediaReadModel(parameters, media, geometry, valid.Load)
		if positionMap != nil {
			positionSource = positionMap.source
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
		PreparedAtNs: time.Now().UnixNano(), JobId: b.jobID, MediaId: media.ID, Identity: media.Identity,
		RequestedOrder: readOrder.String(), EffectiveOrder: effectiveReadOrder.String(),
		ParametersSource: parameterSource, ParametersReference: parameters.GetSourceReference(),
		ApplicabilityReason: parameterReason, PositionSource: positionSource, CoverageDetail: readReason,
	}
	b.executor.recordPreparedRead(device, optimization)

	// Transfer the prepared reader to the content session.
	return mediapkg.NewTarReadSession(records, mediapkg.TarSessionOptions{
		Media: media, Mode: records.Mode, TapeFile: 1, Cleanup: release, Finish: release, Navigator: navigator,
		ReadOrder: readOrder, ReadOrderReason: readReason, ReadModel: readModel,
		ReadPlanningBudget: settingspkg.TapeReadPlanningBudget(defaults),
		ReadOrderPlanned: func(order entity.TapeReadOrder, reason string) {
			b.executor.settlePreparedRead(device, optimization.PreparedAtNs, order, reason)
		},
		ReadTiming: timing, ReadInitialPosition: initialPosition, ReadInitialFileKnown: true,
		ReadCompletion: tapeReadCompletion(parameters),
	})
}

func (b *mediaBackend) newTarWriteSession(ctx context.Context, target *entity.ArchiveTapeTarget, media *mediapkg.Descriptor) (session mediapkg.WriteSession, returnErr error) {
	// Device cadence is attempt-local for FORMAT and APPEND, including direct backend calls.
	defaults, err := b.executor.tapeDefaults(ctx, target.Device)
	if err != nil {
		return nil, err
	}
	interval := settingspkg.TapeCapacityPollInterval(defaults)

	// The Job admitted FORMAT for an unregistered identity or APPEND for its registered Media.
	format := target.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT
	if !format {
		if err := registeredMedia(media, entity.MediaKind_MEDIA_KIND_TAPE, target.Barcode); err != nil {
			return nil, err
		}
	}

	// The attempt holds the device through initialization, adapter handoff and content settlement.
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireTapeDevice(ctx, b.jobID, target.Device, onWait)
	}); err != nil {
		return nil, err
	}
	prepared, err := b.executor.loadRawTape(ctx, b.jobID, target.Device)
	if err != nil {
		return nil, err
	}
	// A different cartridge stays loaded; once its identity matches, every release unloads it.
	admitted := false
	release := func(ctx context.Context) error {
		if prepared == nil {
			return nil
		}
		return b.executor.releaseRawTape(ctx, b.jobID, target.Device, prepared, admitted)
	}
	defer func() {
		if session == nil {
			returnErr = errors.Join(returnErr, release(ctx))
		}
	}()
	if err := crossCheckTarBarcode(ctx, prepared, target.Barcode, format); err != nil {
		return nil, err
	}
	admitted = true

	// Resolve FORMAT options before any mutation.
	operation := tape.Read
	if format {
		options, err := formatTarOptions(target.TarOptions)
		if err != nil {
			return nil, err
		}
		media = &mediapkg.Descriptor{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: target.Barcode, Name: strings.TrimSpace(target.Name),
			CreateTime: time.Now(), Profile: (&entity.TapeMediaProfile{
				Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
					InitializationId: uuid.NewString(), RecordMode: options.GetRecordMode(), RecordBytes: options.GetRecordBytes(),
				},
			}).Pack(),
		}
		if err := entity.ValidateTarMediaProfile(media.Profile.GetTape()); err != nil {
			return nil, err
		}
	}

	// FORMAT initializes the partition before any script can load a key or reposition the medium.
	profile := media.Profile.GetTape().Tar
	if format {
		if _, err := prepared.Configure(ctx, profile, tape.Format); err != nil {
			return nil, err
		}
		if err := prepared.Initialize(ctx); err != nil {
			return nil, err
		}
		if err := assignTarBarcode(ctx, prepared, media.Identity); err != nil {
			return nil, err
		}
	}

	// Run the existing adapter once before the fresh label or APPEND tail observations. A failed
	// script holds no descriptor, so the matching cartridge is unloaded through a new one.
	err = b.executor.cleanupRawTape(ctx, b.jobID, target.Device, prepared)
	prepared = nil
	if err != nil {
		return nil, err
	}
	if err := b.prepareTarEncryption(ctx, target.Device, media, format); err != nil {
		return nil, errors.Join(err, b.unloadAfterFailure(ctx, target.Device, err))
	}
	prepared, err = b.executor.openRawTape(ctx, b.jobID, target.Device)
	if err != nil {
		return nil, errors.Join(err, b.unloadAfterFailure(ctx, target.Device, err))
	}
	// FORMAT has assigned the barcode by now, so every reopened descriptor requires it.
	if err := crossCheckTarBarcode(ctx, prepared, media.Identity, false); err != nil {
		return nil, err
	}
	if format {
		profile.EncryptionAlgorithm = observedTapeEncryptionAlgorithm(prepared.Report())
		operation = tape.Append
	}

	// Apply the cartridge record policy to the descriptor that will perform actual data I/O.
	b.logTarPolicy(ctx, target.Device, target.Mode.String(), media.Profile.GetTape())
	records, err := prepared.Configure(ctx, profile, operation)
	if err != nil {
		return nil, err
	}
	// A completed identity file or proven physical tail establishes the next standalone archive.
	file := uint64(1)
	var initialStop error
	complete := func(ctx context.Context, terminated bool) error {
		err := records.Filemarks(ctx, 1)
		switch err {
		case nil:
			b.logger.WithContext(ctx).Infof("tar Tape file completed, end_of_archive=%t filemark=written", terminated)
		case tape.ErrEarlyWarning:
			b.logger.WithContext(ctx).Warnf("tar Tape file completed, end_of_archive=%t filemark=written_at_early_warning", terminated)
		default:
			b.logger.WithContext(ctx).WithError(err).Errorf("tar Tape file completion failed, end_of_archive=%t", terminated)
		}
		return err
	}
	if format {
		if err := records.Rewind(ctx); err != nil {
			return nil, err
		}
		if err := mediapkg.WriteTarLabel(ctx, records, records.Mode, &mediapkg.TarLabel{
			Format: library.TapeFormatTarPAXV1, Barcode: media.Identity, Name: media.Name, Profile: media.Profile.GetTape().Tar,
		}); err != nil {
			return nil, err
		}
		if err := complete(ctx, true); err != nil {
			if err != tape.ErrEarlyWarning {
				return nil, err
			}
			// The identity is physically complete; EW closes content without another data or mark command.
			initialStop = errors.Join(mediapkg.ErrTargetNoSpace, tape.ErrEarlyWarning)
			complete = func(context.Context, bool) error { return nil }
		}
	} else {
		if err := records.Rewind(ctx); err != nil {
			return nil, err
		}
		label, err := mediapkg.ReadTarLabel(ctx, records, records.Mode)
		if err != nil {
			return nil, err
		}
		if err := validateTarLabel(label, media); err != nil {
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
		records, err = prepared.Configure(ctx, media.Profile.GetTape().Tar, tape.Append)
		if err != nil {
			return nil, err
		}
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
	session, err = mediapkg.NewTarWriteSession(records, mediapkg.TarSessionOptions{
		Media: media, Mode: records.Mode, TapeFile: file,
		Complete: complete, Cleanup: release, Finish: release, Stop: initialStop, Navigator: navigator, Capacity: capacity,
	})
	if err != nil {
		return nil, err
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
		"tar capacity estimate, remaining_native_bytes=%s partition=%d poll_interval=%s",
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
		"prepare tar Tape, device=%q format=%q action=%s record_mode=%s record_bytes=%d initialization_id=%q",
		device, profile.Format, action, value.RecordMode, value.RecordBytes, value.InitializationId,
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
	barcode, err := entity.NormalizeTapeBarcode(expected)
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

func validateTarLabel(label *mediapkg.TarLabel, media *mediapkg.Descriptor) error {
	if label == nil || label.Format != library.TapeFormatTarPAXV1 || label.Barcode != media.Identity ||
		!proto.Equal(label.Profile, media.Profile.GetTape().Tar) {
		return fmt.Errorf("tar label identity or immutable profile differs from selected Media")
	}
	return nil
}

// prepareTarEncryption uses the existing adapter without retaining another copy of the key.
// Its caller owns the device lease and closes every raw descriptor before invoking it.
func (b *mediaBackend) prepareTarEncryption(ctx context.Context, device string, media *mediapkg.Descriptor, format bool) error {
	// FORMAT generates the cartridge key; READ and APPEND restore the saved one.
	tapeDir, err := b.executor.EnsureTapeWorkPath(ctx, b.jobID, media.Identity)
	if err != nil {
		return err
	}

	// Keep the same key representation, temporary file and script context used by LTFS.
	var keyPath string
	var recycle func()
	if format {
		media.Profile.GetTape().Encryption, keyPath, recycle, err = b.executor.NewKey()
	} else {
		keyPath, recycle, err = b.executor.RestoreKey(media.Profile.GetTape().Encryption)
	}
	if err != nil {
		return err
	}
	defer recycle()
	return b.configureTape(ctx, device, keyPath, media.Identity, media.Name, tapeDir)
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
