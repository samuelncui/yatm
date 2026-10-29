package tape

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type characterInfo struct{ fs.FileInfo }

func (i characterInfo) Mode() fs.FileMode { return os.ModeDevice | os.ModeCharDevice | 0600 }

func TestResolverUnifiesNativeAndSGAliasesPassively(t *testing.T) {
	// Fake sysfs and native files reproduce published alias links without opening any device.
	root := t.TempDir()
	dev, sys, owner := filepath.Join(root, "dev"), filepath.Join(root, "sys"), filepath.Join(root, "sys", "devices", "1:0:0:0")
	for _, path := range []string{dev, filepath.Join(sys, "class", "scsi_tape", "st0"), filepath.Join(sys, "class", "scsi_generic", "sg4"), filepath.Join(owner, "scsi_generic", "sg4"), filepath.Join(owner, "scsi_tape")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owner, "type"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"scsi_tape/st0", "scsi_generic/sg4"} {
		if err := os.Symlink(owner, filepath.Join(sys, "class", class, "device")); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"st0", "nst0", "nst0a", "nst0l", "nst0m", "sg4"} {
		if err := os.WriteFile(filepath.Join(dev, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if name != "sg4" {
			if err := os.Mkdir(filepath.Join(owner, "scsi_tape", name), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(dev, "nst0a"), filepath.Join(dev, "configured")); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{SysfsRoot: sys, DeviceRoot: dev, stat: func(path string) (fs.FileInfo, error) { info, err := os.Stat(path); return characterInfo{info}, err }}
	owner, err := filepath.EvalSymlinks(owner)
	if err != nil {
		t.Fatal(err)
	}

	// Original configured names stay unchanged while every known alias shares the same resource key.
	for _, name := range []string{"st0", "nst0", "nst0a", "sg4", "configured"} {
		path := filepath.Join(dev, name)
		identity, err := resolver.Resolve(path)
		if err != nil || identity.Configured != path || identity.Key != owner || identity.SG != filepath.Join(dev, "sg4") || len(identity.Aliases) != 6 {
			t.Fatalf("identity=%+v err=%v", identity, err)
		}
	}
	resolver.stat = nil
	if _, err := resolver.Resolve(filepath.Join(dev, "st0")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("regular file accepted as alias: %v", err)
	}
}

func TestOwnershipReasonsAndAliases(t *testing.T) {
	// Ownership queries name all known aliases and distinguish clean absence from incomplete evidence.
	aliases := []string{"/dev/nst0", "/dev/sg4"}
	if !mountedAlias("1 2 0:1 / /mnt rw - fuse.ltfs ltfs:/dev/sg4 rw\n", aliases) {
		t.Fatal("known external mount missed")
	}
	for _, tc := range []struct {
		err            error
		stdout, stderr []byte
		want           error
	}{
		{nil, []byte("123"), nil, ErrBusy},
		{exec.ErrNotFound, nil, nil, ErrMissingTool},
		{os.ErrPermission, nil, nil, ErrOwnershipUnknown},
	} {
		err := checkHolders(context.Background(), aliases, func(_ context.Context, args []string) ([]byte, []byte, error) {
			if !reflect.DeepEqual(args, []string{"-v", "/dev/nst0", "/dev/sg4"}) {
				t.Fatalf("holder args=%v", args)
			}
			return tc.stdout, tc.stderr, tc.err
		})
		if !errors.Is(err, tc.want) {
			t.Fatalf("ownership error=%v, want=%v", err, tc.want)
		}
	}
}
