package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/config"
	"github.com/stretchr/testify/require"
)

func TestMigrationBackupMapsEvidenceButPreservesRestoreDestination(t *testing.T) {
	// The preserved configuration contains the original absolute installation paths.
	root := t.TempDir()
	backupRoot := filepath.Join(root, ".yatm-upgrades", "attempt", "legacy.backup")
	require.NoError(t, os.MkdirAll(filepath.Join(backupRoot, "scripts"), 0o700))
	text := fmt.Sprintf("database:\n  dialect: sqlite\n  dsn: %s\npaths:\n  work: %s\n  target: ./restore-here\nscripts:\n  mount: ./scripts/mount\n", filepath.Join(root, "tapes.db"), root)
	require.NoError(t, os.WriteFile(filepath.Join(backupRoot, "config.yaml"), []byte(text), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(backupRoot, "scripts", "mount"), []byte("capture_index="+filepath.Join(root, "old-indexes")+"\n"), 0o700))

	// Neither backup repair nor validation may reopen the active catalog or freeze a backup output path.
	backup, err := migrationBackup(root, backupRoot, "config.yaml")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(backupRoot, "tapes.db"), backup.CatalogPath)
	require.Equal(t, backupRoot, backup.WorkRoot)
	require.Equal(t, filepath.Join(backupRoot, "old-indexes"), backup.IndexRoot)
	canonicalTarget, err := canonicalExisting(filepath.Join(root, "restore-here"))
	require.NoError(t, err)
	require.Equal(t, canonicalTarget, backup.RestoreRoot)
	require.NoDirExists(t, backup.RestoreRoot)

	// Absolute links pointing back to the live installation cannot masquerade as preserved evidence.
	require.NoError(t, os.Symlink(filepath.Join(root, "tapes.db"), filepath.Join(backupRoot, "tapes.db")))
	_, err = migrationBackup(root, backupRoot, "config.yaml")
	require.ErrorContains(t, err, "escapes the preserved installation")
}

func TestMigrationBackupPreservesRelativeSymlinkChainAfterExtraction(t *testing.T) {
	// Preserve a legacy installation's relative catalog and work links in an actual archive.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "storage"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "storage", "tapes.db"), []byte("preserved catalog"), 0o600))
	require.NoError(t, os.Symlink("storage", filepath.Join(root, "current")))
	require.NoError(t, os.Symlink("current", filepath.Join(root, "jobs")))
	text := "database:\n  dialect: sqlite\n  dsn: ./jobs/tapes.db\npaths:\n  work: ./jobs\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(text), 0o600))
	archive := filepath.Join(t.TempDir(), "legacy.tar")
	output, err := exec.Command("tar", "-cf", archive, "-C", root, ".").CombinedOutput()
	require.NoError(t, err, string(output))
	backupRoot := t.TempDir()
	output, err = exec.Command("tar", "-xf", archive, "-C", backupRoot).CombinedOutput()
	require.NoError(t, err, string(output))

	// Backup evidence follows the preserved chain, never a changed active catalog.
	require.NoError(t, os.WriteFile(filepath.Join(root, "storage", "tapes.db"), []byte("active catalog"), 0o600))
	backup, err := migrationBackup(root, backupRoot, "config.yaml")
	require.NoError(t, err)
	data, err := os.ReadFile(backup.CatalogPath)
	require.NoError(t, err)
	require.Equal(t, "preserved catalog", string(data))
	link, err := os.Readlink(filepath.Join(backupRoot, "jobs"))
	require.NoError(t, err)
	require.Equal(t, "current", link)
	data, err = os.ReadFile(filepath.Join(root, "jobs", "tapes.db"))
	require.NoError(t, err)
	require.Equal(t, "active catalog", string(data))
}

func TestFreshInspectionUsesFutureInstallationRootWithoutWrites(t *testing.T) {
	// A supplied configuration may name its future installed paths before the directory exists.
	root := filepath.Join(t.TempDir(), "install")
	conf := &config.Config{Listen: ":9092"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "tapes.db")
	conf.Paths.Work = root
	report, err := inspectFreshInstallation(conf, root)
	require.NoError(t, err)
	require.Equal(t, "empty", string(report.Schema))
	require.Equal(t, "http://127.0.0.1:9092", report.ServerURL)
	require.NoDirExists(t, root)

	// Relative and URI database paths resolve against the destination, not temporary package extraction.
	for _, dsn := range []string{"tapes.db", "file:tapes.db?cache=private"} {
		conf.Database.DSN = dsn
		_, err := inspectFreshInstallation(conf, root)
		require.NoError(t, err)
	}
	conf.Database.DSN = filepath.Join(t.TempDir(), "external.db")
	_, err = inspectFreshInstallation(conf, root)
	require.ErrorContains(t, err, "inside the installation root")
	require.NoDirExists(t, root)
}
