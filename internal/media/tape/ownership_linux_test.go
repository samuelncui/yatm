package tape

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOwnershipWithInstalledFuser(t *testing.T) {
	// Real process-table visibility is an integration prerequisite, not a mocked absence of holders.
	if _, err := exec.LookPath("fuser"); err != nil {
		t.Skipf("installed fuser is unavailable: %v", err)
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkVisibility(context.Background(), "/proc", string(mounts)); err != nil {
		t.Skipf("process descriptor tables are not fully visible: %v", err)
	}

	// Ordinary isolated files exercise the actual fuser invocation without accessing a device.
	root := t.TempDir()
	aliases := []string{filepath.Join(root, "nst0"), filepath.Join(root, "sg0")}
	for _, alias := range aliases {
		if err := os.WriteFile(alias, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity := Identity{Key: root, Aliases: aliases}
	if err := CheckOwnership(context.Background(), identity); err != nil {
		t.Fatalf("unused aliases were not accepted: %v", err)
	}

	// A live descriptor must remain busy after accepting the clean negative result above.
	holder, err := os.Open(aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := CheckOwnership(context.Background(), identity); !errors.Is(err, ErrBusy) {
		t.Fatalf("held alias was not busy: %v", err)
	}
}
