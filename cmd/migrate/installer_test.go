package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func installerShell(t *testing.T, script, input string, args ...string) (string, error) {
	t.Helper()
	// Execute the sourced installer against isolated test paths and process-local doubles.
	installer, err := filepath.Abs("../../install-release.sh")
	require.NoError(t, err)
	command := exec.Command("bash", append([]string{"-c", "source \"$1\"\nshift\n" + script, "installer-test", installer}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestInstallerVersionOrdering(t *testing.T) {
	output, err := installerShell(t, `
[[ "$(version_key v1.0.0-alpha.1)" < "$(version_key v1.0.0-alpha.2)" ]]
[[ "$(version_key v1.0.0-alpha.10)" < "$(version_key v1.0.0-beta.1)" ]]
[[ "$(version_key v1.0.0-rc.9)" < "$(version_key v1.0.0)" ]]
[[ "$(version_key v0.1.21)" < "$(version_key v1.0.0-alpha.1)" ]]
! version_key '../unsafe'
parse_options --version 1.0.0-alpha.1
[[ "$RELEASE_VERSION" == v1.0.0-alpha.1 ]]
parse_options --version v1.0.0-alpha.1
[[ "$RELEASE_VERSION" == v1.0.0-alpha.1 ]]
`, "")
	require.NoError(t, err, output)
}

func TestInstallerOptionValues(t *testing.T) {
	for _, option := range []string{"--version", "--archive", "--checksum", "--install-dir", "--service", "--config"} {
		for _, extra := range []string{"", "--check"} {
			output, err := installerShell(t, `parse_options "$1" "$2"`, "", option, extra)
			require.Error(t, err, output)
			require.Contains(t, output, "Missing value for "+option)
		}
	}

	// Help still consumes and validates all supplied options before printing usage.
	output, err := installerShell(t, `parse_options --help --unexpected`, "")
	require.Error(t, err, output)
	require.Contains(t, output, "Unknown option: --unexpected")
}

func TestInstallerFailureBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, failure, input string
		wantSuccess          bool
		want, absent         []string
	}{
		{name: "success", input: "y\n", wantSuccess: true, want: []string{"phase:quiesce", "service:stop", "backup", "phase:prepare", "phase:commit", "phase:validate", "phase:cleanup", "replacement-allowed"}, absent: []string{"phase:abort", "service:start"}},
		{name: "prepare failure", failure: "prepare", want: []string{"phase:abort", "service:start", "Stage: reversed"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report declined", input: "n\n", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report EOF", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "commit failure", failure: "commit", input: "y\n", want: []string{"phase:commit", "Stage: commit", "service:stop"}, absent: []string{"phase:abort", "service:start", "replacement-allowed"}},
		{name: "validation failure", failure: "validate", input: "y\n", want: []string{"Stage: validate"}, absent: []string{"phase:cleanup", "replacement-allowed"}},
		{name: "cleanup failure", failure: "cleanup", input: "y\n", want: []string{"Stage: cleanup"}, absent: []string{"replacement-allowed"}},
		{name: "backup failure", failure: "backup", want: []string{"Stage: backup"}, absent: []string{"phase:prepare", "replacement-allowed"}},
		{name: "abort failure", failure: "abort", input: "n\n", want: []string{"Abort failed", "phase:abort"}, absent: []string{"service:start", "replacement-allowed"}},
		{name: "busy", failure: "quiesce", want: []string{"phase:quiesce"}, absent: []string{"service:stop", "backup", "phase:prepare"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Inject failures at the service/backup/migration boundary, not inside the domain conversion.
			output, err := installerShell(t, `
SCHEMA=legacy
SERVICE_ACTIVE=1
FAIL_PHASE="$1"
REPORT_DIRECTORY=/test-reports
run_migrator() { echo "phase:$2"; [[ "$2" != "$FAIL_PHASE" ]]; }
systemctl() { echo "service:$1"; }
backup_installation() { STAGE=backup; echo backup; [[ "$FAIL_PHASE" != backup ]]; }
trap 'on_failure "$?"' ERR
upgrade_existing
echo replacement-allowed
`, test.input, test.failure)

			// No failed or declined migration may reach replacement or restart an uncertain catalog.
			if test.wantSuccess {
				require.NoError(t, err, output)
			} else {
				require.Error(t, err, output)
			}
			for _, want := range test.want {
				require.Contains(t, output, want)
			}
			for _, absent := range test.absent {
				require.NotContains(t, output, absent)
			}
		})
	}
}

func TestInstallerPreservesUserOwnedFiles(t *testing.T) {
	// Include obsolete release resources and customized files with their original modes.
	root := t.TempDir()
	installed := filepath.Join(root, "installed")
	candidate := filepath.Join(root, "candidate")
	for _, file := range []string{"config.yaml", "scripts/mount", "helper", "yatm-httpd.service", "local-note", "yatm-httpd", "frontend/obsolete.js", "docs/obsolete.md"} {
		path := filepath.Join(installed, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("existing "+file), 0600))
	}
	for _, file := range []string{
		"templates/config.example.yaml", "templates/scripts/mount", "templates/yatm-httpd.service",
		"yatm-httpd", "yatm-cli", "yatm-export-library", "yatm-lto-info", "yatm-migrate", "install-release.sh",
		"VERSION", "COMMIT", "README.md", "CONTEXT.md", "LICENSE", "licenses/example.txt",
		"skills/yatm/SKILL.md", "frontend/index.html", "docs/new.md",
	} {
		path := filepath.Join(candidate, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("candidate "+file), 0600))
	}

	// Replace only release-owned resources; upgrades cannot opt into script or unit replacement.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
RELEASE_DIRECTORY="$2"
CURRENT_VERSION=v0.1.21
install_managed_files
`, "", installed, candidate)
	require.NoError(t, err, output)
	for _, file := range []string{"config.yaml", "scripts/mount", "helper", "yatm-httpd.service", "local-note"} {
		data, err := os.ReadFile(filepath.Join(installed, file))
		require.NoError(t, err)
		require.Equal(t, "existing "+file, string(data))
		info, err := os.Stat(filepath.Join(installed, file))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(installed, "yatm-httpd"))
	require.NoError(t, err)
	require.Equal(t, "candidate yatm-httpd", string(data))
	require.NoFileExists(t, filepath.Join(installed, "frontend/obsolete.js"))
	require.NoFileExists(t, filepath.Join(installed, "docs/obsolete.md"))
}

func TestInstallerCancellationDoesNotInterrupt(t *testing.T) {
	for _, input := range []string{"n\n", ""} {
		// A verified candidate carries the only explanation presented before consent.
		root := t.TempDir()
		output, err := installerShell(t, `
INSTALL_DIRECTORY="$1/yatm"
identify_platform() { :; }
flock() { :; }
resolve_version() { CURRENT_VERSION=v0.1.21; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { mkdir -p "$RELEASE_DIRECTORY/docs/operations"; printf 'Candidate migration guide\n' > "$RELEASE_DIRECTORY/docs/operations/migration.md"; }
inspect_installation() { :; }
systemctl() { echo unexpected-service-operation; return 1; }
run_migrator() { echo unexpected-migration-operation; return 1; }
main
`, input, root)

		// Both explicit cancellation and EOF preserve the report and avoid service changes.
		require.NoError(t, err, output)
		require.Contains(t, output, "Candidate migration guide")
		require.Contains(t, output, "Installation cancelled")
		require.NotContains(t, output, "unexpected-")
		reports, err := filepath.Glob(filepath.Join(root, "yatm/.yatm-upgrades/*/reports/upgrade.log"))
		require.NoError(t, err)
		require.Len(t, reports, 1)
		data, err := os.ReadFile(reports[0])
		require.NoError(t, err)
		require.Contains(t, string(data), "Installation cancelled")
	}
}

func TestInstallerReadOnlyCheckDoesNotInterrupt(t *testing.T) {
	// Read-only checking keeps its candidate outside the installation and never creates retention data.
	root := t.TempDir()
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1/yatm"
identify_platform() { :; }
resolve_version() { CURRENT_VERSION=v0.1.21; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { mkdir -p "$RELEASE_DIRECTORY/docs/operations"; printf 'Candidate guide\n' > "$RELEASE_DIRECTORY/docs/operations/migration.md"; }
inspect_installation() { echo read-only-inspection; }
systemctl() { echo unexpected-service-operation; return 1; }
main --check
`, "", root)
	require.NoError(t, err, output)
	require.Contains(t, output, "Read-only installation checks passed")
	require.NotContains(t, output, "unexpected-")
	require.NoDirExists(t, filepath.Join(root, "yatm"))
}

