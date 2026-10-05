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

	output, err = installerShell(t, `usage`, "")
	require.NoError(t, err, output)
	for _, tool := range []string{"curl", "jq", "tar", "sha256sum", "systemctl", "cp", "du", "df", "readlink", "mktemp", "find", "flock", "tee", "awk", "grep", "sed", "mv", "rm", "mkdir", "chmod", "date", "sleep", "id", "uname", "cat"} {
		require.Contains(t, output, tool)
	}
}

func TestInstallerFailureBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, failure, input string
		wantSuccess          bool
		want, absent         []string
	}{
		{
			name: "success", input: "y\n", wantSuccess: true,
			want: []string{
				"phase:quiesce", "service:stop", "backup", "phase:prepare",
				"phase:commit\nverify-backup\nextract-backup\nphase:cleanup\nphase:config-apply\nreplacement-allowed",
			},
			absent: []string{"phase:validate", "phase:abort", "service:start"},
		},
		{name: "prepare failure", failure: "prepare", want: []string{"phase:abort", "service:start", "Stage: reversed"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report declined", input: "n\n", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report EOF", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "commit failure", failure: "commit", input: "y\n", want: []string{"phase:commit", "Stage: commit", "service:stop"}, absent: []string{"phase:abort", "service:start", "replacement-allowed"}},
		{
			name: "backup checksum failure", failure: "checksum", input: "y\n",
			want:   []string{"verify-backup", "Stage: commit"},
			absent: []string{"extract-backup", "phase:cleanup", "phase:config-apply", "replacement-allowed", "service:start"},
		},
		{
			name: "backup extraction failure", failure: "extract", input: "y\n",
			want:   []string{"extract-backup", "Stage: commit"},
			absent: []string{"phase:cleanup", "phase:config-apply", "replacement-allowed", "service:start"},
		},
		{
			name: "cleanup failure", failure: "cleanup", input: "y\n",
			want:   []string{"phase:cleanup", "Stage: cleanup", "service:stop"},
			absent: []string{"phase:validate", "phase:abort", "phase:config-apply", "replacement-allowed", "service:start"},
		},
		{name: "backup failure", failure: "backup", want: []string{"Stage: backup"}, absent: []string{"phase:prepare", "replacement-allowed"}},
		{name: "abort failure", failure: "abort", input: "n\n", want: []string{"Abort failed", "phase:abort"}, absent: []string{"service:start", "replacement-allowed"}},
		{name: "busy", failure: "quiesce", want: []string{"phase:quiesce"}, absent: []string{"service:stop", "\nbackup\n", "phase:prepare"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Inject failures at the service/backup/migration boundary, not inside the domain conversion.
			output, err := installerShell(t, `
SCHEMA=legacy
SERVICE_ACTIVE=1
FAIL_PHASE="$1"
WORK_DIRECTORY="$2"
BACKUP_DIRECTORY="$2"
INSTALL_DIRECTORY="$2/install"
REPORT_DIRECTORY="$2"
CONFIG_CHANGED=true
run_migrator() {
  echo "phase:$2"
  if [[ "$2" == cleanup ]]; then
    [[ "$*" == "-phase cleanup -backup-root $WORK_DIRECTORY/backup -install-root $INSTALL_DIRECTORY --confirm" ]] || return 1
  fi
  [[ "$2" != "$FAIL_PHASE" ]]
}
systemctl() { echo "service:$1"; }
quiesce_and_stop() { run_migrator -phase quiesce --confirm; STAGE=stopping; systemctl stop "$SERVICE_NAME"; }
backup_installation() { STAGE=backup; echo backup; [[ "$FAIL_PHASE" != backup ]]; }
sha256sum() { echo verify-backup; [[ "$FAIL_PHASE" != checksum ]]; }
tar() { echo extract-backup; [[ "$FAIL_PHASE" != extract ]]; }
trap 'on_failure "$?"' ERR
upgrade_existing
echo replacement-allowed
`, test.input, test.failure, t.TempDir())

			// No failed or declined migration may reach replacement or restart an uncertain catalog.
			if test.wantSuccess {
				require.NoError(t, err, output)
			} else {
				require.Error(t, err, output)
			}
			require.LessOrEqual(t, strings.Count(output, "phase:cleanup"), 1, output)
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
	for _, file := range []string{"config.yaml", "config.example.yaml", "scripts/mount", "helper", "yatm-httpd.service", "local-note", "yatm-httpd", "frontend/obsolete.js", "docs/obsolete.md"} {
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
BACKUP_COMPLETE=1
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
	require.NoFileExists(t, filepath.Join(installed, "config.example.yaml"))
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
plan_configuration() { CONFIG_CHANGED=false; }
systemctl() { echo unexpected-service-operation; return 1; }
run_migrator() { echo unexpected-migration-operation; return 1; }
main
`, input, root)

		// Both explicit cancellation and EOF preserve the report and avoid service changes.
		require.NoError(t, err, output)
		require.Contains(t, output, "Candidate migration guide")
		require.Contains(t, output, "Installation cancelled")
		require.NotContains(t, output, "unexpected-")
		reports, err := filepath.Glob(filepath.Join(root, "yatm/.backup/*/upgrade.log"))
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
plan_configuration() { :; }
systemctl() { echo unexpected-service-operation; return 1; }
main --check
`, "", root)
	require.NoError(t, err, output)
	require.Contains(t, output, "Read-only installation checks passed")
	require.Contains(t, output, "Migration guide from the verified release")
	require.Contains(t, output, "Read this guide before the mutating run")
	require.NotContains(t, output, "Candidate guide\n")
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
verify_service_ownership() { :; }
inspect_installation
`, "", root)

	// Absolute config paths are interpreted by fresh inspection, not by the staging directory.
	require.NoError(t, err, output)
	require.NoDirExists(t, filepath.Join(root, "installed"))
}

func TestInstallerPostReplacementFailureKeepsServiceStopped(t *testing.T) {
	for _, stage := range []string{"commit", "cleanup", "replace-programs", "readiness"} {
		// An uncertain catalog or mixed program tree must stay offline until complete-backup recovery.
		output, err := installerShell(t, `
STAGE="$1"
INSTALLATION_CHANGED=1
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
				for file, operation := range map[string]string{"library.json": "ls", "jobs.json": "job list --limit 1"} {
					data, err := os.ReadFile(filepath.Join(root, file))
					require.NoError(t, err)
					require.Equal(t, "checked-cli:--server http://127.0.0.1:18671 --timeout 3s "+operation+"\n", string(data))
				}
				require.NotContains(t, output, "stopped:")
				return
			}
			require.Error(t, err, output)
			require.NotContains(t, output, "accepted")
			require.NotContains(t, output, "stopped:target.service")
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
	require.NoError(t, os.WriteFile(filepath.Join(root, "VERSION"), []byte("v1.0.0-alpha.1"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
identify_platform() { :; }
flock() { :; }
resolve_version() { CURRENT_VERSION=v1.0.0-alpha.1; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { mkdir -p "$RELEASE_DIRECTORY"; printf candidate > "$RELEASE_DIRECTORY/COMMIT"; }
inspect_installation() { :; }
plan_configuration() { CONFIG_CHANGED=false; }
offer_skill() { echo offer-skill; }
managed_install_matches_candidate() { :; }
check_readiness() { echo checked-readiness; }
backup_installation() { echo unexpected-backup; return 1; }
systemctl() { [[ "$1" == start ]] && { echo service:start; return; }; echo unexpected-service-operation; return 1; }
main
`, "", root)

	// The rerun preserves all active files while keeping the optional installation entry point.
	require.NoError(t, err, output)
	require.Contains(t, output, "already installed")
	require.Contains(t, output, "offer-skill")
	require.Contains(t, output, "service:start")
	require.Contains(t, output, "checked-readiness")
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
	// A non-directory at the archive retention path is never adopted or overwritten.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".backup"), []byte("keep"), 0600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
flock() { :; }
begin_attempt
`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "Reserved .backup path is not a directory")
	data, readErr := os.ReadFile(filepath.Join(root, ".backup"))
	require.NoError(t, readErr)
	require.Equal(t, "keep", string(data))
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
run_migrator() { [[ "$2" == quiesce || "$2" == config-check ]]; }
tar() { [[ "$*" != *-dzf* ]] || return 1; command tar "$@"; }
trap 'on_failure "$?"' ERR
upgrade_existing
echo unexpected-migration-complete
`, "y\n", root)
	require.Error(t, err, output)
	require.Contains(t, output, "Stage: backup")
	require.NotContains(t, output, "unexpected-migration-complete")
}

func TestInstallerRejectsAmbiguousFreshRoot(t *testing.T) {
	for _, name := range []string{"ordinary file", "symlink", "reserved subtree"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			switch name {
			case "ordinary file":
				require.NoError(t, os.WriteFile(filepath.Join(root, "note"), []byte("keep"), 0o600))
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "content")))
			case "reserved subtree":
				require.NoError(t, os.Mkdir(filepath.Join(root, ".yatm-upgrades"), 0o700))
			}
			output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; reject_ambiguous_fresh_root`, "", root)
			require.Error(t, err, output)
			require.Contains(t, output, "non-empty installation directory")
		})
	}
}

func TestInstallerFreshRetryAcceptsOnlyRetainedBackups(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".backup"), 0o700))

	// A cancelled fresh installation leaves its report, but no active installation content to classify.
	output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; reject_ambiguous_fresh_root`, "", root)
	require.NoError(t, err, output)
	require.NoError(t, os.WriteFile(filepath.Join(root, "note"), []byte("user data"), 0o600))
	_, err = installerShell(t, `INSTALL_DIRECTORY="$1"; reject_ambiguous_fresh_root`, "", root)
	require.Error(t, err)
}

func TestInstallerBindsServiceOwnershipBeforeInspection(t *testing.T) {
	root := t.TempDir()
	unit := filepath.Join(root, "yatm-httpd.service")
	require.NoError(t, os.WriteFile(unit, []byte("unit"), 0o600))
	for _, test := range []struct {
		name, current, load, fragment, work string
		wantSuccess                         bool
	}{
		{name: "fresh unused name", current: "none", load: "not-found", wantSuccess: true},
		{name: "fresh foreign unit", current: "none", load: "loaded", fragment: "/other/yatm.service", work: "/other"},
		{name: "owned installed unit", current: "v1.0.0-alpha.1", load: "loaded", fragment: unit, work: root, wantSuccess: true},
		{name: "installed foreign unit", current: "v1.0.0-alpha.1", load: "loaded", fragment: "/other/yatm.service", work: root},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"; CURRENT_VERSION="$2"; LOAD="$3"; FRAGMENT="$4"; WORK="$5"
service_property() { case "$1" in LoadState) printf '%s\n' "$LOAD";; FragmentPath) printf '%s\n' "$FRAGMENT";; WorkingDirectory) printf '%s\n' "$WORK";; esac; }
readlink() { printf '%s\n' "${2:-$1}"; }
verify_service_ownership
`, "", root, test.current, test.load, test.fragment, test.work)
			if test.wantSuccess {
				require.NoError(t, err, output)
			} else {
				require.Error(t, err, output)
			}
		})
	}
}

