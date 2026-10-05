//go:build linux

package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// sysfsRoot is the mounted sysfs prefix; tests point it at a fixture tree.
var sysfsRoot = "/sys"

// VolumeSerialNumber reports the block-device serial backing root, or an empty string.
// It reads sysfs only, never mounts or opens the device, and never fails.
func VolumeSerialNumber(root string) string {
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		return ""
	}
	name, err := blockDeviceName(uint64(stat.Dev))
	if err != nil {
		return ""
	}
	return blockSerial(name)
}

// blockDeviceName resolves one filesystem device number to its whole block device name.
func blockDeviceName(device uint64) (string, error) {
	target, err := filepath.EvalSymlinks(filepath.Join(sysfsRoot, "dev", "block",
		fmt.Sprintf("%d:%d", unix.Major(device), unix.Minor(device))))
	if err != nil {
		return "", err
	}
	if filepath.Base(filepath.Dir(target)) == "block" {
		return filepath.Base(target), nil
	}
	// Partitions resolve below their whole disk: .../block/sda/sda1
	return filepath.Base(filepath.Dir(target)), nil
}

// blockSerial reads the kernel-exposed serial, preferring the SCSI serial attribute
// and falling back to the VPD page 0x80 payload that libata publishes for ATA disks.
func blockSerial(name string) string {
	device := filepath.Join(sysfsRoot, "class", "block", name, "device")
	if serial := readTrimmed(filepath.Join(device, "serial")); serial != "" {
		return serial
	}
	return vpdPage80Serial(filepath.Join(device, "vpd_pg80"))
}

func readTrimmed(filename string) string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
}

func vpdPage80Serial(filename string) string {
	data, err := os.ReadFile(filename)
	if err != nil || len(data) <= 4 {
		return ""
	}
	// The payload starts after the two-byte page header and its two-byte length.
	return strings.TrimSpace(strings.TrimRight(string(data[4:]), "\x00"))
}
