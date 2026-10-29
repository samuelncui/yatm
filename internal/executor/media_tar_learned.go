package executor

import (
	"github.com/samuelncui/yatm/entity"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/protobuf/proto"
)

func selectTarReadParameters(value *entity.DeviceSettings, device string, report *entity.InspectDeviceResponse,
	profile *entity.TapeMediaProfile,
) (*entity.TapeReadParameters, string) {
	return selectTapeReadParameters(value, device, report, observedTarReadApplicability(report, profile))
}

func selectTapeReadParameters(value *entity.DeviceSettings, device string, report *entity.InspectDeviceResponse,
	scope *entity.TapeReadApplicability,
) (*entity.TapeReadParameters, string) {
	// Explicit user parameters retain their presence and unknown values; learning cannot replace them.
	if manual := settingspkg.TapeDefaults(value, device).GetReadParameters(); manual != nil {
		return proto.Clone(manual).(*entity.TapeReadParameters), "manual"
	}

	// Learning is read from the admission snapshot and must match every observed configuration field.
	for _, model := range value.GetLearnedReadParameters() {
		if model.GetDevice() == device && tapeReadParametersReason(model.GetParameters(), scope) == "" {
			return proto.Clone(model.GetParameters()).(*entity.TapeReadParameters), "learned"
		}
	}
	if parameters := builtinTapeReadParameters(report, scope); parameters != nil {
		return parameters, "builtin"
	}
	return nil, "unavailable"
}