func TestInstallerFreshCheckUsesFinalDirectory(t *testing.T) {
	// Stage the supplied configuration separately without changing the final installation path.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "release/templates/scripts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "provided.yaml"), []byte("database: {}\n"), 0600))
	output, err := installerShell(t, `
TMP_DIRECTORY="$1"
INSTALL_DIRECTORY="$1/installed"
RELEASE_DIRECTORY="$1/release"
FRESH_CONFIG="$1/provided.yaml"
CURRENT_VERSION=none
CHECK_ONLY=1
run_migrator() {
  [[ "$*" == *"-install-root $INSTALL_DIRECTORY"* && "$*" == *-fresh-install* ]]
  printf '%s\n' '{"schema":"empty","server_url":"http://127.0.0.1:8080","warnings":[]}'
}
inspect_installation
`, "", root)

	// Absolute config paths are interpreted by fresh inspection, not by the staging directory.
	require.NoError(t, err, output)
	require.NoDirExists(t, filepath.Join(root, "installed"))
}

func TestInstallerPostReplacementFailureKeepsServiceStopped(t *testing.T) {
	for _, stage := range []string{"commit", "validate", "cleanup", "replace-programs", "readiness"} {
		output, err := installerShell(t, `
STAGE="$1"
systemctl() { echo "service:$1"; }
on_failure 1
`, "", stage)
		require.Error(t, err)
		require.Contains(t, output, "service:stop")
		require.NotContains(t, output, "service:start")
	}
}

