package main

// vim: ts=4:sts=4:

import (
	"bytes"
	"io"
	//"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/HewlettPackard/structex"
	"github.com/benmcclelland/mtio"
	"github.com/benmcclelland/sgio"
	"github.com/modern-go/reflect2"
)

type InquiryInfoType struct {
	Vendor   string
	Model    string
	Firmware string
}

type TapeDrive struct {
	DeviceName  string
	Dev         *os.File
	CmList      *Cm
	InquiryInfo InquiryInfoType
	dumpFd      *os.File
}

func TapeDriveNewFake(progress io.Writer) (*TapeDrive, error) {
	return TapeDriveNew("FAKE", progress)
}

func (drive TapeDrive) IsFake() bool {
	return drive.DeviceName == "FAKE"
}

// copy of sg.OpenScsiDevice() but with RDONLY instead of O_RDWR
func OpenScsiDeviceRO(fname string) (*os.File, error) {
	f, err := os.OpenFile(fname, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	var version uint32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(f.Fd()),
		uintptr(sgio.SG_GET_VERSION_NUM),
		uintptr(unsafe.Pointer(&version)),
	)
	if errno != 0 {
		return nil, fmt.Errorf("failed to get version info from sg device (errno=%d)", errno)
	}
	if version < 30000 {
		return nil, fmt.Errorf("device does not appear to be an sg device")
	}
	return f, nil
}

// TapeDriveNew reports opening progress to progress, so --barcode can keep stdout to the value alone.
func TapeDriveNew(devicename string, progress io.Writer) (*TapeDrive, error) {
	if devicename == "" {
		if os.Getenv("TAPE") != "" {
			devicename = os.Getenv("TAPE")
		} else {
			devicename = "/dev/nst0"
		}
	}

	drive := &TapeDrive{DeviceName: devicename, CmList: CmNew()}
	if drive.IsFake() {
		fmt.Fprintln(progress, "Will use a fake tape drive")
		return drive, nil
	}

	fmt.Fprintf(progress, "Opening device %s\n", devicename)
	dev, err := OpenScsiDeviceRO(devicename)
	if err != nil {
		fmt.Fprintln(progress, "Failed to open:", err)
		return nil, err
	}

	fmt.Fprintln(progress, "Checking whether device is ready")
	err = sgio.TestUnitReady(dev)
	if err != nil {
		fmt.Fprintln(progress, "Unit is not ready:", err)
		return nil, err
	}

	fmt.Fprintln(progress, "Unit is ready")
	drive.Dev = dev
	return drive, nil
}

func (drive *TapeDrive) GetStatus() error {
	// http://manpages.ubuntu.com/manpages/focal/man4/st.4.html
	mtget, _ := mtio.GetStatus(drive.Dev)
	fmt.Println(mtget)
	//blocksz := uint32(mtget.DsReg) & 0x00FFFFFF
	//density := (uint32(mtget.DsReg) & 0xFF000000) >> 24
	//fmt.Printf("blocksz=%d density=%x\n", blocksz, density)
	//fmt.Println(mtio.GetPos(drive.Dev))
	return nil
}

func (drive *TapeDrive) SetDumpFile(file string) error {
	if drive.dumpFd != nil {
		drive.dumpFd.Close()
	}
	fo, err := os.Create(file)
	if err != nil {
		panic(err)
	}
	drive.dumpFd = fo
	return nil
}

func (drive *TapeDrive) GetAttributes() error {
	typ := reflect2.TypeOfPtr(drive.CmList).Elem().(reflect2.StructType)
	for i := 0; i < typ.NumField(); i++ {
		attrPtr := typ.Field(i).Get(drive.CmList).(**CmAttr)
		attr := *attrPtr
		drive.GetAttribute(attr)
	}
	return nil
}

