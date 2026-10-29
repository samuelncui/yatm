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
		if err := ValidateTapeReadParameters(tape.ReadParameters); err != nil {
			return fmt.Errorf("Device read parameters are invalid, device=%q, %w", key, err)
		}
	}
	// Automatic results are independently scoped and never create a user preference entry.
	models := make(map[struct{ device, scope string }]bool, len(value.GetLearnedReadParameters()))
	for _, model := range value.GetLearnedReadParameters() {
		if err := validateLearnedReadParameters(model); err != nil {
			return err
		}
		scope, err := proto.MarshalOptions{Deterministic: true}.Marshal(model.Parameters.AppliesTo)
		if err != nil {
			return fmt.Errorf("encode learned Tape applicability failed, %w", err)
		}
		key := struct{ device, scope string }{model.Device, string(scope)}
		if models[key] {
			return fmt.Errorf("learned read parameters applicability is duplicated, device=%q", model.GetDevice())
		}
		models[key] = true
	}
	return nil
}

// ValidateTapeReadParameters preserves partial measurements and checks only supplied facts.
// Completeness and applicability belong to the prepared read session, independently for each method.
func ValidateTapeReadParameters(value *entity.TapeReadParameters) error {
	if value == nil {
		return nil
	}

	// Applicability records measured conditions rather than substituting chooser defaults.
	if scope := value.AppliesTo; scope != nil {
		for name, text := range map[string]*string{"drive_serial": scope.DriveSerial, "firmware": scope.Firmware} {
			if text != nil && strings.TrimSpace(*text) == "" {
				return fmt.Errorf("read parameters %s must be nonempty when supplied", name)
			}
		}
		if scope.DensityCode != nil && scope.GetDensityCode() > 255 {
			return fmt.Errorf("read parameters density_code must be between 0 and 255")
		}
		if scope.RecordMode != nil && scope.GetRecordMode() != entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED &&
			scope.GetRecordMode() != entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE {
			return fmt.Errorf("read parameters record_mode must be FIXED or VARIABLE")
		}
		if scope.RecordBytes != nil && (scope.GetRecordBytes() == 0 || scope.GetRecordBytes()%512 != 0) {
			return fmt.Errorf("read parameters record_bytes must be a positive multiple of 512")
		}
		if scope.EncryptionAlgorithm != nil && *scope.EncryptionAlgorithm != "" &&
			strings.TrimSpace(*scope.EncryptionAlgorithm) == "" {
			return fmt.Errorf("read parameters encryption_algorithm must be empty for plaintext or name an algorithm")
		}
	}

	// Absolute LPOS bounds stay optional and retain the full uint64 address domain.
	if geometry := value.Geometry; geometry != nil {
		if geometry.NominalWraps != nil && geometry.GetNominalWraps() == 0 {
			return fmt.Errorf("read parameters nominal_wraps must be positive")
		}
		if geometry.MinLpos != nil && geometry.MaxLpos != nil && *geometry.MaxLpos <= *geometry.MinLpos {
			return fmt.Errorf("read parameters max_lpos must exceed min_lpos")
		}
	}

	// Signed CERN coefficients remain signed; error and additional READ time cannot be negative.
	if positioning := value.Positioning; positioning != nil {
		if positioning.LongitudinalSecondsPerLpos != nil && positioning.LongitudinalSecondsPerWrap != nil {
			return fmt.Errorf("read parameters longitudinal units must be either seconds per LPOS or seconds per wrap")
		}
		for _, field := range []struct {
			name   string
			value  *float64
			signed bool
		}{
			{"base_seconds", positioning.BaseSeconds, true},
			{"wrap_change_seconds", positioning.WrapChangeSeconds, true},
			{"band_change_seconds", positioning.BandChangeSeconds, true},
			{"landing_zone_change_seconds", positioning.LandingZoneChangeSeconds, true},
			{"direction_change_seconds", positioning.DirectionChangeSeconds, true},
			{"step_back_seconds", positioning.StepBackSeconds, true},
			{"longitudinal_seconds_per_lpos", positioning.LongitudinalSecondsPerLpos, false},
			{"longitudinal_seconds_per_wrap", positioning.LongitudinalSecondsPerWrap, false},
		} {
			if err := validateReadSeconds(field.name, field.value, field.signed); err != nil {
				return err
			}
		}
	}
	if reading := value.Reading; reading != nil {
		if rate := reading.BytesPerSecond; rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate <= 0) {
			return fmt.Errorf("read parameters bytes_per_second must be finite and positive")
		}
		for _, field := range []struct {
			name  string
			value *float64
		}{
			{"post_locate_read_overhead_seconds", reading.PostLocateReadOverheadSeconds},
			{"filemark_overhead_seconds", reading.FilemarkOverheadSeconds},
		} {
			if err := validateReadSeconds(field.name, field.value, false); err != nil {
				return err
			}
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
		} {
			if err := validateReadSeconds(field.name, field.value, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReadSeconds(name string, value *float64, signed bool) error {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || math.Abs(*value) >= float64(math.MaxInt64)/float64(time.Second) {
		return fmt.Errorf("read parameters %s must be finite and representable as a duration", name)
	}
	if !signed && *value < 0 {
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

// TapeDefaultFormat resolves the original LTFS default for an unset preference.
func TapeDefaultFormat(value *entity.TapeDeviceSettings) string {
	if value.GetDefaultFormat() == "" {
		return "ltfs_v1"
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

// TapePassiveReadLearning preserves an explicit Off while enabling ordinary-I/O learning by default.
func TapePassiveReadLearning(value *entity.TapeDeviceSettings) bool {
	return value == nil || value.PassiveReadLearning == nil || value.GetPassiveReadLearning()
}

// TapeReadPlanningBudget resolves the attempt's frozen whole-Tape planning allowance.
func TapeReadPlanningBudget(value *entity.TapeDeviceSettings) time.Duration {
	if value == nil || value.ReadPlanningSeconds == nil {
		return 300 * time.Second
	}
	return time.Duration(value.GetReadPlanningSeconds()) * time.Second
}
