package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/protobuf/proto"
)

type deviceSettingsKey struct{}

func (e *Executor) withDeviceSettings(ctx context.Context) (context.Context, error) {
	// Freeze Device settings at admission, before asynchronous work or a device wait.
	value, err := e.settings.Device.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Device Settings failed, %w", err)
	}
	return context.WithValue(ctx, deviceSettingsKey{}, proto.Clone(value).(*entity.DeviceSettings)), nil
}

func (b *mediaBackend) deviceSettings(ctx context.Context) (*entity.DeviceSettings, error) {
	// Production uses its admitted snapshot, including automatic models, throughout the attempt.
	value, _ := ctx.Value(deviceSettingsKey{}).(*entity.DeviceSettings)
	if value == nil && b.executor.settings != nil {
		var err error
		value, err = b.executor.settings.Device.Current(ctx)
		if err != nil {
			return nil, fmt.Errorf("read Device Settings failed, %w", err)
		}
	}
	return value, nil
}

func (b *mediaBackend) tapeDefaults(ctx context.Context, device string) (*entity.TapeDeviceSettings, error) {
	value, err := b.deviceSettings(ctx)
	if err != nil {
		return nil, err
	}
	return settingspkg.TapeDefaults(value, device), nil
}

func (b *mediaBackend) resolveTapeWrite(ctx context.Context, target *entity.ArchiveTapeTarget) (*entity.ArchiveTapeTarget, error) {
	// Freeze the configured selector and explicit choices before waiting for the drive.
	if target == nil {
		return nil, fmt.Errorf("Tape write target is missing")
	}
	resolved := proto.Clone(target).(*entity.ArchiveTapeTarget)
	resolved.Device = strings.TrimSpace(resolved.Device)
	if !b.executor.configuredDevice(resolved.Device) {
		return nil, fmt.Errorf("Tape device is not configured, device=%q", resolved.Device)
	}
	barcode, err := entity.NormalizeTapeBarcode(resolved.Barcode)
	if err != nil {
		return nil, err
	}
	resolved.Barcode = barcode

	// FORMAT consumes device defaults; APPEND resolves its immutable registered profile.
	switch resolved.Mode {
	case entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT:
		defaults := &entity.TapeDeviceSettings{}
		if resolved.Format == nil || resolved.GetFormat() == library.TapeFormatTarPAXV1 {
			var err error
			defaults, err = b.tapeDefaults(ctx, resolved.Device)
			if err != nil {
				return nil, err
			}
		}
		if resolved.Format == nil {
			resolved.Format = proto.String(settingspkg.TapeDefaultFormat(defaults))
		}
		if resolved.GetFormat() == library.TapeFormatTarPAXV1 {
			resolved.TarOptions = mergeTarOptions(defaults.TarWriteOptions, resolved.TarOptions)
		}
	case entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND:
		stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
		if err != nil {
			return nil, err
		}
		if stored == nil || stored.DestroyedAtNS != nil || stored.Profile.GetTape() == nil {
			return nil, fmt.Errorf("APPEND requires available registered Tape Media")
		}
		format := stored.Profile.GetTape().Format
		if resolved.Format != nil && resolved.GetFormat() != format {
			return nil, fmt.Errorf("APPEND format differs from the immutable Tape profile")
		}
		resolved.Format = proto.String(format)
		if format == library.TapeFormatTarPAXV1 {
			if err := entity.ValidateTarMediaProfile(stored.Profile.GetTape()); err != nil {
				return nil, err
			}
			options := tarProfileOptions(stored.Profile.GetTape().Tar)
			if resolved.TarOptions != nil && !proto.Equal(options, mergeTarOptions(options, resolved.TarOptions)) {
				return nil, fmt.Errorf("APPEND options differ from the immutable Tape profile")
			}
			resolved.TarOptions = options
		}
	default:
		return nil, fmt.Errorf("Tape write mode is unsupported, mode=%s", resolved.Mode)
	}

	// A closed format dispatch preserves native adapters and rejects ignored tar controls.
	switch resolved.GetFormat() {
	case library.TapeFormatLTFSV1:
		if resolved.TarOptions != nil {
			return nil, fmt.Errorf("LTFS does not accept tar write options")
		}
	case library.TapeFormatTarPAXV1:
		if err := settingspkg.ValidateTarWriteOptions(resolved.TarOptions); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("Tape write format is unsupported, format=%q", resolved.GetFormat())
	}
	return resolved, nil
}

