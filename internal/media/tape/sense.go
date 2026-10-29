package tape

import (
	"encoding/binary"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// Sense preserves current/deferred and sequential flags. Information is signed for ILI residuals.
type Sense struct {
	Deferred           bool
	Key, ASC, ASCQ     byte
	Filemark, EOM, ILI bool
	Information        int64
	InformationValid   bool
}

// DecodeSense accepts complete SPC fixed/descriptor responses and refuses truncated descriptors.
func DecodeSense(data []byte) (Sense, error) {
	// Validate the declared envelope before reading fields or walking descriptors.
	if len(data) < 8 || int(data[7])+8 > len(data) {
		return Sense{}, ErrMalformed
	}
	data = data[:int(data[7])+8]
	var sense Sense
	switch data[0] & 0x7f {
	case 0x70, 0x71:
		if len(data) < 14 {
			return Sense{}, ErrMalformed
		}
		sense.Deferred = data[0]&0x7f == 0x71
		sense.Key, sense.ASC, sense.ASCQ = data[2]&0xf, data[12], data[13]
		sense.Filemark, sense.EOM, sense.ILI = data[2]&0x80 != 0, data[2]&0x40 != 0, data[2]&0x20 != 0
		sense.InformationValid = data[0]&0x80 != 0
		sense.Information = int64(int32(binary.BigEndian.Uint32(data[3:7])))
	case 0x72, 0x73:
		if data[0]&0x80 != 0 {
			return Sense{}, ErrMalformed
		}
		sense.Deferred = data[0] == 0x73
		sense.Key, sense.ASC, sense.ASCQ = data[1]&0xf, data[2], data[3]
		information, stream := false, false
		for offset := 8; offset < len(data); {
			if len(data)-offset < 2 {
				return Sense{}, ErrMalformed
			}
			end := offset + 2 + int(data[offset+1])
			if end > len(data) {
				return Sense{}, ErrMalformed
			}
			descriptor := data[offset:end]
			switch descriptor[0] {
			case 0:
				if len(descriptor) != 12 || information {
					return Sense{}, ErrMalformed
				}
				sense.InformationValid = descriptor[2]&0x80 != 0
				sense.Information = int64(binary.BigEndian.Uint64(descriptor[4:12]))
				information = true
			case 4:
				if len(descriptor) != 4 || stream {
					return Sense{}, ErrMalformed
				}
				sense.Filemark, sense.EOM, sense.ILI = descriptor[3]&0x80 != 0, descriptor[3]&0x40 != 0, descriptor[3]&0x20 != 0
				stream = true
			}
			offset = end
		}
	default:
		return Sense{}, fmt.Errorf("unknown Tape sense response: %w", ErrMalformed)
	}
	return sense, nil
}

// CommandError preserves the exact command status and sense without pretending it transferred bytes.
type CommandError struct {
	Response Response
	Sense    Sense
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("Tape command failed, status=%02x sense=%x/%02x%02x deferred=%t", e.Response.Status, e.Sense.Key, e.Sense.ASC, e.Sense.ASCQ, e.Sense.Deferred)
}

// CheckResponse distinguishes known command rejection from missing or invalid completion evidence.
func CheckResponse(response Response) error {
	if !response.Completed {
		return &UncertainError{Cause: fmt.Errorf("Tape completion status is absent")}
	}
	if response.MessageStatus != 0 || response.HostStatus != 0 || response.DriverStatus&0xf != 0 && response.DriverStatus&0xf != 8 {
		return &UncertainError{Cause: fmt.Errorf("Tape host/driver status failed")}
	}
	if response.DriverStatus&0xf == 8 && len(response.Sense) == 0 {
		return &UncertainError{Cause: ErrMalformed}
	}
	if response.Status == 0 {
		if len(response.Sense) != 0 {
			return &UncertainError{Cause: ErrMalformed}
		}
		return nil
	}
	if response.Status != 2 {
		if response.Status == 0x18 {
			return &UncertainError{Cause: &CommandError{Response: response}}
		}
		return &CommandError{Response: response}
	}
	sense, err := DecodeSense(response.Sense)
	if err != nil {
		return &UncertainError{Cause: err}
	}
	if sense.Deferred || sense.Key == 6 && sense.ASC == 0x29 {
		return &UncertainError{Cause: &CommandError{Response: response, Sense: sense}}
	}
	return &CommandError{Response: response, Sense: sense}
}

// CleanUnsupported reports only known optional-command rejection, without another joined cause.
// Cancellation, malformed/deferred responses and unknown completion must remain real failures.
func CleanUnsupported(err error) bool {
	if err == ErrUnsupported {
		return true
	}
	if failure, ok := err.(*CommandError); ok {
		return rejectedCommand(failure) && Reason(failure) == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return CleanUnsupported(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !CleanUnsupported(cause) {
				return false
			}
		}
		return true
	}
	return false
}

func currentCommand(failure *CommandError) bool {
	response := failure.Response
	return response.Completed && response.Status == 2 && response.HostStatus == 0 && response.MessageStatus == 0 &&
		(response.DriverStatus&0xf == 0 || response.DriverStatus&0xf == 8) && !failure.Sense.Deferred
}
