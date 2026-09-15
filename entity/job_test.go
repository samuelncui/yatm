package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestReleasedLegacyWireNumbers(t *testing.T) {
	// Preserve every stable legacy status and catalog field number.
	require.Equal(t, int32(1), int32(CopyStatus_PENDING))
	require.Equal(t, int32(3), int32(CopyStatus_STAGED))
	require.Equal(t, int32(4), int32(CopyStatus_SUBMITTED))
	require.Equal(t, int32(2), int32(JobStatus_PENDING))
	require.Equal(t, int32(4), int32(JobStatus_COMPLETED))
	jobFields := (&Job{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, "status", string(jobFields.ByNumber(2).Name()))
	require.Equal(t, "priority", string(jobFields.ByNumber(3).Name()))
	restoreFields := (&RestoreFile{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, "file_id", string(restoreFields.ByNumber(1).Name()))
	require.Equal(t, "hash", string(restoreFields.ByNumber(19).Name()))
	require.Equal(t, "target_path", string(restoreFields.ByNumber(34).Name()))
}

func TestAlphaJobWireBaseline(t *testing.T) {
	// Keep the Job kind zero value distinct from both executable kinds.
	require.Equal(t, int32(0), int32(JobKind_JOB_KIND_UNSPECIFIED))
	require.Equal(t, int32(1), int32(JobKind_ARCHIVE))
	require.Equal(t, int32(2), int32(JobKind_RESTORE))
	require.Equal(t, int32(4), int32(JobKind_SCAN))
	require.Equal(t, int32(5), int32(JobStatus_INDEXING))
	jobFields := (&Job{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, "kind", string(jobFields.ByNumber(7).Name()))

	// Alpha introduces a typed Archive file independently of the frozen legacy Job payload.
	archiveFields := (&ArchiveFile{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, "source_path", string(archiveFields.ByNumber(1).Name()))
	require.Equal(t, "media_path", string(archiveFields.ByNumber(2).Name()))
}

func TestPublishedWireReservations(t *testing.T) {
	// Retired legacy fields cannot be reused for unrelated Alpha data.
	for _, test := range []struct {
		message proto.Message
		numbers []protoreflect.FieldNumber
		names   []protoreflect.Name
	}{
		{&Job{}, []protoreflect.FieldNumber{17}, []protoreflect.Name{"state"}},
		{&JobFilter{}, []protoreflect.FieldNumber{1}, nil},
		{&Position{}, []protoreflect.FieldNumber{2}, []protoreflect.Name{"file_id"}},
		{&FileGetReply{}, []protoreflect.FieldNumber{2}, []protoreflect.Name{"positions"}},
		{&RestoreFile{}, []protoreflect.FieldNumber{2, 3, 17, 18, 33},
			[]protoreflect.Name{"tape_id", "position_id", "status", "size", "tape_path"}},
	} {
		descriptor := test.message.ProtoReflect().Descriptor()
		for _, number := range test.numbers {
			require.True(t, descriptor.ReservedRanges().Has(number), "%s: %d", descriptor.FullName(), number)
		}
		for _, name := range test.names {
			require.True(t, descriptor.ReservedNames().Has(name), "%s: %s", descriptor.FullName(), name)
		}
	}

	// Removed Draft-only representations are absent from regenerated descriptors as well as Go callers.
	for _, name := range []protoreflect.FullName{"online.OnlineSource", "yatm.job_storage.PreviewJobSpec"} {
		_, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		require.ErrorIs(t, err, protoregistry.NotFound)
	}
	require.Nil(t, (&OnlineExclusions{}).ProtoReflect().Descriptor().Fields().ByName("paths"))
}

func TestManifestBlobRoundTripAndValidation(t *testing.T) {
	// Round-trip the storage-only Archive blob through database interfaces.
	archive := &ArchiveManifestFile{SourcePath: "/source/file"}
	value, err := archive.Value()
	require.NoError(t, err)
	decodedArchive := new(ArchiveManifestFile)
	require.NoError(t, decodedArchive.Scan(value))
	require.True(t, proto.Equal(archive, decodedArchive))

	// Round-trip the two kind-specific specifications without a common oneof wrapper.
	archiveSpec := &ArchiveJobSpec{Sources: []*Source{{Base: "/source", Path: []string{"folder"}}}}
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
		LocationId: 1, Paths: []string{"folder"}, PreviewPolicy: PreviewPolicy_PREVIEW_REGENERATE_ALL,
		SignaturePolicy: ScanSignaturePolicy_FORCE_READ,
	}
	value, err = previewSpec.Value()
	require.NoError(t, err)
	decodedPreviewSpec := new(ScanJobSpec)
	require.NoError(t, decodedPreviewSpec.Scan(value))
	require.True(t, proto.Equal(previewSpec, decodedPreviewSpec))

	// Round-trip the attempt-local Archive result blob.
	copyResult := &ArchiveCopyResult{Size: 7, Mode: 0o644, ModTimeNs: 1, WriteTimeNs: 2, Sha256: make([]byte, 32)}
	value, err = copyResult.Value()
	require.NoError(t, err)
	decodedResult := new(ArchiveCopyResult)
	require.NoError(t, decodedResult.Scan(value))
	require.True(t, proto.Equal(copyResult, decodedResult))
	var absentResult *ArchiveCopyResult
	value, err = absentResult.Value()
	require.NoError(t, err)
	require.Nil(t, value)

	// Reject paths that could escape or vary across platforms.
	for _, invalid := range []string{"", ".", "..", "../file", "/file", "folder/../file", `folder\file`, "file\x00name"} {
		require.Error(t, ValidateRelativePath(invalid), invalid)
	}
}
