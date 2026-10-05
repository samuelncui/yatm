package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestManifestBlobRoundTripAndValidation(t *testing.T) {
	// Round-trip the storage-only Archive blob through database interfaces.
	archive := &ArchiveManifestFile{SourcePath: "/source/file"}
	value, err := archive.Value()
	require.NoError(t, err)
	decodedArchive := new(ArchiveManifestFile)
	require.NoError(t, decodedArchive.Scan(value))
	require.True(t, proto.Equal(archive, decodedArchive))

	// Round-trip the two kind-specific specifications without a common oneof wrapper.
	archiveSpec := &ArchiveJobSpec{Selections: []*FileSelection{{Target: &FileSelection_Library{Library: &LibrarySelection{FileId: 7}}, Scope: FileScope_FILE_SCOPE_ALL}}}
	value, err = archiveSpec.Value()
	require.NoError(t, err)
	decodedArchiveSpec := new(ArchiveJobSpec)
	require.NoError(t, decodedArchiveSpec.Scan(value))
	require.True(t, proto.Equal(archiveSpec, decodedArchiveSpec))
	restoreSpec := &RestoreJobSpec{FileVersionIds: []int64{1, 2}}
	value, err = restoreSpec.Value()
	require.NoError(t, err)
	decodedRestoreSpec := new(RestoreJobSpec)
	require.NoError(t, decodedRestoreSpec.Scan(value))
	require.True(t, proto.Equal(restoreSpec, decodedRestoreSpec))
	previewSpec := &ScanJobSpec{
		Selections:      []*FileSelection{{Target: &FileSelection_Location{Location: &LocationSelection{LocationId: 1, Path: "folder"}}, Scope: FileScope_FILE_SCOPE_ALL}},
		PreviewPolicy:   PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL,
		SignaturePolicy: ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
	}
	value, err = previewSpec.Value()
	require.NoError(t, err)
	decodedPreviewSpec := new(ScanJobSpec)
	require.NoError(t, decodedPreviewSpec.Scan(value))
	require.True(t, proto.Equal(previewSpec, decodedPreviewSpec))

	// Round-trip the attempt-local Archive result blob.
	copyResult := &ArchiveCopyResult{SizeBytes: 7, Mode: 0o644, ModTimeNs: 1, WriteTimeNs: 2, Sha256: make([]byte, 32)}
	value, err = copyResult.Value()
	require.NoError(t, err)
	decodedResult := new(ArchiveCopyResult)
	require.NoError(t, decodedResult.Scan(value))
	require.True(t, proto.Equal(copyResult, decodedResult))
	var absentResult *ArchiveCopyResult
	value, err = absentResult.Value()
	require.NoError(t, err)
	require.Nil(t, value)

	// Reject paths that escape the selected root or cannot represent a literal UTF-8 identity.
	for _, invalid := range []string{"", ".", "..", "../file", "/file", "folder/../file", "file\xffname", "file\x00name"} {
		require.Error(t, ValidateRelativePath(invalid), invalid)
	}
}
