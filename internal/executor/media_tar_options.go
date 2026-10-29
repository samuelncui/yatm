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

func (b *mediaBackend) resolveTapeReadOrder(ctx context.Context, device string) (entity.TapeReadOrder, error) {
	defaults, err := b.tapeDefaults(ctx, device)
	if err != nil {
		return 0, err
	}
	return settingspkg.TapeDefaultReadOrder(defaults), nil
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
	barcode, err := NormalizeTapeBarcode(resolved.Barcode)
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
