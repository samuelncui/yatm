package executor

import (
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// recordPreparedRead stores a small prepared-session observation, never a reusable live cursor.
func (e *Executor) recordPreparedRead(device string, value *entity.TapePreparedRead) {
	if value == nil || !e.configuredDevice(device) {
		return
	}
	value = proto.Clone(value).(*entity.TapePreparedRead)
	e.deviceLock.Lock()
	defer e.deviceLock.Unlock()
	if e.preparedRead == nil {
		e.preparedRead = make(map[string]*entity.TapePreparedRead)
	}
	e.preparedRead[device] = value
}

func (e *Executor) lastPreparedRead(device string) *entity.TapePreparedRead {
	e.deviceLock.Lock()
	defer e.deviceLock.Unlock()
	if value := e.preparedRead[device]; value != nil {
		return proto.Clone(value).(*entity.TapePreparedRead)
	}
	return nil
}

// settlePreparedRead replaces the prepared record's effective order with the planner's outcome.
// A later Session's record for the same device is never overwritten.
func (e *Executor) settlePreparedRead(device string, preparedAt int64, order entity.TapeReadOrder, reason string) {
	e.deviceLock.Lock()
	defer e.deviceLock.Unlock()
	value := e.preparedRead[device]
	if value == nil || value.PreparedAtNs != preparedAt {
		return
	}
	value = proto.Clone(value).(*entity.TapePreparedRead)
	value.EffectiveOrder, value.ApplicabilityReason = order.String(), reason
	e.preparedRead[device] = value
}
