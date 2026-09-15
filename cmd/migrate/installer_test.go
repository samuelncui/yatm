package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func installerShell(t *testing.T, script, input string, args ...string) (string, error) {
	t.Helper()
	installer, err := filepath.Abs("../../install-release.sh")
	require.NoError(t, err)
	command := exec.Command("bash", append([]string{"-c", "source \"$1\"\nshift\n" + script, "installer-test", installer}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestInstallerVersionOrdering(t *testing.T) {
	output, err := installerShell(t, `
[[ "$(version_key v1.0.0-alpha.1)" < "$(version_key v2.0.0-alpha.2)" ]]
[[ "$(version_key v1.0.0-alpha.10)" < "$(version_key v2.0.0-beta.1)" ]]
[[ "$(version_key v2.0.0-rc.9)" < "$(version_key v2.0.0)" ]]
[[ "$(version_key v0.1.21)" < "$(version_key v1.0.0-alpha.1)" ]]
! version_key '../unsafe'
parse_options --version 2.0.0-alpha.1
[[ "$RELEASE_VERSION" == v1.0.0-alpha.1 ]]
parse_options --version v1.0.0-alpha.1
[[ "$RELEASE_VERSION" == v1.0.0-alpha.1 ]]
`, "")
	require.NoError(t, err, output)
}

func TestInstallerFailureBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, failure, input string
		wantSuccess          bool
		want, absent         []string
	}{
		{name: "success", input: "y\n", wantSuccess: true, want: []string{"phase:quiesce", "service:stop", "backup", "phase:prepare", "phase:commit", "replacement-allowed"}, absent: []string{"phase:abort", "service:start"}},
		{name: "prepare failure", failure: "prepare", want: []string{"phase:abort", "service:start", "Stage: reversed"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report declined", input: "n\n", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "report EOF", wantSuccess: true, want: []string{"phase:abort", "service:start"}, absent: []string{"phase:commit", "replacement-allowed"}},
		{name: "commit failure", failure: "commit", input: "y\n", want: []string{"phase:commit", "Stage: commit", "service:stop"}, absent: []string{"phase:abort", "service:start", "replacement-allowed"}},
		{name: "abort failure", failure: "abort", input: "n\n", want: []string{"Abort failed", "phase:abort"}, absent: []string{"service:start", "replacement-allowed"}},
		{name: "busy", failure: "quiesce", want: []string{"phase:quiesce"}, absent: []string{"service:stop", "backup", "phase:prepare"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := installerShell(t, `
SCHEMA=v1
SERVICE_ACTIVE=1
FAIL_PHASE="$1"
run_migrator() { echo "phase:$2"; [[ "$2" != "$FAIL_PHASE" ]]; }
systemctl() { echo "service:$1"; }
backup_installation() { STAGE=backup; echo backup; }
trap 'on_failure "$?"' ERR
upgrade_existing
echo replacement-allowed
`, test.input, test.failure)
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
	root := t.TempDir()
	installed := filepath.Join(root, "installed")
	candidate := filepath.Join(root, "candidate")
	for _, file := range []string{"config.yaml", "scripts/mount", "yatm-httpd.service", "local-note", "yatm-httpd"} {
		path := filepath.Join(installed, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("existing "+file), 0600))
	}
	for _, file := range []string{"templates/config.example.yaml", "templates/scripts/mount", "templates/yatm-httpd.service", "yatm-httpd", "VERSION"} {
		path := filepath.Join(candidate, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("candidate "+file), 0600))
	}
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1"
RELEASE_DIRECTORY="$2"
CURRENT_VERSION=v0.1.21
install_managed_files
`, "", installed, candidate)
	require.NoError(t, err, output)
	for _, file := range []string{"config.yaml", "scripts/mount", "yatm-httpd.service", "local-note"} {
		data, err := os.ReadFile(filepath.Join(installed, file))
		require.NoError(t, err)
		require.Equal(t, "existing "+file, string(data))
	}
	data, err := os.ReadFile(filepath.Join(installed, "yatm-httpd"))
	require.NoError(t, err)
	require.Equal(t, "candidate yatm-httpd", string(data))
}

func TestInstallerCancellationDoesNotInterrupt(t *testing.T) {
	for _, input := range []string{"n\n", ""} {
		output, err := installerShell(t, `
INSTALL_DIRECTORY="$1/yatm"
identify_platform() { :; }
resolve_version() { CURRENT_VERSION=v0.1.21; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { :; }
inspect_installation() { :; }
systemctl() { echo unexpected-service-operation; return 1; }
run_migrator() { echo unexpected-migration-operation; return 1; }
main
`, input, t.TempDir())
		require.NoError(t, err, output)
		require.Contains(t, output, "Installation cancelled")
		require.NotContains(t, output, "unexpected-")
	}
}

func TestInstallerReadOnlyCheckDoesNotInterrupt(t *testing.T) {
	output, err := installerShell(t, `
INSTALL_DIRECTORY="$1/yatm"
identify_platform() { :; }
resolve_version() { CURRENT_VERSION=v0.1.21; RELEASE_VERSION=v1.0.0-alpha.1; }
download_release() { :; }
inspect_installation() { echo read-only-inspection; }
systemctl() { echo unexpected-service-operation; return 1; }
main --check
`, "", t.TempDir())
	require.NoError(t, err, output)
	require.Contains(t, output, "Read-only installation checks passed")
	require.NotContains(t, output, "unexpected-")
}

func TestInstallerPostReplacementFailureKeepsServiceStopped(t *testing.T) {
	for _, stage := range []string{"commit", "replace-programs", "readiness"} {
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
