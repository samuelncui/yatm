package main

// vim: ts=4:sts=4:

import (
	"fmt"
	"strings"
	"time"
)

const (
	TYPE_BINARY = 0x00
	TYPE_ASCII  = 0x01

	READ_ATT_REPLY_LEN = 512
	WRITE_ATT_CMD_LEN  = 16
)

type CmAttr struct {
	IsValid  bool
	Name     string
	Command  int
	Len      int
	DataType int
	DataInt  uint64
	DataStr  string
	NoTrim   bool
	MockInt  uint64
	MockStr  string
}

type Cm struct {
	PartCapRemain         *CmAttr //
	PartCapMax            *CmAttr //
	TapeAlertFlags        *CmAttr
	LoadCount             *CmAttr //
	MAMSpaceRemaining     *CmAttr
	AssigningOrganization *CmAttr //
	FormattedDensityCode  *CmAttr //
	InitializationCount   *CmAttr //err
	Identifier            *CmAttr //err
	VolumeChangeReference *CmAttr //err

	DeviceAtLoadN0             *CmAttr //
	DeviceAtLoadN1             *CmAttr //
	DeviceAtLoadN2             *CmAttr //
	DeviceAtLoadN3             *CmAttr //
	TotalWritten               *CmAttr //
	TotalRead                  *CmAttr //
	TotalWrittenSession        *CmAttr //
	TotalReadSession           *CmAttr //
	LogicalPosFirstEncrypted   *CmAttr //err
	LogicalPosFirstUnencrypted *CmAttr //err

	UsageHistory     *CmAttr
	PartUsageHistory *CmAttr

	Manufacturer             *CmAttr //
	SerialNo                 *CmAttr //
	Length                   *CmAttr //
	Width                    *CmAttr //
	AssigningOrg             *CmAttr //
	MediumDensity            *CmAttr //
	ManufactureDate          *CmAttr //
	MAMCapacity              *CmAttr //
	Type                     *CmAttr //
	TypeInformation          *CmAttr //
	UserText                 *CmAttr
	DateTimeLastWritten      *CmAttr //err
	TextLocalizationId       *CmAttr //err
	Barcode                  *CmAttr //err
	OwningHostTextualName    *CmAttr //err
	MediaPool                *CmAttr //err
	ApplicationFormatVersion *CmAttr //err
	MediumGloballyUniqId     *CmAttr //err
	MediaPoolGloballyUniqId  *CmAttr //err
}

type SpecsType struct {
	IsValid         bool
	NativeCap       int
	CompressedCap   int
	NativeSpeed     int
	CompressedSpeed int
	FullTapeMinutes int
	CompressFactor  string
	CanWORM         bool
	CanEncrypt      bool
	PartitionNumber int
	BandsPerTape    int
	WrapsPerBand    int
	TracksPerWrap   int
}

func min2human(min int) string {
	if min < 60 {
		return fmt.Sprintf("%d min", min)
	}
	return fmt.Sprintf("%dh%d", min/60, min-60*(min/60))
}

// https://github.com/hreinecke/sg3_utils/issues/18
func cmDensityFriendly(d int) (string, SpecsType) {
	friendlyName := "Unknown"
	var specs SpecsType
	switch d {
	case 0x40:
		friendlyName = "LTO-1"
		specs = SpecsType{true, 100, 200, 20, 40, 60 + 23, "2:1", false, false, 1, 4, 12, 8}
	case 0x42:
		friendlyName = "LTO-2"
		specs = SpecsType{true, 200, 400, 40, 80, 60 + 23, "2:1", false, false, 1, 4, 16, 8}
	case 0x44:
		friendlyName = "LTO-3"
		specs = SpecsType{true, 400, 800, 80, 160, 60 + 23, "2:1", true, false, 1, 4, 11, 16}
	case 0x46:
		friendlyName = "LTO-4"
		specs = SpecsType{true, 800, 1600, 120, 240, 60 + 51, "2:1", true, true, 1, 4, 14, 16}
	case 0x58:
		friendlyName = "LTO-5"
		specs = SpecsType{true, 1500, 3000, 140, 280, 60*3 + 10, "2:1", true, true, 2, 4, 20, 16}
	case 0x5A:
		friendlyName = "LTO-6"
		specs = SpecsType{true, 2500, 6250, 160, 400, 60*4 + 20, "2.5:1", true, true, 4, 4, 34, 16}
	case 0x5C:
		friendlyName = "LTO-7"
		specs = SpecsType{true, 6000, 15000, 300, 750, 60*5 + 33, "2.5:1", true, true, 4, 4, 28, 32}
	case 0x5D:
		friendlyName = "LTO-M8"
		specs = SpecsType{true, 9000, 22500, 300, 750, 60*8 + 20, "2.5:1", false, true, 4, 4, 42, 32}
	case 0x5E:
		friendlyName = "LTO-8"
		specs = SpecsType{true, 12000, 30000, 360, 900, 60*9 + 16, "2.5:1", true, true, 4, 4, 52, 32}
	case 0x60: /* guessed, to check FIXME */
		friendlyName = "LTO-9"
		specs = SpecsType{true, 18000, 45000, 400, 1000, 60*12 + 30, "2.5:1", true, true, 4, 0, 0, 32} /* FIXME */
	}
	return friendlyName, specs
}

