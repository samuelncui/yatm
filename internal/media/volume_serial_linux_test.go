//go:build linux

package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestBlockDeviceNameResolvesWholeDisksAndPartitions(t *testing.T) {
	// Mirror the sysfs layout for one whole disk and one partition of it.
	root := t.TempDir()
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
	device := filepath.Join(root, "devices", "pci", "block", "sda")
	require.NoError(t, os.MkdirAll(filepath.Join(device, "sda1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dev", "block"), 0o755))
	require.NoError(t, os.Symlink(device, filepath.Join(root, "dev", "block", "8:0")))
	require.NoError(t, os.Symlink(filepath.Join(device, "sda1"), filepath.Join(root, "dev", "block", "8:1")))

	name, err := blockDeviceName(unix.Mkdev(8, 0))
	require.NoError(t, err)
	require.Equal(t, "sda", name)
	name, err = blockDeviceName(unix.Mkdev(8, 1))
	require.NoError(t, err)
	require.Equal(t, "sda", name)

	// An absent device mapping is unknown, never a fabricated name.
	_, err = blockDeviceName(unix.Mkdev(9, 9))
	require.Error(t, err)
}

func TestBlockSerialPrefersDeviceAttributeAndReadsVPD(t *testing.T) {
	root := t.TempDir()
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
	device := filepath.Join(root, "class", "block", "sda", "device")
	require.NoError(t, os.MkdirAll(device, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(device, "serial"), []byte("SER-123\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(device, "vpd_pg80"), append([]byte{0x00, 0x80, 0x00, 0x08}, []byte("VPD-456\x00")...), 0o644))
	require.Equal(t, "SER-123", blockSerial("sda"))

	// ATA disks without the attribute still expose the same serial through VPD page 0x80.
	require.NoError(t, os.Remove(filepath.Join(device, "serial")))
	require.Equal(t, "VPD-456", blockSerial("sda"))

	// Unknown devices and truncated pages are simply unsigned.
	require.NoError(t, os.WriteFile(filepath.Join(device, "vpd_pg80"), []byte{0x00, 0x80}, 0o644))
	require.Empty(t, blockSerial("sda"))
	require.Empty(t, blockSerial("sdz"))
}
