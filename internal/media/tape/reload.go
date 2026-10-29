package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
)

func reloadCommand(load bool) []byte {
	value := byte(8)
	if load {
		value = 1
	}
	return []byte{0x1b, 0, 0, 0, value, 0}
}

func (d *Device) reloadApplicable() bool {
	// Scoped public command semantics establish HOLD/no-eject and the held-state discriminator.
	vendor, model := strings.TrimSpace(d.report.GetVendor()), strings.TrimSpace(d.report.GetModel())
	return (vendor == "HP" || vendor == "HPE") && strings.HasPrefix(model, "Ultrium") ||
		vendor == "IBM" && (strings.HasPrefix(model, "ULT3580") || strings.HasPrefix(model, "Ultrium"))
}

func (d *Device) reloadPrerequisites() bool {
	// Missing current identity or read observations cannot authorize a physical cycle.
	if !d.reloadApplicable() || !d.report.GetTape().GetMediumPresent() ||
		d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY ||
		d.report.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM ||
		d.report.Reason == entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN ||
		!d.geometry.Valid() || !d.geometry.AllWraps || len(d.geometry.Endpoints) == 0 || d.navigation == nil {
		return false
	}

	// Current logical navigation is reobserved after load; it does not require physical geometry.
	for _, kind := range []entity.DeviceFeatureKind{
		entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
		entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION,
	} {
		f := feature(d.report, kind)
		if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED ||
			f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY ||
			f.Reason != 0 || f.Validation == entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED {
			return false
		}
	}
	return true
}

func (d *Device) inspectReload(ctx context.Context, cipherOff bool) error {
	// No undocumented product family can derive HOLD semantics from an accepted bit mask alone.
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD)
	if !d.reloadApplicable() {
		f.Detail = "No applicable documented non-ejecting HOLD/LOAD protocol family is established"
		return nil
	}

	// One optional report must positively support both exact synchronous forms; no mandatory fallback applies.
	data, err := reportCommand(ctx, d.transport, 0x1b, nil)
	f.Detail = "Synchronous HOLD/LOAD support or current observation prerequisites are unavailable"
	if err != nil {
		f.Reason, f.Detail = Reason(err), err.Error()
		return err
	}
	f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
	f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
	for _, load := range []bool{false, true} {
		support, err := commandFormSupport(data, reloadCommand(load))
		if err != nil {
			f.Support, f.Reason, f.Detail = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, Reason(err), err.Error()
			return err
		}
		if support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			f.Support = support
			if support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
			}
			return nil
		}
	}

	// Eligibility is separate from physical validation, reconstruction, freshness and timing benefit.
	if cipherOff && d.reloadPrerequisites() {
		f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY, 0
		f.Detail = "Documented synchronous non-ejecting HOLD/LOAD forms and current prerequisites observed; physical validation and map freshness remain unknown"
	}
	return nil
}

