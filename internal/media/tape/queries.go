package tape

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// passiveCommand is the read-only SG command/form allowlist. No load, motion, write, mode change,
// key loading, counter reset or measurement command can pass this boundary.
func passiveCommand(command Command) bool {
	// TEST UNIT READY is the sole no-data observation admitted here.
	if len(command.CDB) < 6 {
		return false
	}
	if len(command.CDB) == 6 && command.CDB[0] == 0 && command.Direction == NoData {
		for _, value := range command.CDB[1:] {
			if value != 0 {
				return false
			}
		}
		return len(command.Data) == 0
	}

	// Data observations retain the shared per-command allocation ceiling.
	if command.Direction != FromDevice || len(command.Data) == 0 || len(command.Data) > MaxQueryBytes {
		return false
	}
	cdb := command.CDB

	// Each opcode permits only its documented passive field combinations.
	switch cdb[0] {
	case 0x12:
		return len(cdb) == 6 && cdb[1]&0xfe == 0 && cdb[3] == 0 && int(cdb[4]) == len(command.Data) && cdb[5] == 0 &&
			(cdb[1] == 0 && cdb[2] == 0 || cdb[1] == 1 && (cdb[2] == 0x80 || cdb[2] == 0x83))
	case 5:
		return len(cdb) == 6 && cdb[1] == 0 && cdb[2] == 0 && cdb[3] == 0 && cdb[4] == 0 && cdb[5] == 0 && len(command.Data) == 6
	case 0x5a:
		return len(cdb) == 10 && cdb[1] == 0 && (cdb[2] == 0x3f || cdb[2] == 0x7f) && cdb[3] == 0 &&
			cdb[4] == 0 && cdb[5] == 0 && cdb[6] == 0 && int(binary.BigEndian.Uint16(cdb[7:9])) == len(command.Data) && cdb[9] == 0
	case 0x3c:
		if len(cdb) != 10 || cdb[2] != 0x10 || cdb[9] != 0 {
			return false
		}
		offset := int(cdb[3])<<16 | int(cdb[4])<<8 | int(cdb[5])
		length := int(cdb[6])<<16 | int(cdb[7])<<8 | int(cdb[8])
		if length != len(command.Data) {
			return false
		}
		if cdb[1] == 3 {
			return offset == 0 && length == 4
		}
		return cdb[1] == 2 && offset+length <= cartridgeMemoryMaxBytes
	case 0x34:
		if len(cdb) != 10 || cdb[1] != 0 && cdb[1] != 6 {
			return false
		}
		for _, value := range cdb[2:] {
			if value != 0 {
				return false
			}
		}
		return cdb[1] == 0 && len(command.Data) == 20 || cdb[1] == 6 && len(command.Data) == 32
	case 0xa3:
		if len(cdb) == 12 && cdb[1] == 0x1f && cdb[2] == 0x45 {
			return (cdb[3] == 0 && len(command.Data) == 10 || cdb[3] == 2) && cdb[4] == 0 && cdb[5] == 0 &&
				int(binary.BigEndian.Uint32(cdb[6:10])) == len(command.Data) && cdb[10] == 0 && cdb[11] == 0
		}
		return len(cdb) == 12 && cdb[1] == 0x0c &&
			(cdb[2] == 1 && cdb[4] == 0 && cdb[5] == 0 || cdb[2] == 2 && cdb[4] == 0 && (cdb[3] == 0x34 && (cdb[5] == 0 || cdb[5] == 6) || cdb[3] == 0xa3 && cdb[5] == 0x1f)) &&
			int(binary.BigEndian.Uint32(cdb[6:10])) == len(command.Data) && cdb[10] == 0 && cdb[11] == 0
	case 0x4d:
		return len(cdb) == 10 && cdb[1] == 0 && (cdb[2] == 0x4c || cdb[2] == 0x74) && cdb[3] == 0 && cdb[4] == 0 &&
			cdb[5] == 0 && cdb[6] == 0 && int(binary.BigEndian.Uint16(cdb[7:9])) == len(command.Data) && cdb[9] == 0
	case 0x8c:
		return len(cdb) == 16 && (cdb[1] == 0 || cdb[1] == 3) && cdb[2] == 0 && cdb[3] == 0 &&
			cdb[4] == 0 && cdb[5] == 0 && cdb[6] == 0 && cdb[7] == 0 &&
			int(binary.BigEndian.Uint32(cdb[10:14])) == len(command.Data) && cdb[14] == 0 && cdb[15] == 0
	case 0xa2:
		page := binary.BigEndian.Uint16(cdb[2:4])
		return len(cdb) == 12 && (cdb[1] == 0x20 && (page == 0 || page == 1 || page == 0x10 || page == 0x11 || page == 0x12 || page == 0x20) || cdb[1] == 0 && page == 0) && cdb[4] == 0 && cdb[5] == 0 &&
			int(binary.BigEndian.Uint32(cdb[6:10])) == len(command.Data) && cdb[10] == 0 && cdb[11] == 0
	}
	return false
}