func (drive *TapeDrive) GetAttribute(attr *CmAttr) error {
	if drive == nil {
		return errors.New("drive is nil")
	}

	senseBuf := make([]byte, sgio.SENSE_BUF_LEN)
	replyBuf := make([]byte, READ_ATT_REPLY_LEN)

	/* READ ATTRIBUTE (8Ch)
		bits: 7 | 6 | 5 | 4 | 3 | 2 | 1 | 0
	   byte0: --- OPERATION CODE (8Ch) ----
	   byte1: reserved  | SERVICE ACTION
	   byte2: obsolete
	   byte3: obsolete
	   byte4: obsolete
	   byte5: LOGICAL VOLUME NUMBER
	   byte6: reserved
	   byte7: PARTITION NUMBER
	   byte8: (MSB) <-- FIRST ATTRIBUTE
	   byte9:     IDENTIFIER      --> (LSB)
	  byte10: (MSB) <-- ALLOCATION
	  byte11:
	  byte12:
	  byte13:     LENGTH          --> (LSB)
	  byte14: reserved                | CACHE
	  byte15: CONTROL BYTE (00h)
	*/
	inqCmdBlk := []uint8{0x8C, 0, 0, 0, 0, 0, 0, 0, 0x04, 0x00, 0, 0, 159, 0, 0, 0}
	inqCmdBlk[8] = uint8(0xff & (attr.Command >> 8))
	inqCmdBlk[9] = uint8(0xff & attr.Command)
	inqCmdBlk[12] = uint8(0xff & attr.Len)

	ioHdr := &sgio.SgIoHdr{
		InterfaceID:    int32('S'),
		CmdLen:         uint8(len(inqCmdBlk)),
		MxSbLen:        sgio.SENSE_BUF_LEN,
		DxferDirection: sgio.SG_DXFER_FROM_DEV,
		DxferLen:       READ_ATT_REPLY_LEN,
		Dxferp:         &replyBuf[0],
		Cmdp:           &inqCmdBlk[0],
		Sbp:            &senseBuf[0],
		Timeout:        sgio.TIMEOUT_20_SECS,
	}

	if drive.IsFake() {
		if attr.MockStr == "err" {
			attr.IsValid = false
			return errors.New("mocked error")
		}

		if attr.DataType == TYPE_BINARY {
			attr.DataInt = attr.MockInt
			attr.IsValid = true
			return nil
		}

		if attr.DataType == TYPE_ASCII {
			attr.DataStr = attr.MockStr
			attr.IsValid = true
			return nil
		}

		return errors.New("Invalid type")
	}

	attr.IsValid = false

	err := sgio.SgioSyscall(drive.Dev, ioHdr)
	if drive.dumpFd != nil {
		senserr := sgio.CheckSense(ioHdr, &senseBuf)
		senstr := "<nil>"
		if senserr != nil {
			senstr = strings.Replace(senserr.Error(), "\n", " ", -1)
		}
		drive.dumpFd.Write([]byte(fmt.Sprintf("GetAttribute[%s]:\nsyscallerr: %v\nsenserr: %v\ncommand: 0x%04x\ninqCmdBlk: %v\nsenseBuf: %v\nreplyBuf: %v\n\n", attr.Name, err, senstr, attr.Command, inqCmdBlk, senseBuf, replyBuf)))
	}
	if err != nil {
		return err
	}

	err = sgio.CheckSense(ioHdr, &senseBuf)
	if err != nil {
		return err
	}

	if attr.DataType == TYPE_BINARY {
		attr.DataInt = 0
		for i := 0; i < attr.Len; i++ {
			attr.DataInt *= 256
			attr.DataInt += uint64(replyBuf[9+i])
		}
		attr.IsValid = true
		return nil
	}

	if attr.DataType == TYPE_ASCII {
		attr.DataStr = string(replyBuf[9:(9 + attr.Len)])
		if !attr.NoTrim {
			attr.DataStr = strings.TrimRight(attr.DataStr, " ")
		}
		attr.IsValid = true
		return nil
	}

	return errors.New("Invalid type")
}

type SCSI_Inquiry_Cmd struct {
	OpCode           uint8
	EVPD             uint8 `bitfield:"1"`
	reserved0        uint8 `bitfield:"4,reserved"`
	obsolete0        uint8 `bitfield:"3,reserved"`
	PageCode         uint8
	AllocationLength uint16
	ControlByte      uint8
}

type SCSI_Drive_Serial_Numbers_Return struct {
	PeripheralDeviceType uint8 `bitfield:"5"` // Byte 0
	PeripheralQualifier  uint8 `bitfield:"3"`
	PageCode             uint8 // Byte 1
	reserved0            uint8 `bitfield:"8,reserved"` // Byte 2
	PageLength           uint8
	ManufSN              [12]byte
	ReportedSN           [12]byte
}

