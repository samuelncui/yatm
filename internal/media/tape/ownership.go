package tape

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
)

var (
	ErrBusy             = errors.New("Tape device has an external holder")
	ErrOwnershipUnknown = errors.New("Tape access ownership is unknown")
	ErrMissingTool      = errors.New("Tape ownership tool is missing")
)

// CheckOwnership observes known mounts and every resolved alias under the caller's lease.
// Missing visibility never becomes permission to use raw access or a restriction on native LTFS.
func CheckOwnership(ctx context.Context, identity Identity) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if runtime.GOOS != "linux" {
		return ErrUnsupported
	}
	if identity.Key == "" || len(identity.Aliases) < 2 {
		return ErrOwnershipUnknown
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.Join(ErrOwnershipUnknown, err)
	}
	if mountedAlias(string(mounts), identity.Aliases) {
		return ErrBusy
	}
	if err := checkVisibility(ctx, "/proc", string(mounts)); err != nil {
		return err
	}

	// fuser alone can silently omit another user's unreadable file descriptor table.
	return checkHolders(ctx, identity.Aliases, func(ctx context.Context, args []string) ([]byte, []byte, error) {
		command := exec.CommandContext(ctx, "fuser", args...)
		var stdout, stderr strings.Builder
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return []byte(stdout.String()), []byte(stderr.String()), err
	})
}

func checkVisibility(ctx context.Context, root, mounts string) error {
	// Hidden process directories cannot establish absence even when visible fd tables are readable.
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[4] != root {
			continue
		}
		for _, field := range fields[5:] {
			for _, option := range strings.Split(field, ",") {
				if strings.HasPrefix(option, "hidepid=") && option != "hidepid=0" && option != "hidepid=off" {
					return ErrOwnershipUnknown
				}
			}
		}
	}

	// Observe fd-table access in the existing same-host model; exiting processes are not holders.
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.Join(ErrOwnershipUnknown, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrOwnershipUnknown, err)
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		directory := filepath.Join(root, entry.Name(), "fd")
		files, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.Join(ErrOwnershipUnknown, err)
		}
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return errors.Join(ErrOwnershipUnknown, err)
			}
			if _, err := os.Readlink(filepath.Join(directory, file.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrOwnershipUnknown, err)
			}
		}
	}
	return nil
}

func mountedAlias(mounts string, aliases []string) bool {
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "-" || i+2 >= len(fields) {
				continue
			}
			source := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(fields[i+2])
			for _, alias := range aliases {
				if source == alias || source == "ltfs:"+alias {
					return true
				}
			}
			break
		}
	}
	return false
}

func checkHolders(ctx context.Context, aliases []string, run func(context.Context, []string) ([]byte, []byte, error)) error {
	// Resolved aliases are absolute paths; fuser does not consistently accept an end-of-options marker.
	// A clean fuser exit 1 with empty output is the only negative-holder evidence accepted.
	args := append([]string{"-v"}, aliases...)
	stdout, stderr, err := run(ctx, args)
	if err == nil {
		return ErrBusy
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrMissingTool, err)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(stdout) == 0 && len(stderr) == 0 {
		return nil
	}
	return errors.Join(ErrOwnershipUnknown, err)
}

// Reason maps observation failures without requiring clients to parse provider error prose.
func Reason(err error) entity.DeviceReason {
	if err == nil {
		return entity.DeviceReason_DEVICE_REASON_UNSPECIFIED
	}
	switch {
	case errors.Is(err, ErrBusy):
		return entity.DeviceReason_DEVICE_REASON_BUSY
	case errors.Is(err, ErrMissingTool):
		return entity.DeviceReason_DEVICE_REASON_MISSING_TOOL
	case errors.Is(err, ErrUncertain), errors.Is(err, ErrOwnershipUnknown):
		return entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN
	case errors.Is(err, os.ErrPermission):
		return entity.DeviceReason_DEVICE_REASON_PERMISSION
	case errors.Is(err, ErrMalformed):
		return entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE
	case errors.Is(err, ErrUnsupported):
		return entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
	}
	var command *CommandError
	if errors.As(err, &command) {
		if command.Sense.Key == 2 && command.Sense.ASC == 0x3a {
			return entity.DeviceReason_DEVICE_REASON_NO_MEDIUM
		}
		if command.Sense.Key == 2 {
			return entity.DeviceReason_DEVICE_REASON_NOT_READY
		}
		if command.Sense.Key == 5 && (command.Sense.ASC == 0x20 || command.Sense.ASC == 0x24) && command.Sense.ASCQ == 0 &&
			!command.Sense.Filemark && !command.Sense.EOM && !command.Sense.ILI {
			return entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
		}
	}
	return entity.DeviceReason_DEVICE_REASON_TRANSPORT_ERROR
}
