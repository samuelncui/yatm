package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CommandObservation contains completed command facts only, never payload bytes or predictions.
// Sequence gaps mean observations were dropped; a consumer must withdraw its inferred cursor.
type CommandObservation struct {
	Sequence      uint64
	Opcode        byte
	StartedAt     time.Time
	Elapsed       time.Duration
	Known         bool
	Bytes         int
	Filemark, EOD bool
	Discard       bool
	Position      Position
}

type readDiscardContextKey struct{}

// WithReadDiscard labels an actual READ requested while skipping unselected content. A physical
// record can also contain useful bytes; the label describes its trigger, not an exact waste count.
func WithReadDiscard(ctx context.Context) context.Context {
	return context.WithValue(ctx, readDiscardContextKey{}, true)
}

// CommandObservations is one bounded, lossy subscription to a prepared transport. Recording
// never waits for its consumer; Stop waits only for an already-issued native command to settle.
type CommandObservations struct {
	values    chan CommandObservation
	dropped   atomic.Uint64
	sequence  uint64
	mode      RecordMode
	transport *serializedTransport
	once      sync.Once
}

func (o *CommandObservations) Values() <-chan CommandObservation { return o.values }
func (o *CommandObservations) Dropped() uint64                   { return o.dropped.Load() }

func (o *CommandObservations) Stop() {
	o.once.Do(func() {
		// Taking the existing lane joins the final emitter before closing its channel.
		<-o.transport.lane
		o.transport.observations.CompareAndSwap(o, nil)
		close(o.values)
		o.transport.lane <- struct{}{}
	})
}

// ObserveCommands attaches after Configure and sends no device command. An opaque transport
// without native timing leaves learning unavailable without changing ordinary I/O.
func (d *Device) ObserveCommands() *CommandObservations {
	if d.operation == 0 {
		return nil
	}
	provider, ok := d.transport.(interface {
		observeCommands(RecordMode) *CommandObservations
	})
	if !ok {
		return nil
	}
	return provider.observeCommands(d.records.Mode)
}

func (t *serializedTransport) observeCommands(mode RecordMode) *CommandObservations {
	// Exactly one prepared-session consumer shares this native owner's completed-command facts.
	o := &CommandObservations{values: make(chan CommandObservation, 256), mode: mode, transport: t}
	if !t.observations.CompareAndSwap(nil, o) {
		return nil
	}
	return o
}

func (o *CommandObservations) record(command Command, response Response, commandErr error, started time.Time, elapsed time.Duration) {
	// Passive queries other than READ POSITION do not change a sequential cursor or train timings.
	opcode := command.CDB[0]
	if opcode != 0x34 && passiveCommand(command) {
		return
	}
	o.sequence++
	value := CommandObservation{Sequence: o.sequence, Opcode: opcode, StartedAt: started, Elapsed: elapsed, Discard: command.readDiscard}
	if response.Completed && commandErr == nil {
		o.decode(&value, command, response)
	}
	select {
	case o.values <- value:
	default:
		o.dropped.Add(1)
	}
}

func (o *CommandObservations) decode(value *CommandObservation, command Command, response Response) {
	// Use the same record and position decoders as the physical caller, including short records and marks.
	switch value.Opcode {
	case 0x08, 0x0a:
		writing := value.Opcode == 0x0a
		result, err := decodeRecord(response, len(command.Data), o.mode, writing)
		value.Known = result.AcceptedKnown && (err == nil || !writing && (result.Filemark || result.EOD) ||
			writing && result.EarlyWarning && WriteWarning(err, int64(len(command.Data)-result.Bytes), o.mode))
		value.Bytes, value.Filemark, value.EOD = result.Bytes, result.Filemark, result.EOD
	case 0x34:
		if CheckResponse(response) != nil || response.Residual < 0 || int(response.Residual) > len(command.Data) {
			return
		}
		position, err := decodePosition(command.Data[:len(command.Data)-int(response.Residual)], command.CDB[1] == 6)
		value.Known, value.Position = err == nil && position.ObjectKnown, position
	case 0x2b, 0x92:
		if controlResponse(response) != nil || command.CDB[1] != 2 {
			return
		}
		value.Known, value.Position.ObjectKnown = true, true
		if value.Opcode == 0x2b && len(command.CDB) == 10 {
			value.Position.Partition = uint32(command.CDB[8])
			value.Position.Object = uint64(binary.BigEndian.Uint32(command.CDB[3:7]))
		} else if value.Opcode == 0x92 && len(command.CDB) == 16 {
			value.Position.Partition = uint32(command.CDB[3])
			value.Position.Object = binary.BigEndian.Uint64(command.CDB[4:12])
		} else {
			value.Known = false
		}
	case 1:
		value.Known = controlResponse(response) == nil && command.CDB[1]&1 == 0
	case 0x1b:
		// Only normal synchronous UNLOAD supplies completion timing; LOAD/HOLD/IMMED do not.
		value.Known = len(command.CDB) == 6 && command.Direction == NoData && len(command.Data) == 0 &&
			command.CDB[1] == 0 && command.CDB[2] == 0 && command.CDB[3] == 0 && command.CDB[4] == 0 &&
			command.CDB[5] == 0 && response.Residual == 0 && controlResponse(response) == nil
	case 0x55:
		// Successful restoration of owned record controls does not move the physical cursor.
		value.Known = response.Residual == 0 && controlResponse(response) == nil && nonmovingRestoration(command)
	case 0x10:
		// A sync-only mark command changes no cursor; a real mark invalidates the next-object inference.
		count := uint32(command.CDB[2])<<16 | uint32(command.CDB[3])<<8 | uint32(command.CDB[4])
		err := filemarkResponse(response, count, o.mode.WarningCounts)
		value.Known = count == 0 && (err == nil || errors.Is(err, ErrEarlyWarning))
	}
}

func nonmovingRestoration(command Command) bool {
	// Recognize only the synchronous forms used by normal session record-control restoration.
	cdb, data := command.CDB, command.Data
	if command.Direction != ToDevice {
		return false
	}
	switch cdb[0] {
	case 0x55:
		if len(cdb) != 10 || cdb[1] != 0x10 || cdb[2] != 0 || cdb[3] != 0 || cdb[4] != 0 ||
			cdb[5] != 0 || cdb[6] != 0 || int(binary.BigEndian.Uint16(cdb[7:9])) != len(data) || cdb[9] != 0 ||
			len(data) < 16 || data[0] != 0 || data[1] != 0 || data[4] != 0 || data[5] != 0 ||
			binary.BigEndian.Uint16(data[6:8]) != 8 {
			return false
		}
		pages, err := selectionPages(data)
		if err != nil {
			return false
		}
		length := 16
		for kind, page := range pages {
			if kind != 0x0f && kind != 0x10 {
				return false
			}
			length += len(page)
		}
		return length == len(data)

	}
	return false
}