func TestInstallerFreshServiceConflictPrecedesInstallationWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "yatm")
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
identify_platform() { :; }
resolve_version() { CURRENT_VERSION=none; RELEASE_VERSION=v1.0.0-alpha.2; }
service_property() { case "$1" in LoadState) echo loaded;; FragmentPath) echo /other/yatm.service;; WorkingDirectory) echo /other;; esac; }
download_release() { echo unexpected-download; return 1; }
main
`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "not owned by this fresh installation")
	require.NotContains(t, output, "unexpected-download")
	require.NoDirExists(t, root)
}

func TestInstallerRestoresAdmissionWhenQuiesceOrStopIsUncertain(t *testing.T) {
	for _, failure := range []string{"quiesce", "stop"} {
		t.Run(failure, func(t *testing.T) {
			output, err := installerShell(t, `
SERVICE_ACTIVE=1; FAILURE="$1"; INSTALL_DIRECTORY=/install; SERVICE_NAME=yatm.service
service_main_pid() { echo 42; }
run_migrator() { [[ "$FAILURE" != quiesce ]]; }
systemctl() { if [[ "$1" == stop && "$FAILURE" == stop ]]; then return 1; fi; [[ "$1" != is-active || "$FAILURE" == stop ]]; }
restore_previous_service() { echo admission-restored; }
quiesce_and_stop
`, "", failure)
			require.Error(t, err, output)
			require.Contains(t, output, "admission-restored")
		})
	}
}

func TestInstallerWaitsForPreviousServiceRecovery(t *testing.T) {
	output, err := installerShell(t, `
