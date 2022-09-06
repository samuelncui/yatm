package tapewriter

import (
	"archive/tar"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/benmcclelland/mtio"
	"github.com/benmcclelland/sgio"
	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"
)

const (
	MaxSense  = 255
	BlockSize = 512 * 1024
)

type position struct {
	partition uint8
	offset    uint64
}

type capacity struct {
	cap uint64
	all uint64
}

type Writer struct {
	*tar.Writer
	buffer    *BlockWriter
	tape      *os.File
	index     []*tar.Header
	blockSize int
	writen    uint64
	current   *position
}

func NewWriter(tape *os.File) (*Writer, error) {
	w := &Writer{
		tape:      tape,
		index:     make([]*tar.Header, 0, 16),
		blockSize: BlockSize,
		writen:    0,
		current:   new(position),
	}

	w.buffer = NewBlockWriter(w.tape, w.blockSize, 32)
	w.Writer = tar.NewWriter(w.buffer)

	if err := w.formatTape(); err != nil {
		return nil, err
	}
	if err := mtio.DoOp(w.tape, mtio.NewMtOp(mtio.WithOperation(mtio.MTSETBLK), mtio.WithCount(BlockSize))); err != nil {
		return nil, err
	}

	cap, err := w.readCapacity()
	if err != nil {
		return nil, err
	}
	spew.Dump(cap)

	return w, nil
}

func (w *Writer) Close() error {
	if err := w.Writer.Close(); err != nil {
		return err
	}

	if err := w.buffer.Close(); err != nil {
		return err
	}

	return nil
}

func (w *Writer) formatTape() error {
	// mode sense -> mode select -> format
	partitionMode := make([]byte, 32)
	if err := w.modeSense(TC_MP_MEDIUM_PARTITION, TC_MP_PC_CURRENT, 0x00, partitionMode, TC_MP_MEDIUM_PARTITION_SIZE); err != nil {
		return fmt.Errorf("read partition config fail, err= %w", err)
	}
	logrus.Infof("read partition mode success, '%x'", partitionMode)

	// Set appropriate values to the page and Issue Mode Select
	partitionMode[0] = 0x00
	partitionMode[1] = 0x00

	partitionMode[19] = 0x01
	partitionMode[20] = 0x20 | (partitionMode[20] & 0x1F) /* Set FDP=0, SDP=0, IDP=1 ==> User Setting */
	partitionMode[22] = 0x09                              /* Set partition unit as gigabytes (10^9) */

	partitionMode[24] = 0x00 /* Set Partition0 Capacity */
	partitionMode[25] = 1    /* will round up to minimum partition size */
	partitionMode[26] = 0xFF /* Set Partition1 Capacity */
	partitionMode[27] = 0xFF
	logrus.Infof("edit partition mode success, '%x'", partitionMode)

	pageLength := uint16(TC_MP_MEDIUM_PARTITION_SIZE)
	if partitionMode[17] > 0x0A {
		pageLength += uint16(partitionMode[17] - 0x0A)
	}

	if err := w.modeSelect(partitionMode, pageLength); err != nil {
		return fmt.Errorf("write partition mode fail, err= %w", err)
	}
	if err := w.formatPartition(); err != nil {
		return fmt.Errorf("format partition fail, err= %w", err)
	}
	if err := w.locate(&position{partition: 1, offset: 0}); err != nil {
		return fmt.Errorf("locate fail, err= %w", err)
	}

	return nil
}

// only for lto5
func (w *Writer) readCapacity() ([]*capacity, error) {
	buf := make([]byte, 1024)
	if err := w.logSense(LOG_TAPECAPACITY, 0, buf); err != nil {
		return nil, fmt.Errorf("read capacity fail, err= %w", err)
	}

	page, err := parseLogPage(buf)
	if err != nil {
		return nil, fmt.Errorf("parse log page fail, err= %w", err)
	}

	result := make([]*capacity, 2)
	for idx := range result {
		cap := page[uint16(idx+1)]
		all := page[uint16(len(result)+idx+1)]

		c := new(capacity)
		if len(cap) >= 4 {
			c.cap = uint64(binary.BigEndian.Uint32(cap))
		}
		if len(cap) >= 4 {
			c.all = uint64(binary.BigEndian.Uint32(all))
		}

		result[idx] = c
	}

	return result, nil
}

func (w *Writer) modeSense(page, pc, subpage uint8, buf []byte, size uint16) error {
	cdb := make([]uint8, 10)
	cdb[0] = SPCCodeModeSense10
	cdb[2] = pc | (page & 0x3F) // Current value
	cdb[3] = subpage
	cdb[7] = uint8(size << 8)
	cdb[8] = uint8(size)

	return w.sendCmd(sgio.SG_DXFER_FROM_DEV, buf, cdb...)
}

func (w *Writer) modeSelect(buf []byte, size uint16) error {
	cdb := make([]uint8, 10)
	cdb[0] = SPCCodeModeSelect10
	cdb[1] = 0x10
	cdb[7] = uint8(size << 8)
	cdb[8] = uint8(size)

	return w.sendCmd(sgio.SG_DXFER_TO_DEV, buf, cdb...)
}

func (w *Writer) logSense(page, subpage uint8, buf []byte) error {
	cdb := make([]uint8, 10)
	cdb[0] = SPCCodeLogSense
	cdb[2] = 0x40 | (page & 0x3F) // Current value
	cdb[3] = subpage
	cdb[7] = 0xff
	cdb[8] = 0xff

	resp := make([]byte, 0xffff)
	if err := w.sendCmd(sgio.SG_DXFER_FROM_DEV, resp, cdb...); err != nil {
		return fmt.Errorf("send cmd fail, err= %w", err)
	}

	copy(buf, resp)
	return nil
}

func (w *Writer) formatPartition() error {
	return w.sendCmd(sgio.SG_DXFER_TO_FROM_DEV, nil, SSCCodeFormatMedium, 0, FormatDestPart, 0, 0, 0)
}

func (w *Writer) locate(target *position) error {
	cdb := make([]uint8, 16)
	cdb[0] = SSCCodeLocate16
	if w.current.partition != target.partition {
		cdb[1] = 0x02 // Set Change partition(CP) flag
	}
	cdb[2] = target.partition

	blockNum := target.offset / uint64(w.blockSize)
	binary.BigEndian.PutUint64(cdb[4:], blockNum)
	if err := w.sendCmd(sgio.SG_DXFER_TO_FROM_DEV, nil, SSCCodeFormatMedium, 0, FormatDestPart, 0, 0, 0); err != nil {
		return fmt.Errorf("send locate cmd fail, err= %w", err)
	}

	// left := int(target.offset) % int(w.blockSize)
	return nil
}

func (w *Writer) sendCmd(direction int32, dxfer []byte, cmd ...uint8) error {
	senseBuf := make([]byte, MaxSense)

	ioHdr := &sgio.SgIoHdr{
		InterfaceID:    int32('S'),
		DxferDirection: direction,

		CmdLen: uint8(len(cmd)),
		Cmdp:   &cmd[0],

		MxSbLen: uint8(len(senseBuf)),
		Sbp:     &senseBuf[0],

		// DxferLen: 0,
		// Dxferp: ,

		Timeout: sgio.TIMEOUT_20_SECS,
	}

	if len(dxfer) > 0 {
		ioHdr.DxferLen = uint32(len(dxfer))
		ioHdr.Dxferp = &dxfer[0]
	}

	if err := sgio.SgioSyscall(w.tape, ioHdr); err != nil {
		return err
	}
	if err := sgio.CheckSense(ioHdr, &senseBuf); err != nil {
		return err
	}

	return nil
}