func TestInstallerReadinessRequiresTargetProcess(t *testing.T) {
	for _, test := range []struct {
		name, pid, response string
		changePID, inactive bool
		wantSuccess         bool
	}{
		{name: "matching process", pid: "101", response: `{"process_id":101}`, wantSuccess: true},
		{name: "foreign ready endpoint", pid: "101", response: `{"process_id":202}`},
		{name: "missing process identity", pid: "101", response: `{}`},
		{name: "no running process", pid: "0", response: `{"process_id":0}`},
		{name: "inactive service", pid: "101", response: `{"process_id":101}`, inactive: true},
		{name: "restart during checks", pid: "101", response: `{"process_id":101}`, changePID: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Keep the endpoint ready while independently controlling the target service identity.
			root := t.TempDir()
			for name, body := range map[string]string{
				"pid":          test.pid,
				"status.json":  test.response,
				"curl":         "#!/bin/sh\ncat \"$TEST_STATUS\"\n",
				"yatm-cli":     "#!/bin/sh\nprintf 'checked-cli:%s\\n' \"$*\"\n",
				"yatm-migrate": "#!/bin/sh\nif [ \"$CHANGE_PID\" = true ]; then printf '202' > \"$TEST_PID\"; fi\nprintf 'checked-frontend\\n'\n",
			} {
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0700))
			}
			changePID, inactive := "false", "false"
			if test.changePID {
				changePID = "true"
			}
			if test.inactive {
				inactive = "true"
			}
			output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
TMP_DIRECTORY="$1"
SERVER_URL=http://127.0.0.1:18671
SERVICE_NAME=target.service
STAGE=readiness
export PATH="$1:$PATH" TEST_PID="$1/pid" TEST_STATUS="$1/status.json" CHANGE_PID="$2"
INACTIVE="$3"
systemctl() {
  case "$1" in
    show) cat "$TEST_PID" ;;
    is-active) [[ "$INACTIVE" == false ]] ;;
    stop) echo "stopped:$2" ;;
    *) return 1 ;;
  esac
}
sleep() { :; }
trap 'on_failure "$?"' ERR
check_readiness
echo accepted
`, "", root, changePID, inactive)

			// A foreign endpoint or restart cannot make an unrelated service pass activation.
			if test.wantSuccess {
				require.NoError(t, err, output)
				require.Contains(t, output, "accepted")
				for file, operation := range map[string]string{"library.json": "files list", "jobs.json": "job list"} {
					data, err := os.ReadFile(filepath.Join(root, file))
					require.NoError(t, err)
					require.Contains(t, string(data), operation)
				}
				require.NotContains(t, output, "stopped:")
				return
			}
			require.Error(t, err, output)
			require.NotContains(t, output, "accepted")
			require.Contains(t, output, "stopped:target.service")
			if test.changePID {
				require.Contains(t, output, "checked-frontend")
				require.Contains(t, output, "changed or stopped")
				return
			}
			require.NotContains(t, output, "checked-cli")
			require.Contains(t, output, "does not belong")
		})
	}
}

func TestInstallerMissingGuidePreventsStop(t *testing.T) {
	// Reject incomplete major-upgrade candidates after inspection but before consent or service mutation.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1/yatm"
identify_platform() { :; }
flock() { :; }
resolve_version() { CURRENT_VERSION=v0.1.8; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { mkdir "$RELEASE_DIRECTORY"; }
inspect_installation() { :; }
systemctl() { echo unexpected-service-operation; return 1; }
main
`, "y\n", t.TempDir())
	require.Error(t, err, output)
	require.Contains(t, output, "missing its migration guide")
	require.NotContains(t, output, "unexpected-")
}