type SCSI_Inquiry_Return struct {
	PeripheralDeviceType uint8 `bitfield:"5"` // Byte 0
	PeripheralQualifier  uint8 `bitfield:"3"`
	Reserved0            uint8
	Version              uint8
	ReponseDataFormat    uint8 `bitfield:"4"`
	HiSup                uint8 `bitfield:"1"`
	NACA                 uint8 `bitfield:"1"`
	Obsolete0            uint8 `bitfield:"1"`
	Obsolete1            uint8 `bitfield:"1"`
	AdditionalLen        uint8
	Protect              uint8 `bitfield:"1"`
	Reserved1            uint8 `bitfield:"2"`
	ThreePC              uint8 `bitfield:"1"`
	TPGS                 uint8 `bitfield:"2"`
	ACC                  uint8 `bitfield:"1"`
	SCCS                 uint8 `bitfield:"1"`
	Osef0                uint8
	Osef1                uint8
	VendorID             [8]byte
	ProductID            [16]byte
	ProductRevision      [4]byte // YMDV(F63D), Y=15 M=6 D=3 V=D
	Reserved2            uint8
	Obsolete2            uint8
	MaxSpeed             uint8 `bitfield:"4"`
	ProtocolID           uint8 `bitfield:"4"`
	FIPS                 uint8 `bitfield:"2"`
	Reserved3            uint8 `bitfield:"5"`
	Restricted           uint8 `bitfield:"1"`
	Reserved4            uint8
	OEMSpecific          uint8
	OEMSpecificSubfield  uint8
	Reserved5            uint8
	Reserved6            uint32
	PartNumber           [8]byte
	Reserved7            uint8
	Reserved8            uint8
	Truc1                uint16
	Truc2                uint16
	Truc3                uint16
	Truc4                uint16
	Truc5                uint16
	Truc6                uint16
}

func (drive *TapeDrive) ScsiInquiry() error {
	senseBuf := make([]byte, sgio.SENSE_BUF_LEN)
	replyBuf := make([]byte, 0xFF)

	inqCmdBlk := []uint8{0x12, 0, 0, 0, 0xFF, 0}

	ioHdr := &sgio.SgIoHdr{
		InterfaceID:    int32('S'),
		CmdLen:         uint8(len(inqCmdBlk)),
		MxSbLen:        sgio.SENSE_BUF_LEN,
		DxferDirection: sgio.SG_DXFER_FROM_DEV,
		DxferLen:       0xFF,
		Dxferp:         &replyBuf[0],
		Cmdp:           &inqCmdBlk[0],
		Sbp:            &senseBuf[0],
		Timeout:        sgio.TIMEOUT_20_SECS,
	}

	if !drive.IsFake() {
		err := sgio.SgioSyscall(drive.Dev, ioHdr)
		if drive.dumpFd != nil {
			senserr := sgio.CheckSense(ioHdr, &senseBuf)
			senstr := "<nil>"
			if senserr != nil {
				senstr = strings.Replace(senserr.Error(), "\n", " ", -1)
			}
			drive.dumpFd.Write([]byte(fmt.Sprintf("ScsiInquiry:\nsyscallerr: %v\nsenserr: %v\ninqCmdBlk: %v\nsenseBuf: %v\nreplyBuf: %v\n\n", err, senstr, inqCmdBlk, senseBuf, replyBuf)))
		}
		if err != nil {
			return err
		}

		err = sgio.CheckSense(ioHdr, &senseBuf)
		if err != nil {
			return err
		}
	} else {
		replyBuf = []byte{1, 128, 3, 2, 91, 0, 1, 48, 72, 80, 32, 32, 32, 32, 32, 32, 85, 108, 116, 114, 105, 117, 109, 32, 50, 45, 83, 67, 83, 73, 32, 32, 70, 54, 51, 68, 0, 0, 0, 0, 0, 12, 0, 36, 68, 82, 45, 49, 48, 0, 0, 0, 0, 0, 0, 0, 12, 0, 0, 84, 11, 28, 2, 119, 2, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	}

	//fmt.Println(replyBuf)

	var parsed = new(SCSI_Inquiry_Return)
	if err := structex.Decode(bytes.NewReader(replyBuf), parsed); err != nil {
		fmt.Println("structex failed:", err)
	}
	drive.InquiryInfo.Vendor = strings.Trim(string(parsed.VendorID[:]), " \u0000")
	drive.InquiryInfo.Model = strings.Trim(string(parsed.ProductID[:]), " \u0000")
	drive.InquiryInfo.Firmware = strings.Trim(string(parsed.ProductRevision[:]), " \u0000")
	//fmt.Printf("MaxSpeed=%d ProtoID=%d OEMSpec=%d OEMSpecSub=%d PartNu=<%s>\n", parsed.MaxSpeed, parsed.ProtocolID, parsed.OEMSpecific, parsed.OEMSpecificSubfield, parsed.PartNumber)
	//fmt.Printf("Truc1=%04x Truc2=%04x Truc3=%04x Truc4=%04x Truc5=%04x Truc6=%04x\n", parsed.Truc1, parsed.Truc2, parsed.Truc3, parsed.Truc4, parsed.Truc5, parsed.Truc6)

	return nil
}

func (drive *TapeDrive) String() string {
	s := fmt.Sprintf("Drive information:\n")
	s += fmt.Sprintf("   Vendor  : %s\n", drive.InquiryInfo.Vendor)
	s += fmt.Sprintf("   Model   : %s\n", drive.InquiryInfo.Model)
	s += fmt.Sprintf("   Firmware: %s\n", drive.InquiryInfo.Firmware)
	s += drive.CmList.String()
	return s
}
