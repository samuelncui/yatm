package tape

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
)

func blockLength(mode []byte) uint32 {
	return uint32(mode[13])<<16 | uint32(mode[14])<<8 | uint32(mode[15])
}

func sessionMode(current, compression, configuration []byte, block uint32) []byte {
	// MODE SELECT has zero mode-data length, no write-protect bit and SP clear in its CDB.
	data := make([]byte, 16+len(compression)+len(configuration))
	data[3], data[7] = current[3]&0x7f, 8
	data[13], data[14], data[15] = byte(block>>16), byte(block>>8), byte(block)
	copy(data[16:], compression)
	if len(compression) != 0 {
		data[16] &^= 0x80
	}
	copy(data[16+len(compression):], configuration)
	if len(configuration) != 0 {
		data[16+len(compression)] &^= 0x80
	}
	return data
}

func selectionPages(data []byte) (map[byte][]byte, error) {
	// Reuse the bounded mode parser for outgoing parameter lists whose mode-data length must be zero.
	copy := append([]byte(nil), data...)
	if len(copy) < 8 {
		return nil, ErrMalformed
	}
	binary.BigEndian.PutUint16(copy[:2], uint16(len(copy)-2))
	_, pages, err := modeData(copy)
	return pages, err
}

func (d *Device) selectMode(ctx context.Context, data []byte, verifyDecoder bool, writePolicy *bool) error {
	// Session MODE SELECT never saves defaults and owns its response before a readback can follow.
	desired, err := selectionPages(data)
	if err != nil {
		return err
	}
	cdb := make([]byte, 10)
	cdb[0], cdb[1] = 0x55, 0x10
	binary.BigEndian.PutUint16(cdb[7:9], uint16(len(data)))
	response, err := d.transport.Execute(ctx, Command{CDB: cdb, Direction: ToDevice, Data: data, Timeout: 2 * time.Minute,
		validate: func(response Response) error {
			if err := controlResponse(response); err != nil {
				return err
			}
			if response.Residual != 0 {
				return &UncertainError{Cause: ErrMalformed}
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	if response.Residual != 0 {
		return &UncertainError{Cause: ErrMalformed}
	}

	// Pending partition controls are verified without treating the mode-page count as physical layout.
	current, pages, err := d.readMode(ctx, false)
	if err != nil {
		return err
	}
	if d.original != nil {
		original, err := selectionPages(d.original)
		if err != nil {
			return err
		}
		if err := checkCapturedWriteAlgorithm(pages, original); err != nil {
			return err
		}
		if err := checkUntouchedCompression(pages, original, d.compressionChanged, d.selectorChanged); err != nil {
			return err
		}
	}
	if binary.BigEndian.Uint16(data[6:8]) == 0 {
		page := pages[0x11]
		if len(page) < 8 || page[4] != data[12] || current[3]&0x7f != data[3]&0x7f {
			return fmt.Errorf("partition control readback differs")
		}
		return nil
	}
	if len(current) < 16 || binary.BigEndian.Uint16(current[6:8]) != 8 || blockLength(current) != blockLength(data) || current[3]&0x7f != data[3]&0x7f {
		return fmt.Errorf("Tape session mode readback differs")
	}
	if verifyDecoder && (len(pages[0x0f]) < 16 || pages[0x0f][3]&0xe0 != 0x80) {
		return fmt.Errorf("required Tape decoder mode readback differs")
	}
	if writePolicy != nil {
		active, _ := compressionState(pages)
		if active == nil || *active != *writePolicy {
			return fmt.Errorf("required Tape write compression readback differs")
		}
	}
	return checkCompressionReadback(pages, desired)
}

func (d *Device) restoreMode(ctx context.Context) error {
	// Restore touched controls on fresh pages, retaining current partition/EOD and last-read algorithm facts.
	current, pages, err := d.readMode(ctx, false)
	if err != nil {
		return err
	}
	if len(current) < 16 || binary.BigEndian.Uint16(current[6:8]) != 8 {
		return fmt.Errorf("captured record mode is unavailable for restoration")
	}
	original, err := selectionPages(d.original)
	if err != nil {
		return err
	}
	if err := checkCapturedWriteAlgorithm(pages, original); err != nil {
		return err
	}
	if err := checkUntouchedCompression(pages, original, d.compressionChanged, d.selectorChanged); err != nil {
		return err
	}

	// Rebuild only owned mutable fields on the current pages, retaining unrelated reported facts.
	var compression, configuration []byte
	if d.compressionChanged != 0 {
		compression = append([]byte(nil), pages[0x0f]...)
		if len(compression) < 16 || len(original[0x0f]) < 16 {
			return fmt.Errorf("captured compression controls are unavailable")
		}
		writeMask, readMask := byte(d.compressionChanged), byte(d.compressionChanged>>8)
		compression[2] = compression[2]&^writeMask | original[0x0f][2]&writeMask
		compression[3] = compression[3]&^readMask | original[0x0f][3]&readMask
	}
	if d.selectorChanged {
		configuration = append([]byte(nil), pages[0x10]...)
		if len(configuration) < 16 || len(original[0x10]) < 16 {
			return fmt.Errorf("captured compression selector is unavailable")
		}
		configuration[14] = original[0x10][14]
	}

	// A settled rejection or unchanged reload needs no MODE SELECT; held/unready controls cannot be restored safely.
	if blockLength(current) == blockLength(d.original) && current[3]&0x7f == d.original[3] &&
		!compressionControlsDiffer(pages, original) {
		return nil
	}
	if d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY || !d.report.GetTape().GetMediumPresent() {
		return fmt.Errorf("Tape controls require a ready cartridge for restoration")
	}

	// Restore the captured settings through the existing checked mode-selection boundary.
	data := sessionMode(current, compression, configuration, blockLength(d.original))
	data[3] = d.original[3]
	return d.selectMode(ctx, data, false, nil)
}

func (d *Device) preparePartition(ctx context.Context, partition []byte, header byte) error {
	// Resolve FORMAT and delayed partition controls before preparing the known required transition.
	if partition[4]&4 == 0 {
		return fmt.Errorf("delayed partitioning is unavailable")
	}
	if err := d.requireCommand(ctx, []byte{4, 0, 1, 0, 0, 0}, false); err != nil {
		return err
	}
	_, changes, err := d.readMode(ctx, true)
	if err != nil {
		return err
	}
	changeable := changes[0x11]
	if len(changeable) != len(partition) {
		return fmt.Errorf("partition changeability is unknown")
	}
	page := append([]byte(nil), partition...)
	page[0] &^= 0x80
	page[3], page[4] = 0, page[4]&0x1c|0x40
	for i := 2; i < len(page); i++ {
		if (page[i]^partition[i])&^changeable[i] != 0 {
			return fmt.Errorf("required single-partition transition is unsupported")
		}
	}
	d.partition = append(make([]byte, 8), page...)
	d.partition[3] = header & 0x7f
	original := append([]byte(nil), partition...)
	original[0] &^= 0x80
	d.partitionOriginal = append(make([]byte, 8), original...)
	d.partitionOriginal[3] = header & 0x7f
	return nil
}