SERVICE_ACTIVE=1; SERVICE_NAME=yatm.service; SCHEMA=current; attempts=0
systemctl() {
  [[ "$1" == restart ]] && return 0
  if [[ "$1" == is-active ]]; then
    attempts=$((attempts + 1))
    [[ "$attempts" -ge 3 ]]
    return
  fi
  return 1
}
ready_process_id() { echo 42; }
sleep() { :; }
restore_previous_service
echo "attempts:$attempts"
`, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "Previous service admission and active state restored")
	require.Contains(t, output, "attempts:3")
}

func TestInstallerBackupFailureStaysExplicitlyIncomplete(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("The supported installer verifies backups with Linux GNU tar")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "VERSION"), []byte("v0.1.8"), 0o600))
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"; CURRENT_VERSION=v0.1.8
begin_attempt
tar() { return 1; }
backup_installation
`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "incomplete archive remains")
	require.NotContains(t, output, "Passed: complete backup")
	archives, err := filepath.Glob(filepath.Join(root, ".backup/*/yatm.tar.gz"))
	require.NoError(t, err)
	require.Empty(t, archives)
}

func TestInstallerManagedIdentityIncludesCandidateCommitAndCompleteTrees(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the supported installer compares managed trees with GNU tar")
	}
	root := t.TempDir()
	installed, candidate := filepath.Join(root, "installed"), filepath.Join(root, "candidate")
	items := []string{"yatm-httpd", "yatm-cli", "yatm-export-library", "yatm-lto-info", "yatm-migrate", "install-release.sh", "frontend", "README.md", "CONTEXT.md", "docs", "VERSION", "COMMIT", "LICENSE", "licenses", "skills", "templates"}
	for _, base := range []string{installed, candidate} {
		for _, item := range items {
			path := filepath.Join(base, item)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(item), 0o600))
		}
		require.NoError(t, os.WriteFile(filepath.Join(base, "VERSION"), []byte("v1.0.0-alpha.2"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(base, "COMMIT"), []byte("candidate"), 0o600))
	}
	output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; RELEASE_DIRECTORY="$2"; RELEASE_VERSION=v1.0.0-alpha.2; managed_install_matches_candidate`, "", installed, candidate)
	require.NoError(t, err, output)
	require.NoError(t, os.WriteFile(filepath.Join(installed, "frontend"), []byte("stale"), 0o600))
	_, err = installerShell(t, `INSTALL_DIRECTORY="$1"; RELEASE_DIRECTORY="$2"; RELEASE_VERSION=v1.0.0-alpha.2; managed_install_matches_candidate`, "", installed, candidate)
	require.Error(t, err)

	// A legacy release-owned sample means an otherwise identical package still needs replacement.
	require.NoError(t, os.WriteFile(filepath.Join(installed, "config.example.yaml"), []byte("obsolete"), 0o600))
	_, err = installerShell(t, `INSTALL_DIRECTORY="$1"; RELEASE_DIRECTORY="$2"; RELEASE_VERSION=v1.0.0-alpha.2; managed_install_matches_candidate`, "", installed, candidate)
	require.Error(t, err)
}

func TestInstallerCompleteArchiveBackup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("complete archive metadata comparison requires GNU tar; macOS coverage requires gtar")
	}

	// Preserve ordinary, hidden, linked, and legacy-retained installation input.
	root := t.TempDir()
	for name, data := range map[string]string{
		"VERSION":                    "v1.0.0-alpha.1\n",
		".hidden":                    "hidden\n",
		"directory with spaces/file": "spaces\n",
		"content":                    "content\n",
		".yatm-upgrades/legacy":      "retained\n",
	} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o640))
	}
	require.NoError(t, os.Link(filepath.Join(root, "content"), filepath.Join(root, "hard-link")))
	require.NoError(t, os.Symlink("content", filepath.Join(root, "soft-link")))

	// A second archive must not include the first retained archive or scratch directory.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
CURRENT_VERSION=v1.0.0-alpha.1
begin_attempt
backup_installation
first="$BACKUP_DIRECTORY/yatm.tar.gz"
printf 'changed\n' > "$INSTALL_DIRECTORY/VERSION"
ATTEMPT_DIRECTORY="$(mktemp -d "$INSTALL_DIRECTORY/.backup/second.XXXXXX")"
REPORT_DIRECTORY="$ATTEMPT_DIRECTORY"
WORK_DIRECTORY="$ATTEMPT_DIRECTORY/.work"
mkdir "$WORK_DIRECTORY"
backup_installation
tar -tzf "$first" | grep -Fx './.yatm-upgrades/legacy'
! tar -tzf "$first" | grep -q '^./.backup/'
tar -tzf "$BACKUP_DIRECTORY/yatm.tar.gz" | grep -Fx './directory with spaces/file'
[[ "$(tar -xOzf "$first" ./VERSION)" == v1.0.0-alpha.1 ]]
[[ "$(tar -xOzf "$BACKUP_DIRECTORY/yatm.tar.gz" ./VERSION)" == changed ]]
(cd "$BACKUP_DIRECTORY" && sha256sum --check yatm.tar.gz.sha256)
`, "", root)
	require.NoError(t, err, output)
	require.Equal(t, 2, strings.Count(output, "Passed: complete backup content and metadata comparison."))
}

