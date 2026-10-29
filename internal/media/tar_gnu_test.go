package media

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/acp"
)

func TestTarGNUExtraction(t *testing.T) {
	// Automatic discovery accepts the usual GNU names only after checking the implementation.
	gnu := os.Getenv("YATM_GNU_TAR")
	if gnu == "" {
		for _, name := range []string{"gtar", "tar"} {
			candidate, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			version, err := exec.Command(candidate, "--version").CombinedOutput()
			if err == nil && bytes.Contains(version, []byte("GNU tar")) {
				gnu = candidate
				break
			}
		}
	}
	if gnu == "" {
		t.Skip("GNU tar unavailable; set YATM_GNU_TAR to run public-tool acceptance")
	}
	version, err := exec.Command(gnu, "--version").CombinedOutput()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Fatalf("not GNU tar: %q, %v", version, err)
	}
	d := &tarTestDevice{}
	s, _ := newTarTestWriteSession(d, tarTestOptions(t, false, 4096))
	name := strings.Repeat("long/", 40) + "file"
	writeTarTestMember(t, s, name, []byte("public recovery"), map[string][]byte{"user.empty": {}, "user.binary": {0, '\n', 255}})
	if _, err := s.Finalize(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	// Independent extraction needs only ordinary tar bytes and the public tool.
	root := t.TempDir()
	archive := filepath.Join(root, "content.tar")
	if err := os.WriteFile(archive, tarTestBytes(d), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(gnu, "--xattrs", "--same-permissions", "--no-same-owner",
		"-xf", archive, "-C", root).CombinedOutput(); err != nil {
		t.Fatalf("GNU extraction failed: %s, %v", output, err)
	}
	extracted := filepath.Join(root, filepath.FromSlash(tarPrefix(1)+"/"+name))
	data, err := os.ReadFile(extracted)
	if err != nil || string(data) != "public recovery" {
		t.Fatalf("GNU extracted wrong bytes: %q, %v", data, err)
	}

	// The public recovery flags preserve permissions and the original subsecond modification time.
	info, err := os.Stat(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 || !info.ModTime().Equal(tarTestInfo{}.ModTime()) {
		t.Fatalf("GNU changed metadata: mode=%o mtime=%s", info.Mode().Perm(), info.ModTime())
	}

	// GNU re-archives the extracted filesystem attrs; its PAX output proves raw empty/binary preservation.
	repacked := filepath.Join(root, "gnu.tar")
	if output, err := exec.Command(gnu, "--format=pax", "--xattrs", "--xattrs-include=*",
		"-cf", repacked, "-C", root, tarPrefix(1)+"/"+name).CombinedOutput(); err != nil {
		t.Fatalf("GNU rearchive failed: %s, %v", output, err)
	}
	gnuBytes, err := os.ReadFile(repacked)
	if err != nil {
		t.Fatal(err)
	}
	gnuReader := tar.NewReader(bytes.NewReader(gnuBytes))
	gnuHeader, err := gnuReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if value, exists := gnuHeader.PAXRecords["SCHILY.xattr.user.empty"]; !exists || value != "" {
		t.Fatal("GNU extraction/rearchive lost empty xattr")
	}
	if gnuHeader.PAXRecords["SCHILY.xattr.user.binary"] != string([]byte{0, '\n', 255}) {
		t.Fatal("GNU extraction/rearchive changed binary xattr")
	}

	// Our reader accepts GNU-produced PAX framing, independently of our writer's header choices.
	imported := &tarTestDevice{records: []tarTestRecord{{mark: true}, {data: gnuBytes}, {mark: true}}}
	read, err := NewTarReadSession(imported, tarTestOptions(t, false, 16<<10))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := read.WalkInventory(context.Background(), func(entry *InventoryEntry) error {
		count++
		if entry.Path != tarPrefix(1)+"/"+name {
			t.Fatal("GNU member path changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("GNU inventory count=%d", count)
	}

	// GNU recovers complete members before a warning-truncated tail despite its nonzero exit.
	partial := &tarTestDevice{warningAt: 1}
	write, err := newTarTestWriteSession(partial, tarTestOptions(t, false, 4096))
	if err != nil {
		t.Fatal(err)
	}
	writeTarTestMember(t, write, "before-tail", []byte("saved"), nil)
	ref, _ := write.Target("tail", 10000)
	w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "tail", size: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	_, cause := w.Write(make([]byte, 10000))
	_ = w.Finish(cause)
	if _, err := write.Finalize(context.Background(), cause); err != nil {
		t.Fatal(err)
	}
	tailArchive := filepath.Join(root, "truncated.tar")
	if err := os.WriteFile(tailArchive, tarTestBytes(partial), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(gnu, "-xf", tailArchive, "-C", root).CombinedOutput(); err == nil {
		t.Fatalf("GNU accepted incomplete archive without error: %s", output)
	}
	before, err := os.ReadFile(filepath.Join(root, tarPrefix(1), "before-tail"))
	if err != nil || string(before) != "saved" {
		t.Fatalf("GNU failed recovery before tail: %q, %v", before, err)
	}
}
