package media

import (
	"math"
	"time"

	"github.com/samuelncui/yatm/entity"
)

// ReadCompletion describes synchronous unload timing independently of LOCATE regression terms.
type ReadCompletion struct {
	FullSpanSeconds, FixedSeconds float64
}

// UnloadCost quotes return to physical BOT and fixed unload work without issuing a command.
// A final host-next EOD uses its retained interval scale, including a partial reverse wrap.
func (m *ReadCostModel) UnloadCost(from *entity.TapeBytePosition, parameters *ReadCompletion) (time.Duration, bool) {
	// Only a current position map and finite completion inputs can price the normal finish action.
	if parameters == nil || !m.Valid() || parameters.FullSpanSeconds <= 0 || parameters.FixedSeconds < 0 ||
		math.IsInf(parameters.FullSpanSeconds, 0) || math.IsInf(parameters.FixedSeconds, 0) {
		return 0, false
	}
	interval, numerator, known := m.position(from)
	if !known {
		return 0, false
	}

	// The reflected fraction is independent of LPOS origin and never stretches a partial wrap to EOD.
	coordinate := float64(numerator) / float64(interval.span)
	return readSeconds(parameters.FixedSeconds + coordinate*parameters.FullSpanSeconds)
}