func (cm *Cm) String() string {
	s := "Medium information:\n"

	if cm.Type.IsValid {
		friendlyName := "Unknown"
		switch cm.Type.DataInt {
		case 0x00:
			friendlyName = "Data cartridge"
		case 0x01:
			friendlyName = "Cleaning cartridge"
			if cm.TypeInformation.IsValid {
				friendlyName = fmt.Sprintf("%s (%d cycles max)", friendlyName, cm.TypeInformation.DataInt)
			}
		case 0x80:
			friendlyName = "WORM (Write-once) cartridge"
		}
		s += fmt.Sprintf("  Cartridge Type: 0x%02x - %s\n", cm.Type.DataInt, friendlyName)
	}

	var specs SpecsType
	specs.IsValid = false
	if cm.MediumDensity.IsValid {
		var s1 string
		s1, specs = cmDensityFriendly(int(cm.MediumDensity.DataInt))
		s += fmt.Sprintf("  Medium format : 0x%02x - %s\n", cm.MediumDensity.DataInt, s1)
		s2, _ := cmDensityFriendly(int(cm.FormattedDensityCode.DataInt))
		s += fmt.Sprintf("  Formatted as  : 0x%02x - %s\n", cm.FormattedDensityCode.DataInt, s2)
	}
	if cm.Barcode.IsValid {
		s += fmt.Sprintf("  Barcode       : %s\n", cm.Barcode.DataStr)
	}
	if cm.AssigningOrg.IsValid {
		s += fmt.Sprintf("  Assign. Org.  : %s\n", cm.AssigningOrg.DataStr)
	}
	if cm.Manufacturer.IsValid {
		s += fmt.Sprintf("  Manufacturer  : %s\n", cm.Manufacturer.DataStr)
	}
	if cm.SerialNo.IsValid {
		s += fmt.Sprintf("  Serial No     : %s\n", cm.SerialNo.DataStr)
	}
	if cm.ManufactureDate.IsValid {
		if len(cm.ManufactureDate.DataStr) == 8 {
			// YYYYMMDD
			if d, err := time.Parse("20060102", cm.ManufactureDate.DataStr); err == nil {
				years := time.Since(d).Hours() / 24.0 / 365.0
				s += fmt.Sprintf("  Manuf. Date   : %s-%s-%s (roughly %.1f years ago)\n", cm.ManufactureDate.DataStr[0:4], cm.ManufactureDate.DataStr[4:6], cm.ManufactureDate.DataStr[6:8], years)
			} else {
				s += fmt.Sprintf("  Manuf. Date   : %s-%s-%s\n", cm.ManufactureDate.DataStr[0:4], cm.ManufactureDate.DataStr[4:6], cm.ManufactureDate.DataStr[6:8])
			}
		} else {
			s += fmt.Sprintf("  Manuf. Date   : %s\n", cm.ManufactureDate.DataStr)
		}
	}
	if cm.Length.IsValid {
		s += fmt.Sprintf("  Tape length   : %d meters\n", cm.Length.DataInt)
	}
	if cm.Width.IsValid {
		s += fmt.Sprintf("  Tape width    : %.1f mm\n", float32(cm.Width.DataInt)/10)
	}
	if cm.MAMCapacity.IsValid {
		if cm.MAMSpaceRemaining.IsValid {
			s += fmt.Sprintf("  MAM Capacity  : %d bytes (%d bytes remaining)\n", cm.MAMCapacity.DataInt, cm.MAMSpaceRemaining.DataInt)
		} else {
			s += fmt.Sprintf("  MAM Capacity  : %d bytes\n", cm.MAMCapacity.DataInt)
		}
	}

	if specs.IsValid {
		s += fmt.Sprintf("Format specs:\n")
		s += fmt.Sprintf("   Capacity  : %5d GB native   - %5d GB compressed with a %s ratio\n", specs.NativeCap, specs.CompressedCap, specs.CompressFactor)
		s += fmt.Sprintf("   R/W Speed : %5d MB/s native - %5d MB/s compressed\n", specs.NativeSpeed, specs.CompressedSpeed)
		s += fmt.Sprintf("   Partitions: %5d max partitions supported\n", specs.PartitionNumber)
		s += fmt.Sprintf("   Phy. specs: %d bands/tape, %d wraps/band, %d tracks/wrap, %d total tracks\n", specs.BandsPerTape, specs.WrapsPerBand, specs.TracksPerWrap, specs.BandsPerTape*specs.WrapsPerBand*specs.TracksPerWrap)
		s += fmt.Sprintf("   Duration  : %s to fill tape with %d end-to-end passes (%.0f seconds/pass)\n", min2human(specs.FullTapeMinutes), specs.BandsPerTape*specs.WrapsPerBand, float64(specs.FullTapeMinutes)*60.0/float64(specs.BandsPerTape*specs.WrapsPerBand))
	}

	s += fmt.Sprintf("Usage information:\n")
	if cm.PartCapRemain.IsValid && cm.PartCapMax.IsValid {
		r := cm.PartCapRemain.DataInt
		m := cm.PartCapMax.DataInt
		if m > 0 {
			s += fmt.Sprintf("  Partition space free  : %d%% (%d/%d MiB, %d/%d GiB, %.2f/%.2f TiB)\n", 100*r/m, r, m, r/1024, m/1024, float32(r)/1024/1024, float32(m)/1024/1024)
		} else {
			s += fmt.Sprintf("  Partition space free  :  ?%% (%d/%d MiB, %d/%d GiB, %.2f/%.2f TiB)\n", r, m, r/1024, m/1024, float32(r)/1024/1024, float32(m)/1024/1024)
		}
	}
	if cm.LoadCount.IsValid {
		s += fmt.Sprintf("  Cartridge load count  : %d\n", cm.LoadCount.DataInt)
	}
	if cm.TotalWritten.IsValid && cm.TotalRead.IsValid {
		s += fmt.Sprintf("  Data written - alltime: %12d MiB (%9.2f GiB, %6.2f TiB", cm.TotalWritten.DataInt, float64(cm.TotalWritten.DataInt)/1024, float64(cm.TotalWritten.DataInt)/1024/1024)
		if cm.PartCapMax.IsValid {
			s += fmt.Sprintf(", %.2f FVE", float64(cm.TotalWritten.DataInt)/float64(cm.PartCapMax.DataInt))
		}
		s += fmt.Sprintf(")\n")

		s += fmt.Sprintf("  Data read    - alltime: %12d MiB (%9.2f GiB, %6.2f TiB", cm.TotalRead.DataInt, float64(cm.TotalRead.DataInt)/1024, float64(cm.TotalRead.DataInt)/1024/1024)
		if cm.PartCapMax.IsValid {
			s += fmt.Sprintf(", %.2f FVE", float64(cm.TotalRead.DataInt)/float64(cm.PartCapMax.DataInt))
		}
		s += fmt.Sprintf(")\n")
	}
	if cm.TotalWrittenSession.IsValid && cm.TotalReadSession.IsValid {
		s += fmt.Sprintf("  Data written - session: %12d MiB (%9.2f GiB, %6.2f TiB", cm.TotalWrittenSession.DataInt, float64(cm.TotalWrittenSession.DataInt)/1024, float64(cm.TotalWrittenSession.DataInt)/1024/1024)
		if cm.PartCapMax.IsValid {
			s += fmt.Sprintf(", %.2f FVE", float64(cm.TotalWrittenSession.DataInt)/float64(cm.PartCapMax.DataInt))
		}
		s += fmt.Sprintf(")\n")

		s += fmt.Sprintf("  Data read    - session: %12d MiB (%9.2f GiB, %6.2f TiB", cm.TotalReadSession.DataInt, float64(cm.TotalReadSession.DataInt)/1024, float64(cm.TotalReadSession.DataInt)/1024/1024)
		if cm.PartCapMax.IsValid {
			s += fmt.Sprintf(", %.2f FVE", float64(cm.TotalReadSession.DataInt)/float64(cm.PartCapMax.DataInt))
		}
		s += fmt.Sprintf(")\n")
	}

	s += fmt.Sprintf("Previous sessions:\n")
	for i, load := range []*CmAttr{cm.DeviceAtLoadN0, cm.DeviceAtLoadN1, cm.DeviceAtLoadN2, cm.DeviceAtLoadN3} {
		if load.IsValid {
			var devname, serial string
			if len(load.DataStr) > 8 {
				devname = strings.Trim(load.DataStr[:8], " \u0000")
				serial = strings.Trim(load.DataStr[8:], " \u0000")
			} else {
				devname = strings.Trim(load.DataStr, " \u0000")
			}
			if serial != "" {
				s += fmt.Sprintf("  Session N-%d: Used in a device of vendor %s (serial %s)\n", i, devname, serial)
			} else {
				s += fmt.Sprintf("  Session N-%d: Used in a device of vendor %s\n", i, devname)
			}
		}
	}

	//s += fmt.Sprintf("Medium Usage History:\n")

	return s
}