func TestInstallerConfigurationPlanOrdering(t *testing.T) {
	root := t.TempDir()

	// The reviewed plan is created before any stop, rechecked after stopping, then applied only after backup.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
TMP_DIRECTORY="$1/work"
REPORT_DIRECTORY="$1/report"
mkdir -p "$TMP_DIRECTORY" "$REPORT_DIRECTORY"
LEGACY=0
SERVICE_ACTIVE=0
SCHEMA=current
CONFIG_CHANGED=true
CONFIG_PLAN="$TMP_DIRECTORY/config-plan.json"
run_migrator() {
  echo "migrator:$*"
  case "$*" in
    *config-check*) [[ "$*" == *'-plan-file '* && "$*" == *-service-stopped* ]];;
    *config-apply*) [[ "$*" == *'-plan-file '* && "$*" == *-service-stopped* && "$*" == *--confirm* ]];;
  esac
}
backup_installation() { echo backup; BACKUP_COMPLETE=1; }
upgrade_existing
`, "", root)
	require.NoError(t, err, output)
	check := strings.Index(output, "migrator:-phase config-check")
	backup := strings.Index(output, "backup")
	apply := strings.Index(output, "migrator:-phase config-apply")
	require.GreaterOrEqual(t, check, 0, output)
	require.Greater(t, backup, check, output)
	require.Greater(t, apply, backup, output)
}

func TestInstallerConfigurationCheckFailureStopsBeforeBackup(t *testing.T) {
	root := t.TempDir()

	// Changed reviewed inputs require a fresh plan and must not create an archive or mutate the installation.
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
TMP_DIRECTORY="$1/work"
mkdir "$TMP_DIRECTORY"
SERVICE_ACTIVE=0
SCHEMA=current
CONFIG_PLAN="$TMP_DIRECTORY/config-plan.json"
run_migrator() { [[ "$*" != *config-check* ]]; }
backup_installation() { echo unexpected-backup; }
upgrade_existing
`, "", root)
	require.Error(t, err, output)
	require.NotContains(t, output, "unexpected-backup")
}