func TestInstallerSameVersionStillOffersSkill(t *testing.T) {
	// Installed programs agree on the release identity; a rerun is not a replacement operation.
	root := t.TempDir()
	for _, program := range []string{"yatm-httpd", "yatm-cli", "yatm-migrate", "yatm-export-library", "yatm-lto-info"} {
		body := "#!/bin/sh\nprintf '%s\\n' '{\"program\":\"" + program +
			"\",\"version\":\"v1.0.0-alpha.1\",\"commit\":\"candidate\"}'\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, program), []byte(body), 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "COMMIT"), []byte("candidate"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
identify_platform() { :; }
flock() { :; }
resolve_version() { CURRENT_VERSION=v1.0.0-alpha.1; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { :; }
inspect_installation() { :; }
offer_skill() { echo offer-skill; }
backup_installation() { echo unexpected-backup; return 1; }
systemctl() { echo unexpected-service-operation; return 1; }
main
`, "", root)

	// The rerun preserves all active files while keeping the optional installation entry point.
	require.NoError(t, err, output)
	require.Contains(t, output, "already installed")
	require.Contains(t, output, "offer-skill")
	require.NotContains(t, output, "unexpected-")
}

func TestInstallerSkillWithoutNodeIsOptional(t *testing.T) {
	// Restrict the test user's dependency lookup without modifying the host tool installation.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "skills/yatm"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "skills/yatm/SKILL.md"), []byte("fixture"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0700))
	require.NoError(t, os.Symlink("/bin/sh", filepath.Join(root, "bin/sh")))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
SUDO_USER=skill-fixture
id() { if [[ "$1" == -un ]]; then echo skill-fixture; fi; }
export PATH="$1/bin"
offer_skill
echo installation-unaffected
`, "", root)

	// Missing optional dependencies are reported without installing tools or failing YATM.
	require.NoError(t, err, output)
	require.Contains(t, output, "Node/npm are not available")
	require.Contains(t, output, "They were not installed")
	require.Contains(t, output, "installation-unaffected")
}

func TestInstallerRejectsUnknownInstalledVersion(t *testing.T) {
	// Version detection never interprets an unrecognized existing installation as a fresh install.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "VERSION"), []byte("development"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
RELEASE_VERSION=v1.0.0-alpha.1
resolve_version
`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "Installed VERSION is unrecognized")
}

func TestInstallerRejectsChecksumBeforeCandidateExecution(t *testing.T) {
	// A bad checksum fails before any candidate is extracted or executed.
	root := t.TempDir()
	archive := filepath.Join(root, "candidate.tar.gz")
	checksum := filepath.Join(root, "candidate.sha256")
	require.NoError(t, os.WriteFile(archive, []byte("invalid candidate"), 0600))
	require.NoError(t, os.WriteFile(checksum, []byte(strings.Repeat("0", 64)), 0600))
	output, err := installerShell(t, `
TMP_DIRECTORY="$1"
RELEASE_DIRECTORY="$1/release"
RELEASE_VERSION=v1.0.0-alpha.1
LOCAL_ARCHIVE="$2"
CHECKSUM_FILE="$3"
download_release
`, "", root, archive, checksum)
	require.Error(t, err, output)
	require.Contains(t, output, "Release checksum mismatch")
	require.NoDirExists(t, filepath.Join(root, "release"))
}

func TestInstallerRejectsReservedDirectoryConflict(t *testing.T) {
	// Existing user data at the reserved path is never adopted or overwritten.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".yatm-upgrades"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".yatm-upgrades/user-note"), []byte("keep"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
begin_attempt
`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "not an installer-owned directory")
	require.NoFileExists(t, filepath.Join(root, ".yatm-upgrades/OWNER"))
}