func query(ctx context.Context, transport Transport, cdb []byte, length int) ([]byte, error) {
	// Allocation bounds belong here, before a malformed device can influence buffer size.
	if length < 0 || length > MaxQueryBytes {
		return nil, ErrMalformed
	}
	command := Command{CDB: cdb, Data: make([]byte, length), Direction: FromDevice, Timeout: 2 * time.Second}
	if length == 0 {
		command.Direction = NoData
	}
	if !passiveCommand(command) {
		return nil, fmt.Errorf("query is outside passive command allowlist")
	}
	if deadline, ok := ctx.Deadline(); ok {
		command.Timeout = min(command.Timeout, max(time.Millisecond, time.Until(deadline)))
	}
	response, err := transport.Execute(ctx, command)
	if err != nil {
		return nil, err
	}
	if err := CheckResponse(response); err != nil {
		return nil, err
	}
	if response.Residual < 0 || int(response.Residual) > length {
		return nil, &UncertainError{Cause: ErrMalformed}
	}
	return command.Data[:length-int(response.Residual)], nil
}

// Position records exact logical position only when the applicable READ POSITION validity bits permit it.
type Position struct {
	Partition                     uint32
	Object, File                  uint64
	ObjectKnown, FileKnown        bool
	Beginning, BeyondEarlyWarning bool
}

func decodePosition(data []byte, long bool) (Position, error) {
	var position Position
	if long {
		if len(data) != 32 {
			return Position{}, ErrMalformed
		}
		position = Position{
			Partition: binary.BigEndian.Uint32(data[4:8]), Object: binary.BigEndian.Uint64(data[8:16]),
			File: binary.BigEndian.Uint64(data[16:24]), ObjectKnown: data[0]&4 == 0, FileKnown: data[0]&8 == 0,
			Beginning: data[0]&0x80 != 0, BeyondEarlyWarning: data[0]&0x40 != 0,
		}
	} else {
		if len(data) != 20 {
			return Position{}, ErrMalformed
		}
		position = Position{
			Partition: uint32(data[1]), Object: uint64(binary.BigEndian.Uint32(data[4:8])), ObjectKnown: data[0]&6 == 0,
			Beginning: data[0]&0x80 != 0, BeyondEarlyWarning: data[0]&0x40 != 0,
		}
	}

	// Valid counters must agree with BOP and with filemarks being a subset of logical objects.
	if position.Beginning && (position.ObjectKnown && position.Object != 0 || position.FileKnown && position.File != 0) ||
		position.ObjectKnown && position.FileKnown && position.File > position.Object {
		return Position{}, ErrMalformed
	}
	return position, nil
}

func (r Records) Position(ctx context.Context, long bool) (position Position, err error) {
	defer func() { r.geometryError(err) }()
	cdb, length := []byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 20
	if long {
		cdb[1], length = 6, 32
	}
	data, err := query(ctx, r.Transport, cdb, length)
	if err != nil {
		return Position{}, err
	}
	return decodePosition(data, long)
}
