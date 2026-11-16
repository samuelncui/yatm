package settings

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// ValidateDevice validates saved Device settings, including currently inactive configured entries.
func ValidateDevice(value *entity.DeviceSettings) error {
	// Stored keys retain deployment spelling and never grant access to a device.
	seen := make(map[string]bool, len(value.GetDevices()))
	for _, config := range value.GetDevices() {
		key := config.GetDevice()
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("Device entry is blank")
		}
		if seen[key] {
			return fmt.Errorf("Device entry is duplicated, device=%q", key)
		}
		seen[key] = true
		tape := config.GetTape()
		if tape == nil {
			return fmt.Errorf("Device Tape settings are missing, device=%q", key)
		}
		if tape.DefaultFormat != nil && tape.GetDefaultFormat() != "ltfs_v1" && tape.GetDefaultFormat() != "tar_pax_v1" {
			return fmt.Errorf("Device default format is unsupported, device=%q", key)
		}
		if tape.GetReadOrder() != entity.TapeReadOrder_TAPE_READ_ORDER_UNSPECIFIED && tape.GetReadOrder() != entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL && tape.GetReadOrder() != entity.TapeReadOrder_TAPE_READ_ORDER_AUTO {
			return fmt.Errorf("Device Tape read order must be LOGICAL or AUTO, device=%q", key)
		}
		if seconds := tape.GetCapacityPollIntervalSeconds(); seconds > 0 && seconds < 5 {
			return fmt.Errorf("Device capacity polling interval must be zero or at least 5 seconds, device=%q", key)
		}
		if tape.ReadPlanningSeconds != nil && tape.GetReadPlanningSeconds() == 0 {
			return fmt.Errorf("Device read planning seconds must be positive, device=%q", key)
		}
		if err := ValidateTarWriteOptions(tape.TarWriteOptions); err != nil {
			return fmt.Errorf("Device tar options are invalid, device=%q, %w", key, err)
		}
		densities := make(map[uint32]bool, len(tape.ReadParameters))
		for _, parameters := range tape.ReadParameters {
			if err := ValidateTapeReadParameters(parameters); err != nil {
				return fmt.Errorf("Device read parameters are invalid, device=%q, %w", key, err)
			}
			density := parameters.GetAppliesTo().GetDensityCode()
			if densities[density] {
				return fmt.Errorf("Device read parameters repeat density code %d, device=%q", density, key)
			}
			densities[density] = true
		}
	}
	return nil
}

// ValidateTapeReadParameters checks one parameter set: it must name its density code, and supplied
// measurements must be usable. Missing measurements stay unknown; completeness belongs to the
// prepared read session, independently for each method.
func ValidateTapeReadParameters(value *entity.TapeReadParameters) error {
	if value == nil {
		return fmt.Errorf("read parameters are empty")
	}
	scope := value.AppliesTo
	if scope == nil || scope.DensityCode == nil {
		return fmt.Errorf("read parameters density_code is required")
	}
	if scope.GetDensityCode() > 255 {
		return fmt.Errorf("read parameters density_code must be between 0 and 255")
	}
	if scope.DriveSerial != nil && strings.TrimSpace(scope.GetDriveSerial()) == "" {
		return fmt.Errorf("read parameters drive_serial must be nonempty when supplied")
	}

	if geometry := value.Geometry; geometry != nil && geometry.NominalWraps != nil && geometry.GetNominalWraps() == 0 {
		return fmt.Errorf("read parameters nominal_wraps must be positive")
	}

	// Positioning terms and additional READ time cannot be negative.
	if positioning := value.Positioning; positioning != nil {
		for _, field := range []struct {
			name  string
			value *float64
		}{
			{"base_seconds", positioning.BaseSeconds},
			{"approach_reversal_seconds", positioning.ApproachReversalSeconds},
			{"seek_seconds_per_wrap", positioning.SeekSecondsPerWrap},
		} {
			if err := validateReadSeconds(field.name, field.value); err != nil {
				return err
			}
		}
	}
	if reading := value.Reading; reading != nil {
		if rate := reading.BytesPerSecond; rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate <= 0) {
			return fmt.Errorf("read parameters bytes_per_second must be finite and positive")
		}
		if err := validateReadSeconds("filemark_overhead_seconds", reading.FilemarkOverheadSeconds); err != nil {
			return err
		}
	}
	if completion := value.Completion; completion != nil {
		if completion.FullSpanSeconds != nil && completion.GetFullSpanSeconds() <= 0 {
			return fmt.Errorf("read parameters full_span_seconds must be positive")
		}
		for _, field := range []struct {
			name  string
			value *float64
		}{
			{"completion.full_span_seconds", completion.FullSpanSeconds},
			{"completion.fixed_seconds", completion.FixedSeconds},
			{"completion.ltfs_unmount_seconds", completion.LtfsUnmountSeconds},
		} {
			if err := validateReadSeconds(field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReadSeconds(name string, value *float64) error {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || math.Abs(*value) >= float64(math.MaxInt64)/float64(time.Second) {
		return fmt.Errorf("read parameters %s must be finite and representable as a duration", name)
	}
	if *value < 0 {
		return fmt.Errorf("read parameters %s must be nonnegative", name)
	}
	return nil
}

// ValidateTarWriteOptions checks optional write defaults without substituting runtime values.
func ValidateTarWriteOptions(value *entity.TarWriteOptions) error {
	if value == nil {
		return nil
	}
	if value.RecordMode != nil && value.GetRecordMode() != entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED && value.GetRecordMode() != entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE {
		return fmt.Errorf("tar record mode must be FIXED or VARIABLE")
	}
	if value.RecordBytes != nil && (value.GetRecordBytes() == 0 || value.GetRecordBytes()%512 != 0) {
		return fmt.Errorf("tar record bytes must be a positive multiple of 512")
	}
	return nil
}

// TapeDefaults returns an isolated preference for one deployment-configured spelling.
func TapeDefaults(value *entity.DeviceSettings, device string) *entity.TapeDeviceSettings {
	for _, config := range value.GetDevices() {
		if config.GetDevice() == device && config.GetTape() != nil {
			return proto.Clone(config.GetTape()).(*entity.TapeDeviceSettings)
		}
	}
	return &entity.TapeDeviceSettings{}
}

// TapeDefaultFormat resolves the tar default for an unset preference.
func TapeDefaultFormat(value *entity.TapeDeviceSettings) string {
	if value.GetDefaultFormat() == "" {
		return "tar_pax_v1"
	}
	return value.GetDefaultFormat()
}

// TapeDefaultReadOrder resolves the logical baseline for an unset preference.
func TapeDefaultReadOrder(value *entity.TapeDeviceSettings) entity.TapeReadOrder {
	if value.GetReadOrder() == entity.TapeReadOrder_TAPE_READ_ORDER_UNSPECIFIED {
		return entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
	}
	return value.GetReadOrder()
}

// TapeCapacityPollInterval resolves the default cadence while preserving an explicit Off.
func TapeCapacityPollInterval(value *entity.TapeDeviceSettings) time.Duration {
	if value == nil || value.CapacityPollIntervalSeconds == nil {
		return 5 * time.Second
	}
	return time.Duration(value.GetCapacityPollIntervalSeconds()) * time.Second
}

// TapeReadPlanningBudget resolves the attempt's frozen whole-Tape planning allowance.
func TapeReadPlanningBudget(value *entity.TapeDeviceSettings) time.Duration {
	if value == nil || value.ReadPlanningSeconds == nil {
		return 300 * time.Second
	}
	return time.Duration(value.GetReadPlanningSeconds()) * time.Second
}