func TestInstallerBackupsRemainInsideInstallation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("The supported installer uses Linux GNU tar comparison and flock; covered by Linux acceptance")
	}

	// Include dotfiles, a user backup, a helper and linked content in the complete installation.
	root := t.TempDir()
	installed := filepath.Join(root, "yatm")
	for _, file := range []string{"VERSION", ".secret", "user.backup", "scripts/helper", "content"} {
		path := filepath.Join(installed, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("original "+file), 0640))
	}
	require.NoError(t, os.Link(filepath.Join(installed, "content"), filepath.Join(installed, "hard-link")))
	require.NoError(t, os.Symlink("content", filepath.Join(installed, "soft-link")))

	// Two upgrade attempts preserve the first snapshot byte-for-byte and omit retained attempts only.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
CURRENT_VERSION=v0.1.8
begin_attempt
backup_installation
first_backup="$BACKUP_DIRECTORY"
printf 'new version\n' > "$INSTALL_DIRECTORY/VERSION"
flock -u 9
exec 9>&-
CURRENT_VERSION=v1.0.0-alpha.1
begin_attempt
backup_installation
[[ ! -e "$BACKUP_DIRECTORY/.yatm-upgrades" && ! -e "$first_backup/.yatm-upgrades" ]]
[[ "$(< "$first_backup/VERSION")" == 'original VERSION' ]]
[[ "$(< "$BACKUP_DIRECTORY/VERSION")" == 'new version' ]]
[[ "$(stat -c '%a' "$first_backup")" == 700 ]]
[[ "$(stat -c '%a' "$BACKUP_DIRECTORY/scripts/helper")" == 640 ]]
[[ "$BACKUP_DIRECTORY/content" -ef "$BACKUP_DIRECTORY/hard-link" ]]
[[ "$(readlink "$BACKUP_DIRECTORY/soft-link")" == content ]]
`, "", installed)
	require.NoError(t, err, output)
	require.Equal(t, 2, strings.Count(output, "Passed: complete backup"))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "retained files must not leak into the installation parent")
}

func TestInstallerBackupMismatchStopsBeforeMigration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("The supported installer verifies backups with Linux GNU tar")
	}

	// Deliberately corrupt the snapshot after a successful copy to exercise actual verification.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "VERSION"), []byte("v0.1.8"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
CURRENT_VERSION=v0.1.8
SCHEMA=legacy
begin_attempt
systemctl() { :; }
run_migrator() { [[ "$2" == quiesce ]]; }
cp() { command cp "$@"; printf 'damaged backup' > "$BACKUP_DIRECTORY/VERSION"; }
trap 'on_failure "$?"' ERR
upgrade_existing
echo unexpected-migration-complete
`, "y\n", root)
	require.Error(t, err, output)
	require.Contains(t, output, "Stage: backup")
	require.NotContains(t, output, "unexpected-migration-complete")
}