func CmAttrNew(name string, command int, length int, datatype int, mock interface{}) *CmAttr {
	cmAttr := &CmAttr{
		Name:     name,
		Command:  command,
		Len:      length,
		DataType: datatype,
	}

	switch mock.(type) {
	case string:
		cmAttr.MockStr = mock.(string)
	default:
		cmAttr.MockInt = uint64(mock.(int))
	}

	return cmAttr
}

func CmNew() *Cm {
	return &Cm{
		PartCapRemain: CmAttrNew(
			"Remaining capacity in partition (MiB)",
			0x0000, 8, TYPE_BINARY, 198423,
		),
		PartCapMax: CmAttrNew(
			"Maximum capacity in partition (MiB)",
			0x0001, 8, TYPE_BINARY, 200448,
		),
		TapeAlertFlags: CmAttrNew(
			"Tape alert flags",
			0x0002, 8, TYPE_BINARY, 0,
		),
		LoadCount: CmAttrNew(
			"Load count",
			0x0003, 8, TYPE_BINARY, 42,
		),
		MAMSpaceRemaining: CmAttrNew(
			"MAM space remaining (bytes)",
			0x0004, 8, TYPE_BINARY, 850,
		),
		AssigningOrganization: CmAttrNew(
			"Assigning organization",
			0x0005, 8, TYPE_ASCII, "LTO-FAKE",
		),
		FormattedDensityCode: CmAttrNew(
			"Formatted density code",
			0x0006, 1, TYPE_BINARY, 66,
		),
		InitializationCount: CmAttrNew(
			"Initialization count",
			0x0007, 2, TYPE_BINARY, "err",
		),
		Identifier: CmAttrNew(
			"Identifier (deprecated)",
			0x0008, 32, TYPE_ASCII, "err",
		),
		VolumeChangeReference: CmAttrNew(
			"Volume change reference",
			0x0009, 4, TYPE_BINARY, "err",
		),
		DeviceAtLoadN0: CmAttrNew(
			"Device Vendor/Serial at current load",
			0x020A, 40, TYPE_ASCII, "FAKEVENDMODEL012345678901234567890123456",
		),
		DeviceAtLoadN1: CmAttrNew(
			"Device Vendor/Serial at load N-1",
			0x020B, 40, TYPE_ASCII, "FAKEVEND   MODEL12345",
		),
		DeviceAtLoadN2: CmAttrNew(
			"Device Vendor/Serial at load N-2",
			0x020C, 40, TYPE_ASCII, "ACMEINC \u0000\u0000\u0000\u0000\u0000\u0000\u0000\u0000\u0000\u0000",
		),
		DeviceAtLoadN3: CmAttrNew(
			"Device Vendor/Serial at load N-3",
			0x020D, 40, TYPE_ASCII, "FAKEVEND   MODEL34567",
		),

		TotalWritten: CmAttrNew(
			"Total MiB written",
			0x0220, 8, TYPE_BINARY, 17476,
		),
		TotalRead: CmAttrNew(
			"Total MiB read",
			0x0221, 8, TYPE_BINARY, 15827,
		),
		TotalWrittenSession: CmAttrNew(
			"Total MiB written in current load",
			0x0222, 8, TYPE_BINARY, 0,
		),
		TotalReadSession: CmAttrNew(
			"Total MiB Read in current load",
			0x0223, 8, TYPE_BINARY, 139,
		),
		LogicalPosFirstEncrypted: CmAttrNew(
			"Logical pos. of 1st encrypted block",
			0x0224, 8, TYPE_BINARY, "err",
		),
		LogicalPosFirstUnencrypted: CmAttrNew(
			"Logical pos. of 1st unencrypted block after 1st encrypted block",
			0x0225, 8, TYPE_BINARY, "err",
		),

		UsageHistory: CmAttrNew(
			"Medium Usage History",
			0x0340, 90, TYPE_BINARY, "err",
		),
		PartUsageHistory: CmAttrNew(
			"Partition Usage History",
			0x0341, 90, TYPE_BINARY, "err",
		),

		Manufacturer: CmAttrNew(
			"Manufacturer",
			0x0400, 8, TYPE_ASCII, "FAKMANUF",
		),
		SerialNo: CmAttrNew(
			"Serial No",
			0x0401, 32, TYPE_ASCII, "123456789",
		),
		Length: CmAttrNew(
			"Tape length",
			0x0402, 4, TYPE_BINARY, 999,
		),
		Width: CmAttrNew(
			"Tape width",
			0x0403, 4, TYPE_BINARY, 111,
		),
		AssigningOrg: CmAttrNew(
			"Assigning Organization",
			0x0404, 8, TYPE_ASCII, "LTO-FAKE",
		),
		MediumDensity: CmAttrNew(
			"Medium density code",
			0x0405, 1, TYPE_BINARY, 0x42,
		),
		ManufactureDate: CmAttrNew(
			"Manufacture Date",
			0x0406, 8, TYPE_ASCII, "20191231",
		),
		MAMCapacity: CmAttrNew(
			"MAM Capacity",
			0x0407, 8, TYPE_BINARY, 4096,
		),
		Type: CmAttrNew(
			"Type",
			0x0408, 1, TYPE_BINARY, 1,
		),
		TypeInformation: CmAttrNew(
			"Type Information",
			0x0409, 2, TYPE_BINARY, 50,
		),
		/*
			CmAttr{
				Name:     "Application Vendor",
				Command:  0x0800,
				Len:      8,
				DataType: TYPE_ASCII,
			},
			CmAttr{
				Name:     "Application Name",
				Command:  0x0801,
				Len:      32,
				DataType: TYPE_ASCII,
			},
			CmAttr{
				Name:     "Application Version",
				Command:  0x0802,
				Len:      8,
				DataType: TYPE_ASCII,
			},
		*/
		UserText: CmAttrNew(
			"User Medium Text Label",
			0x0803, 160, TYPE_ASCII, "User Label",
			//NoTrim:   tr)e,
		),

		DateTimeLastWritten: CmAttrNew(
			"Date and Time Last Written",
			0x0804, 12, TYPE_ASCII, "err",
		),
		TextLocalizationId: CmAttrNew(
			"Text Localization Identifier",
			0x0805, 1, TYPE_BINARY, "err",
		),
		Barcode: CmAttrNew(
			"Barcode",
			0x0806, 12, TYPE_ASCII, "err",
		),
		OwningHostTextualName: CmAttrNew(
			"Owning Host Textual Name",
			0x0807, 80, TYPE_ASCII, "err",
		),
		MediaPool: CmAttrNew(
			"Media Pool",
			0x0808, 160, TYPE_ASCII, "err",
		),
		ApplicationFormatVersion: CmAttrNew(
			"Application Format Version",
			0x080B, 16, TYPE_ASCII, "err",
		),
		MediumGloballyUniqId: CmAttrNew(
			"Medium Globally Unique Identifier",
			0x0820, 36, TYPE_ASCII, "err",
		),
		MediaPoolGloballyUniqId: CmAttrNew(
			"Media Pool Globally Unique Identifier",
			0x0821, 36, TYPE_ASCII, "err",
		),
	}
}
