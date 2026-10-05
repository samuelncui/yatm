package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const previewInstallerVersion = "v1.2.3"

type previewPackage struct {
	archive  string
	checksum string
}

func TestPreviewInstallerOptions(t *testing.T) {
	// Selection flags carry a shared archive/checksum pair without changing default optional behavior.
	output, err := installerShell(t, `
parse_options --with-preview --preview-archive archive.tar.gz --preview-checksum archive.sha256
[[ "$PREVIEW_CHOICE" == yes && "$PREVIEW_ARCHIVE" == archive.tar.gz && "$PREVIEW_CHECKSUM" == archive.sha256 ]]
parse_options --without-preview
[[ "$PREVIEW_CHOICE" == no ]]
`, "")
	require.NoError(t, err, output)
}

func TestPreparePreviewVerifiesAndStagesSelectedPackage(t *testing.T) {
	// A checked native helper expands the release allowlist only after all package boundaries pass.
	pkg := writePreviewPackage(t, previewPackageOptions{})
	output, err := preparePreview(t, pkg, "yes", false, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "Optional Preview helper verified")
	require.Contains(t, output, "managed:19 install:1")
}

func TestPreparePreviewRejectsUnsafePackages(t *testing.T) {
	for _, test := range []struct {
		name string
		pkg  func(*testing.T) previewPackage
		want string
	}{
		{name: "checksum", pkg: func(t *testing.T) previewPackage {
			pkg := writePreviewPackage(t, previewPackageOptions{})
			require.NoError(t, os.WriteFile(pkg.checksum, []byte("0000000000000000000000000000000000000000000000000000000000000000  preview.tar.gz\n"), 0o600))
			return pkg
		}, want: "Preview checksum mismatch"},
		{name: "foreign member", pkg: func(t *testing.T) previewPackage {
			return writePreviewPackage(t, previewPackageOptions{ForeignMember: true})
		}, want: "Unexpected Preview archive member"},
		{name: "symbolic link", pkg: func(t *testing.T) previewPackage {
			return writePreviewPackage(t, previewPackageOptions{SymbolicLink: true})
		}, want: "Preview archives may only contain ordinary files and directories"},
		{name: "parent member", pkg: func(t *testing.T) previewPackage {
			return writeUnsafePreviewPackage(t)
		}, want: "Unsafe Preview archive member"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Reject archive structure before extraction, helper execution, or managed replacement admission.
			output, err := preparePreview(t, test.pkg(t), "yes", false, "")
			require.Error(t, err, output)
			require.Contains(t, output, test.want)
			require.NotContains(t, output, "Optional Preview helper verified")
		})
	}
}

func TestPreparePreviewRejectsIncompatibleHelper(t *testing.T) {
	for _, test := range []struct {
		name    string
		options previewPackageOptions
		want    string
	}{
		{name: "version", options: previewPackageOptions{Version: "v9.9.9"}, want: "Preview package version mismatch"},
		{name: "commit", options: previewPackageOptions{Commit: "foreign"}, want: "Preview package commit mismatch"},
		{name: "capability version", options: previewPackageOptions{CapabilityVersion: "v9.9.9"}, want: "incompatible protocol/version"},
		{name: "protocol", options: previewPackageOptions{Protocol: 2}, want: "incompatible protocol/version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Metadata and helper protocol must agree with the verified main release before staging.
			output, err := preparePreview(t, writePreviewPackage(t, test.options), "yes", false, "")
			require.Error(t, err, output)
			if test.want != "" {
				require.Contains(t, output, test.want)
			}
		})
	}
}

func TestPreparePreviewLeavesOptionalHelperUnselectedWithoutWrites(t *testing.T) {
	// Non-interactive default selection must skip the optional package and leave staging untouched.
	output, err := preparePreview(t, previewPackage{}, "ask", false, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "Optional Preview helper not selected")
	require.Contains(t, output, "managed:16 install:0")
	require.NotContains(t, output, "Optional Preview helper verified")
}

func TestPreparePreviewCheckOnlyStagesWithoutInstalling(t *testing.T) {
	// A selected read-only check verifies candidate artifacts but never writes the installation directory.
	pkg := writePreviewPackage(t, previewPackageOptions{})
	output, err := preparePreview(t, pkg, "yes", true, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "Optional Preview helper verified")
	require.Contains(t, output, "managed:19 install:1")
	require.NotContains(t, output, "installed-preview")
}

func TestPreparePreviewPreservesUnknownInstalledResources(t *testing.T) {
	// Existing unowned helper paths stop before replacement and retain their original contents.
	pkg := writePreviewPackage(t, previewPackageOptions{})
	root := t.TempDir()
	output, err := preparePreviewAt(t, root, pkg, "yes", false, `
printf unknown > "$INSTALL_DIRECTORY/yatm-preview"
`)
	require.Error(t, err, output)
	require.Contains(t, output, "Unrecognized local Preview resource: yatm-preview")
	contents, readErr := os.ReadFile(filepath.Join(root, "installed", "yatm-preview"))
	require.NoError(t, readErr)
	require.Equal(t, "unknown", string(contents))
}