func TestInstallerBlocksLegacyPendingMigration(t *testing.T) {
	root := t.TempDir()
	pending := filepath.Join(root, ".yatm-upgrades")
	require.NoError(t, os.MkdirAll(pending, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pending, "migration.pending.json"), []byte("{}\n"), 0o600))

	// The new installer does not resume an old phase marker, because it lacks a verified archive contract.
	output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; reject_pending_migration`, "", root)
	require.Error(t, err, output)
	require.Contains(t, output, "earlier upgrade is unfinished")
}

func TestInstallerCleansOnlyRecognizedLegacyArtifactsAfterBackup(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, ".yatm-upgrades")
	known := filepath.Join(old, "20260916010101.abc123")
	unknown := filepath.Join(old, "keep-me")
	require.NoError(t, os.MkdirAll(filepath.Join(known, "reports"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(known, "v1.0.0-alpha.1.backup"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(old, "OWNER"), []byte("yatm-installer-upgrades\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(known, "reports", "upgrade.log"), []byte("completed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(known, "reports", "backup.status"), []byte("complete\n"), 0o600))
	require.NoError(t, os.MkdirAll(unknown, 0o700))

	// Old installer output is removed only after a complete new archive, and unknown input survives.
	output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; BACKUP_COMPLETE=1; clean_old_upgrades`, "", root)
	require.NoError(t, err, output)
	require.NoDirExists(t, known)
	require.DirExists(t, unknown)
}

func TestInstallerCleansRecognizedIncompleteAttemptsOnlyAfterNewBackup(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, ".yatm-upgrades")
	incomplete := filepath.Join(old, "20260916010101.abc123")
	unknown := filepath.Join(old, "20260916010102.def456")
	pending := filepath.Join(old, "migration.pending.json")
	require.NoError(t, os.MkdirAll(filepath.Join(incomplete, "reports"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(incomplete, "reports", "upgrade.log"), []byte("cancelled before backup\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(unknown, "reports"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(unknown, "reports", "unknown-report.json"), []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(pending), []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(old, "OWNER"), []byte("yatm-installer-upgrades\n"), 0o600))

	// Cancellation evidence is retained until a later verified backup protects it.
	output, err := installerShell(t, `INSTALL_DIRECTORY="$1"; BACKUP_COMPLETE=0; clean_old_upgrades`, "", root)
	require.NoError(t, err, output)
	require.DirExists(t, incomplete)

	// A recognized incomplete attempt is now disposable; unknown reports and pending recovery evidence are not.
	output, err = installerShell(t, `INSTALL_DIRECTORY="$1"; BACKUP_COMPLETE=1; clean_old_upgrades`, "", root)
	require.NoError(t, err, output)
	require.NoDirExists(t, incomplete)
	require.DirExists(t, unknown)
	require.FileExists(t, pending)
}
