package entity

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarMetadataPhysicalAnchors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header *TapeBytePosition
		end    *TapeBytePosition
		valid  bool
	}{
		{"sequential", nil, nil, true},
		{"shared record", &TapeBytePosition{LogicalObject: 10, OffsetBytes: 512}, &TapeBytePosition{LogicalObject: 10, OffsetBytes: 1536}, true},
		{"next record", &TapeBytePosition{LogicalObject: 10, OffsetBytes: 512}, &TapeBytePosition{LogicalObject: 11}, true},
		{"underflow", &TapeBytePosition{LogicalObject: 10, OffsetBytes: 1536}, &TapeBytePosition{LogicalObject: 11}, false},
		{"partition", &TapeBytePosition{Partition: 1, LogicalObject: 10}, &TapeBytePosition{Partition: 1, LogicalObject: 11}, false},
		{"narrowed partition", &TapeBytePosition{Partition: math.MaxUint32, LogicalObject: 10}, &TapeBytePosition{Partition: math.MaxUint32, LogicalObject: 11}, false},
		{"unpaired", &TapeBytePosition{LogicalObject: 10}, nil, false},
		{"record limit", &TapeBytePosition{LogicalObject: 10, OffsetBytes: 4096}, &TapeBytePosition{LogicalObject: 11}, false},
		{"reversed", &TapeBytePosition{LogicalObject: 11}, &TapeBytePosition{LogicalObject: 10}, false},
		{"empty range", &TapeBytePosition{LogicalObject: 10}, &TapeBytePosition{LogicalObject: 10}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := &TarMetadata{TapeFile: 1, HeaderOffsetBytes: 1024, Header: tc.header, End: tc.end}
			if tc.valid {
				require.NoError(t, ValidateTarMetadata(value, 4096))
			} else {
				require.Error(t, ValidateTarMetadata(value, 4096))
			}
		})
	}
}

func TestTarPublicProfileAndEncryptionMaterialHaveSeparateValidation(t *testing.T) {
	// A recovery label carries public requirements without the separately retained key.
	public := &TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: TapeRecordMode_TAPE_RECORD_MODE_VARIABLE,
		RecordBytes: 262144, EncryptionAlgorithm: proto.String("AES256")}
	require.NoError(t, ValidateTarProfile(public))
	stored := &TapeMediaProfile{Format: "tar_pax_v1", Tar: public}
	require.Error(t, ValidateTarMediaProfile(stored))
	stored.Encryption = "encoded-key"
	require.NoError(t, ValidateTarMediaProfile(stored))

	// Script-prepared encryption remains valid when no public algorithm was observed.
	public.EncryptionAlgorithm = nil
	require.NoError(t, ValidateTarMediaProfile(stored))
	stored.Encryption = ""
	require.NoError(t, ValidateTarMediaProfile(stored))

	// A present empty algorithm is invalid even when no key was supplied.
	public.EncryptionAlgorithm = proto.String("")
	require.Error(t, ValidateTarProfile(public))
}
