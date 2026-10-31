package main

// vim: ts=4:sts=4

import (
	"errors"
	"fmt"
	"os"
	"time"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/internal/buildinfo"
)

type OptionsStruct struct {
	Device  string `short:"f" long:"device" value-name:"DEV" description:"Tape device (default: /dev/nst0, or TAPE envvar)"`
	Mock    bool   `long:"mock" description:"Use a mocked tape drive (for tests only)"`
	Dump    string `long:"dump" value-name:"FILE" description:"Dump SCSI raw data to a file"`
	Barcode bool   `long:"barcode" description:"Print only the cartridge memory barcode; fail when it is unreadable"`

	Man bool `hidden:"1" long:"man"`
}

func main() {
	// Reading the binary identity must never initialize or inspect a Tape drive.
	if buildinfo.IsVersion(os.Args[1:]) {
		if err := buildinfo.Write(os.Stdout, "yatm-lto-info"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Parse device options before starting the bounded device-open attempt.
	var err error

	options := &OptionsStruct{}
	parser := flags.NewParser(options, flags.Default)
	_, err = parser.Parse()
	if err != nil {
		os.Exit(1)
	}

	if options.Man {
		parser.WriteManPage(os.Stdout)
		return
	}

	// Barcode output keeps stdout to the value alone; progress goes to stderr.
	progress := os.Stdout
	if options.Barcode {
		progress = os.Stderr
	}

	var drive *TapeDrive

	syncerr := make(chan error)
	go func() {
		if options.Mock {
			drive, err = TapeDriveNewFake()
		} else {
			drive, err = TapeDriveNew(options.Device)
		}
		syncerr <- err
	}()

	openerr := errors.New("timeout")
	started := time.Now()
	waitupto := started.Add(time.Second * 20)
	lastprint := started
waitfor:
	for waitupto.After(time.Now()) && openerr != nil {
		select {
		case openerr = <-syncerr:
			if openerr != nil {
				fmt.Fprintln(progress, "Failed")
				os.Exit(1)
			} else {
				// openerr
				break waitfor
			}
		default:
			if time.Since(lastprint) > time.Second {
				fmt.Fprintf(progress, "Still trying to open the device, aborting in %s...\n", time.Until(waitupto).Round(time.Second))
				lastprint = time.Now()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if openerr != nil {
		fmt.Fprintln(progress, "Timed out opening device, is a tape inserted?")
		os.Exit(1)
	} else {
		fmt.Fprintf(progress, "Device %s opened\n", drive.DeviceName)
	}

	if options.Dump != "" {
		drive.SetDumpFile(options.Dump)
	}

	// An empty value is a readable attribute without an assigned barcode.
	if options.Barcode {
		attr := drive.CmList.Barcode
		if err := drive.GetAttribute(attr); err != nil || !attr.IsValid {
			fmt.Fprintln(os.Stderr, "Barcode is unavailable")
			os.Exit(1)
		}
		fmt.Println(attr.DataStr)
		return
	}

	drive.ScsiInquiry()

	drive.GetStatus()
	drive.GetAttributes()
	fmt.Println("")
	//fmt.Printf("\r                              \r")

	//drive.CmList.Print()
	fmt.Println(drive)
}
