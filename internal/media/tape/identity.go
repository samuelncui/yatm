package tape

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Identity unifies configured st/nst/mode and SG aliases through their passive SCSI sysfs owner.
// Configured retains the exact script-facing name; Key is only an Executor resource identity.
type Identity struct {
	Configured string
	Canonical  string
	Key        string
	Sysfs      string
	SG         string
	Aliases    []string
}

// Resolver reads configuration/sysfs only. It never opens the drive or issues a SCSI command.
type Resolver struct {
	SysfsRoot, DeviceRoot string
	stat                  func(string) (fs.FileInfo, error)
}

var tapeName = regexp.MustCompile(`^n?st([0-9]+)[alm]?$`)
var sgName = regexp.MustCompile(`^sg[0-9]+$`)

func Resolve(path string) (Identity, error) {
	return (Resolver{SysfsRoot: "/sys", DeviceRoot: "/dev"}).Resolve(path)
}

func (r Resolver) Resolve(path string) (Identity, error) {
	// Resolve configured symlinks once without changing the name passed to LTFS scripts.
	identity := Identity{Configured: path}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return identity, fmt.Errorf("resolve Tape device path failed, %w", err)
	}
	stat := r.stat
	if stat == nil {
		stat = os.Stat
	}
	info, err := stat(canonical)
	if err != nil {
		return identity, fmt.Errorf("stat Tape device failed, %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return identity, fmt.Errorf("Tape alias is not a character device: %w", ErrUnsupported)
	}
	identity.Canonical = canonical
	name := filepath.Base(canonical)
	var device string
	if match := tapeName.FindStringSubmatch(name); match != nil {
		device = filepath.Join(r.SysfsRoot, "class", "scsi_tape", "st"+match[1], "device")
	} else if sgName.MatchString(name) {
		device = filepath.Join(r.SysfsRoot, "class", "scsi_generic", name, "device")
	} else {
		return identity, fmt.Errorf("Tape device has no known native alias, path=%q", path)
	}

	// SCSI type and one SG alias establish the same underlying owner without active discovery.
	device, err = filepath.EvalSymlinks(device)
	if err != nil {
		return identity, fmt.Errorf("resolve Tape sysfs identity failed, %w", err)
	}
	kind, err := os.ReadFile(filepath.Join(device, "type"))
	if err != nil {
		return identity, fmt.Errorf("read Tape SCSI type failed, %w", err)
	}
	if strings.TrimSpace(string(kind)) != "1" {
		return identity, fmt.Errorf("SCSI device is not sequential access")
	}
	identity.Sysfs, identity.Key = device, device
	generic, err := os.ReadDir(filepath.Join(device, "scsi_generic"))
	if err != nil {
		return identity, fmt.Errorf("read Tape SG aliases failed, %w", err)
	}
	for _, entry := range generic {
		if !sgName.MatchString(entry.Name()) {
			continue
		}
		if identity.SG != "" {
			return identity, fmt.Errorf("Tape has no unique SG alias")
		}
		identity.SG = filepath.Join(r.DeviceRoot, entry.Name())
		identity.Aliases = append(identity.Aliases, identity.SG)
	}
	if identity.SG == "" {
		return identity, fmt.Errorf("Tape has no SG alias")
	}

	// Include every kernel-reported mode alias for the external holder check owned by the Executor.
	aliases, err := os.ReadDir(filepath.Join(device, "scsi_tape"))
	if err != nil {
		return identity, fmt.Errorf("read Tape native aliases failed, %w", err)
	}
	for _, entry := range aliases {
		if tapeName.MatchString(entry.Name()) {
			identity.Aliases = append(identity.Aliases, filepath.Join(r.DeviceRoot, entry.Name()))
		}
	}
	if len(identity.Aliases) == 1 {
		return identity, fmt.Errorf("Tape native aliases are unknown")
	}
	sort.Strings(identity.Aliases)
	return identity, nil
}