func mergeTarOptions(levels ...*entity.TarWriteOptions) *entity.TarWriteOptions {
	// Default to variable 512 KiB records; retain omitted compression for qualified recommendations.
	resolved := &entity.TarWriteOptions{
		RecordMode:  entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(),
		RecordBytes: proto.Uint32(512 << 10),
	}
	for _, level := range levels {
		if level == nil {
			continue
		}
		value := proto.Clone(level).(*entity.TarWriteOptions)
		if value.RecordMode != nil {
			resolved.RecordMode = value.RecordMode
		}
		if value.RecordBytes != nil {
			resolved.RecordBytes = value.RecordBytes
		}
		if value.HardwareCompression != nil {
			resolved.HardwareCompression = value.HardwareCompression
		}
	}
	return resolved
}

func tarProfileOptions(profile *entity.TarMediaProfile) *entity.TarWriteOptions {
	profile = proto.Clone(profile).(*entity.TarMediaProfile)
	return &entity.TarWriteOptions{
		RecordMode: profile.RecordMode.Enum(), RecordBytes: proto.Uint32(profile.RecordBytes),
		HardwareCompression: proto.Bool(profile.HardwareCompression),
	}
}

// formatTarOptions applies qualified recommendations to still-omitted FORMAT controls.
func formatTarOptions(report *entity.InspectDeviceResponse, options *entity.TarWriteOptions) (*entity.TarWriteOptions, error) {
	resolved := mergeTarOptions(report.GetTape().GetRecommendedWriteOptions(), options)
	return resolved, settingspkg.ValidateTarWriteOptions(resolved)
}

// checkTarWriteMedia admits FORMAT only for an unregistered identity and APPEND only for usable tar Media.
func checkTarWriteMedia(stored *library.Media, target *entity.ArchiveTapeTarget) error {
	if target.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT {
		if stored != nil {
			return fmt.Errorf("Tape already exists in the Library; delete its Media metadata explicitly before formatting, barcode=%q", target.Barcode)
		}
		return nil
	}
	if stored == nil || stored.DestroyedAtNS != nil || stored.Profile.GetTape().GetFormat() != library.TapeFormatTarPAXV1 {
		return fmt.Errorf("tar APPEND Media is unavailable")
	}
	key, err := decodeTarKey(stored.Profile.GetTape().Encryption)
	if err != nil {
		return err
	}
	clear(key)
	return nil
}

// tarWriteDeviceDetail names a known physical blocker; optional diagnostic failures do not block a write.
func tarWriteDeviceDetail(report *entity.InspectDeviceResponse) string {
	tape := report.GetTape()
	blocked := report.GetDevice().GetBusy() || tape != nil && (tape.MediumPresent != nil && !tape.GetMediumPresent() || tape.GetWriteProtected())
	switch report.GetReason() {
	case entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED,
		entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE, entity.DeviceReason_DEVICE_REASON_NOT_IMPLEMENTED:
	default:
		blocked = true
	}
	if !blocked {
		return ""
	}
	return "Tape device is unavailable: " + report.GetDetail()
}

// CheckTarWrite answers a requested tar write with the resolution and Media checks of its attempt.
// The answer is advisory: the attempt repeats every check under its device lease.
func (e *Executor) CheckTarWrite(ctx context.Context, report *entity.InspectDeviceResponse, target *entity.ArchiveTapeTarget) error {
	resolved, err := (&mediaBackend{executor: e}).resolveTapeWrite(ctx, target)
	if err != nil {
		report.WriteDetail = err.Error()
		return nil
	}
	if resolved.GetFormat() != library.TapeFormatTarPAXV1 {
		report.WriteDetail = fmt.Sprintf("device inspection checks only tar writes, format=%q", resolved.GetFormat())
		return nil
	}

	// FORMAT options are those the leased attempt derives from this report's recommendations.
	report.ResolvedTarOptions = resolved.TarOptions
	if resolved.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT {
		report.ResolvedTarOptions, err = formatTarOptions(report, resolved.TarOptions)
		if err != nil {
			report.WriteDetail = err.Error()
			return nil
		}
	}
	stored, err := e.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, resolved.Barcode)
	if err != nil {
		return err
	}
	if err := checkTarWriteMedia(stored, resolved); err != nil {
		report.WriteDetail = err.Error()
		return nil
	}
	if detail := tarWriteDeviceDetail(report); detail != "" {
		report.WriteDetail = detail
		return nil
	}
	report.Writable = true
	return nil
}