type previewPackageOptions struct {
	Version           string
	Commit            string
	CapabilityVersion string
	Protocol          int
	ForeignMember     bool
	SymbolicLink      bool
}

func writePreviewPackage(t *testing.T, options previewPackageOptions) previewPackage {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "preview-lib"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "preview-support"), 0o700))
	version, commit, protocol := options.Version, options.Commit, options.Protocol
	if version == "" {
		version = previewInstallerVersion
	}
	if commit == "" {
		commit = "candidate"
	}
	if protocol == 0 {
		protocol = 1
	}
	capabilityVersion := `"$PREVIEW_TEST_VERSION"`
	if options.CapabilityVersion != "" {
		capabilityVersion = fmt.Sprintf("%q", options.CapabilityVersion)
	}
	// The helper prints one JSON capabilities line. The format string is single-quoted, so the JSON
	// quotes need no escaping: bash strips a backslash it would otherwise pass through and dash keeps
	// it, which would make the helper emit invalid JSON under one shell only.
	helper := fmt.Sprintf("#!/bin/sh\nprintf '{\"protocol\":%d,\"type\":\"capabilities\",\"capabilities\":{\"version\":\"%%s\"}}\\n' %s\n", protocol, capabilityVersion)
	require.NoError(t, os.WriteFile(filepath.Join(source, "yatm-preview"), []byte(helper), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "preview-lib", "codec"), []byte("codec"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "preview-support", "VERSION"), []byte(version), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "preview-support", "COMMIT"), []byte(commit), 0o600))
	items := []string{"yatm-preview", "preview-lib", "preview-support"}
	if options.ForeignMember {
		require.NoError(t, os.WriteFile(filepath.Join(source, "foreign"), []byte("foreign"), 0o600))
		items = append(items, "foreign")
	}
	if options.SymbolicLink {
		require.NoError(t, os.Symlink("codec", filepath.Join(source, "preview-lib", "link")))
	}
	archive := filepath.Join(root, "preview.tar.gz")
	command := exec.Command("tar", append([]string{"-czf", archive, "-C", source}, items...)...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return previewPackage{archive: archive, checksum: writePreviewChecksum(t, archive)}
}

func writeUnsafePreviewPackage(t *testing.T) previewPackage {
	t.Helper()
	root := t.TempDir()
	archive := filepath.Join(root, "preview.tar.gz")
	file, err := os.Create(archive)
	require.NoError(t, err)
	writer := gzip.NewWriter(file)
	entries := tar.NewWriter(writer)
	require.NoError(t, entries.WriteHeader(&tar.Header{Name: "../preview-support/VERSION", Mode: 0o600, Size: 6}))
	_, err = entries.Write([]byte("v1.2.3"))
	require.NoError(t, err)
	require.NoError(t, entries.Close())
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
	return previewPackage{archive: archive, checksum: writePreviewChecksum(t, archive)}
}

func writePreviewChecksum(t *testing.T, archive string) string {
	t.Helper()
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	checksum := filepath.Join(filepath.Dir(archive), "preview.sha256")
	require.NoError(t, os.WriteFile(checksum, []byte(fmt.Sprintf("%x  %s\n", sum, filepath.Base(archive))), 0o600))
	return checksum
}

func preparePreview(t *testing.T, pkg previewPackage, choice string, checkOnly bool, setup string) (string, error) {
	t.Helper()
	return preparePreviewAt(t, t.TempDir(), pkg, choice, checkOnly, setup)
}

func preparePreviewAt(t *testing.T, root string, pkg previewPackage, choice string, checkOnly bool, setup string) (string, error) {
	t.Helper()
	check := "0"
	if checkOnly {
		check = "1"
	}
	return installerShell(t, `
TMP_DIRECTORY="$1/tmp"
RELEASE_DIRECTORY="$1/release"
INSTALL_DIRECTORY="$1/installed"
PREVIEW_ARCHIVE="$2"
PREVIEW_CHECKSUM="$3"
PREVIEW_CHOICE="$4"
CHECK_ONLY="$5"
RELEASE_VERSION="v1.2.3"
LEGACY=0
export PREVIEW_TEST_VERSION="$RELEASE_VERSION"
mkdir -p "$TMP_DIRECTORY" "$RELEASE_DIRECTORY" "$INSTALL_DIRECTORY"
printf candidate > "$RELEASE_DIRECTORY/COMMIT"
jq() {
  input="$(cat)"
  [[ "$input" == *'"protocol":1'* && "$input" == *'"type":"capabilities"'* && "$input" == *'"version":"'"$RELEASE_VERSION"'"'* ]]
}
`+setup+`
prepare_preview
printf 'managed:%s install:%s\n' "${#MANAGED_ITEMS[@]}" "$INSTALL_PREVIEW"
if [[ -f "$INSTALL_DIRECTORY/yatm-preview" ]]; then printf 'installed-preview:%s\n' "$(< "$INSTALL_DIRECTORY/yatm-preview")"; fi
`, "", root, pkg.archive, pkg.checksum, choice, check)
}