// RefreshPositionMap performs one eligible non-ejecting HOLD/LOAD and captures a new session observation.
// It neither establishes REOWP reconstruction nor physical freshness, coverage or ordering benefit.
func (d *Device) RefreshPositionMap(ctx context.Context) (err error) {
	// Refresh precedes all session control/key preparation and cannot release or replace any foreign key.
	if err := ctx.Err(); err != nil {
		return err
	}
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD)
	if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED ||
		f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY || f.Reason != 0 ||
		f.Validation == entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED || !d.reloadPrerequisites() {
		return fmt.Errorf("non-ejecting HOLD/LOAD prerequisites are unavailable: %w", ErrUnsupported)
	}
	if d.operation != 0 || d.original != nil || d.prepared != nil || d.partition != nil ||
		d.changed || d.partitionChanged || d.compressionChanged != 0 || d.selectorChanged {
		return fmt.Errorf("position-map refresh must precede Tape control preparation")
	}
	if _, err := query(ctx, d.transport, []byte{0, 0, 0, 0, 0, 0}, 0); err != nil {
		return fmt.Errorf("confirm loaded Tape readiness failed, %w", err)
	}
	if err := d.requireEncryptionOff(ctx); err != nil {
		return err
	}
	// LOAD may reset session controls, so capture their original owner before any motion or later Configure.
	current, pages, err := d.readMode(ctx, false)
	if err != nil {
		return fmt.Errorf("capture pre-reload Tape controls failed, %w", err)
	}
	if len(current) < 16 || binary.BigEndian.Uint16(current[6:8]) != 8 || len(pages[0x10]) < 16 {
		return fmt.Errorf("pre-reload Tape controls are unavailable")
	}
	d.original = sessionMode(current, pages[0x0f], pages[0x10], blockLength(current))
	before := d.report

	// Withdraw every old map/navigation observation before the first possible motion.
	d.invalidateGeometry()
	d.geometry, d.navigation = nil, nil
	for _, kind := range []entity.DeviceFeatureKind{
		entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
		entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION,
	} {
		observed := feature(d.report, kind)
		observed.Availability, observed.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
		observed.Limits = nil
	}
	defer func() {
		if err != nil {
			d.invalidateGeometry()
			d.geometry, d.navigation = nil, nil
			for _, kind := range []entity.DeviceFeatureKind{
				entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
				entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION,
			} {
				observed := feature(d.report, kind)
				observed.Availability, observed.Reason, observed.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, Reason(err), err.Error()
				observed.Limits = nil
			}
			failed := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD)
			failed.Availability, failed.Reason, failed.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, Reason(err), err.Error()
		}
	}()

	// Each native command settles once; cancellation or failure never schedules compensating LOAD.
	d.changed = true
	if len(pages[0x0f]) >= 16 {
		d.compressionChanged = 0xe080
	}
	d.selectorChanged = true
	d.report.Reason = entity.DeviceReason_DEVICE_REASON_NOT_READY
	d.report.Detail = "Tape HOLD/LOAD has not yet reestablished ready session controls"
	if err := d.reload(ctx, false); err != nil {
		return fmt.Errorf("hold Tape failed, %w", err)
	}
	command := Command{CDB: []byte{0, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: 2 * time.Second, validate: heldTapeResponse}
	if deadline, ok := ctx.Deadline(); ok {
		command.Timeout = min(command.Timeout, max(time.Millisecond, time.Until(deadline)))
	}
	response, err := d.transport.Execute(ctx, command)
	if err != nil {
		return fmt.Errorf("confirm held Tape state failed, %w", err)
	}
	if err := heldTapeResponse(response); err != nil {
		return err
	}
	if err := d.reload(ctx, true); err != nil {
		return fmt.Errorf("load held Tape failed, %w", err)
	}

	// Reuse the full same-descriptor observation path; old readiness, controls, navigation and geometry never survive.
	if _, err := d.inspect(ctx, Identity{Configured: before.GetDevice().GetDevice(), Key: before.GetAccessIdentity()}); err != nil {
		return fmt.Errorf("observe reloaded Tape failed, %w", err)
	}
	for _, identity := range [][2]string{
		{before.GetVendor(), d.report.GetVendor()}, {before.GetModel(), d.report.GetModel()},
		{before.GetFirmware(), d.report.GetFirmware()}, {before.GetSerialNumber(), d.report.GetSerialNumber()},
	} {
		if identity[0] != "" && identity[0] != identity[1] {
			return fmt.Errorf("reloaded Tape drive identity changed")
		}
	}
	f = feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD)
	if !d.reloadPrerequisites() || f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED ||
		f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY || f.Reason != 0 {
		return fmt.Errorf("reloaded Tape observation prerequisites are unavailable")
	}
	if err := d.requireEncryptionOff(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

func heldTapeResponse(response Response) error {
	err := CheckResponse(response)
	failure, ok := err.(*CommandError)
	if ok && currentCommand(failure) && response.Residual == 0 && !failure.Sense.Filemark && !failure.Sense.EOM &&
		!failure.Sense.ILI && failure.Sense.Key == 2 && failure.Sense.ASC == 0x3a && failure.Sense.ASCQ == 4 {
		return nil
	}
	return errors.Join(err, fmt.Errorf("settled held/unthreaded Tape state was not confirmed"))
}

func (d *Device) reload(ctx context.Context, load bool) error {
	response, err := d.transport.Execute(ctx, Command{
		CDB: reloadCommand(load), Direction: NoData, Timeout: 10 * time.Minute, validate: controlResponse,
	})
	if err != nil {
		return err
	}
	return controlResponse(response)
}
