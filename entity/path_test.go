package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelativePathPreservesLiteralUTF8(t *testing.T) {
	for _, name := range []string{
		`back\slash`, `literal\n`, "line\nbreak", " leading", "trailing ", " \t\n",
		`quotes'"`, "100%?#", "照片 😀", "\u00a0", "\uFFFD", "\x01\x7f",
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidatePathComponent(name))
			require.NoError(t, ValidateRelativePath(name))
			require.NoError(t, ValidateRelativePath("parent/"+name+"/child"))
		})
	}
}

func TestPathComponentRejectsInvalidIdentity(t *testing.T) {
	for _, value := range []string{"", ".", "..", "a/b", "nul\x00byte", "invalid\xff", "\xed\xa0\x80"} {
		t.Run(value, func(t *testing.T) {
			require.Error(t, ValidatePathComponent(value))
		})
	}
}

func TestArchiveSourcePathRequiresLiteralUTF8(t *testing.T) {
	// Archive's persistence boundary keeps legal source names intact through its blob codec.
	for _, name := range []string{`back\slash`, " \t\n", "照片"} {
		original := &ArchiveManifestFile{SourcePath: "/source/" + name}
		value, err := original.Value()
		require.NoError(t, err)
		var decoded ArchiveManifestFile
		require.NoError(t, decoded.Scan(value))
		require.Equal(t, original.SourcePath, decoded.SourcePath)
	}

	// Native source paths must reject invalid bytes and traversal before reaching a stored blob.
	for _, name := range []string{"/source/\xff", "/source/nul\x00", "/source/../outside"} {
		require.Error(t, (&ArchiveManifestFile{SourcePath: name}).Validate())
	}
}

func TestRelativePathRejectsInvalidIdentity(t *testing.T) {
	for _, value := range []string{
		"", ".", "..", "/absolute", "../escape", "a/../b", "a/./b", "a//b", "a/",
		"nul\x00byte", "invalid\xff", "a/invalid\xfe", "a/\xed\xa0\x80",
	} {
		t.Run(value, func(t *testing.T) {
			require.Error(t, ValidateRelativePath(value))
		})
	}
}
