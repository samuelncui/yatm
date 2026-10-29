package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type encryptionFixture struct {
	*deviceFixture
	state               encryptionState
	index               byte
	sets, statusQueries int
	capabilities        []byte
	unavailable         bool
}

func newEncryptionFixture() *encryptionFixture {
	return &encryptionFixture{deviceFixture: newDeviceFixture(), state: encryptionState{control: 1}, index: 3}
}

func encryptionPage(page uint16, length int) []byte {
	data := make([]byte, length)
	binary.BigEndian.PutUint16(data[:2], page)
	binary.BigEndian.PutUint16(data[2:4], uint16(length-4))
	return data
}

func encryptionTestProfile() *entity.TarMediaProfile {
	return &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}
}

func (f *encryptionFixture) execute(command Command) (Response, error) {
	// The fixture exposes script-installed state. Native security-out commands must never occur.
	if command.CDB[0] == 0xb5 {
		f.sets++
		return Response{}, errors.New("native key mutation is forbidden")
	}
	if command.CDB[0] != 0xa2 || command.CDB[1] != 0x20 {
		return f.deviceFixture.execute(command)
	}
	var data []byte
	switch binary.BigEndian.Uint16(command.CDB[2:4]) {
	case 0x10:
		if f.unavailable {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
		}
		data = encryptionPage(0x10, 44)
		data[20], data[23], data[24] = f.index, 20, 0xba
		binary.BigEndian.PutUint16(data[30:32], 32)
		binary.BigEndian.PutUint32(data[40:44], 0x00010014)
		if f.capabilities != nil {
			data = f.capabilities
		}
	case 0x20:
		f.statusQueries++
		data = encryptionPage(0x20, 24)
		data[4], data[5], data[6], data[7], data[12] = f.state.nexus<<5|f.state.scope, f.state.encrypt, f.state.decrypt, f.state.algorithm, f.state.control<<4
		binary.BigEndian.PutUint32(data[8:12], f.state.counter)
	default:
		return Response{}, errors.New("native key-management capability query is forbidden")
	}
	copy(command.Data, data)
	return Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
}

func fixtureEncryptionDevice(t *testing.T, f *encryptionFixture) *Device {
	t.Helper()
	d, err := NewDevice(t.Context(), Identity{}, newSerializedTransport(f.execute, func() error { f.closed++; return nil }))
	require.NoError(t, err)
	return d
}

func TestEncryptionDiagnosticsDoNotOwnScriptKeys(t *testing.T) {
	for _, state := range []encryptionState{{control: 1}, {nexus: 1, scope: 1, encrypt: 2, decrypt: 2, algorithm: 3, control: 1}, {nexus: 2, scope: 2, encrypt: 2, decrypt: 2, algorithm: 3, control: 2, counter: 17}} {
		f := newEncryptionFixture()
		f.state = state
		d := fixtureEncryptionDevice(t, f)
		report := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION)
		require.Equal(t, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, report.Support)
		require.Equal(t, entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED, report.Validation)
		require.Equal(t, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, report.Availability)
		require.Equal(t, uint32(state.encrypt), report.GetEncryption().GetEncryptionMode())
		require.Equal(t, uint32(state.decrypt), report.GetEncryption().GetDecryptionMode())
		require.Equal(t, uint32(state.counter), report.GetEncryption().GetKeyInstanceCounter())
		require.Equal(t, EncryptionAES256GCM, report.GetEncryption().Algorithms[0].Name)
		require.NoError(t, d.Cleanup(context.Background()))
		require.Equal(t, state, f.state)
		require.Zero(t, f.sets)
	}
}

func TestScriptPreparedEncryptionUsesRecordsWithoutNativeKeyCommands(t *testing.T) {
	for _, operation := range []Operation{Read, Append, Format} {
		t.Run(map[Operation]string{Read: "read", Append: "append", Format: "format"}[operation], func(t *testing.T) {
			f := newEncryptionFixture()
			f.state = encryptionState{nexus: 2, scope: 2, encrypt: 2, decrypt: 2, algorithm: 3, control: 2}
			d := fixtureEncryptionDevice(t, f)
			before := f.state
			profile := encryptionTestProfile()
			// Legacy metadata is descriptive; a missing algorithm is equally usable after script preparation.
			if operation == Read {
				profile.EncryptionAlgorithm = proto.String("legacy-public-name")
			}
			records, err := d.Configure(t.Context(), profile, operation)
			require.NoError(t, err)
			queries := f.statusQueries
			if operation == Format {
				require.NoError(t, d.Initialize(t.Context()))
			}
			require.NoError(t, records.Rewind(t.Context()))
			require.NoError(t, records.Filemarks(t.Context(), 0))
			require.NoError(t, d.Cleanup(context.Background()))
			require.Equal(t, before, f.state)
			require.Equal(t, queries, f.statusQueries, "record motion and cleanup do not recreate native key ownership checks")
			require.Zero(t, f.sets)
			require.Equal(t, 1, f.closed)
		})
	}
}

func TestUnknownEncryptionAlgorithmDoesNotBlockScriptPreparedRecords(t *testing.T) {
	f := newEncryptionFixture()
	f.unavailable = true
	f.state = encryptionState{nexus: 2, scope: 2, encrypt: 2, decrypt: 2, algorithm: 9, control: 2}
	d := fixtureEncryptionDevice(t, f)
	require.Empty(t, feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION).GetEncryption().Algorithms)
	_, err := d.Configure(t.Context(), encryptionTestProfile(), Read)
	require.NoError(t, err)
	require.NoError(t, d.Cleanup(context.Background()))
	require.Zero(t, f.sets)
}

func TestEncryptionAlgorithmObservationRejectsMalformedOrInventedNames(t *testing.T) {
	valid := encryptionPage(0x10, 44)
	valid[20], valid[23], valid[24] = 7, 20, 0xba
	binary.BigEndian.PutUint16(valid[30:32], 32)
	binary.BigEndian.PutUint32(valid[40:44], 0x00010014)
	values, err := decodeEncryptionCapabilities(valid)
	require.NoError(t, err)
	require.Equal(t, []encryptionAlgorithm{{index: 7, encrypt: 2, decrypt: 2}}, values)
	for _, data := range [][]byte{valid[:19], valid[:43]} {
		_, err := decodeEncryptionCapabilities(data)
		require.ErrorIs(t, err, ErrMalformed)
	}
	valid[43] = 0x15
	values, err = decodeEncryptionCapabilities(valid)
	require.NoError(t, err)
	require.Empty(t, values, "an unknown algorithm must remain unnamed")
}

func TestEncryptionReportKeepsUnknownAndMalformedAlgorithmsOptional(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown algorithm", true: "malformed descriptor"}[malformed], func(t *testing.T) {
			// A script-installed algorithm remains usable even when native diagnostics cannot name it.
			f := newEncryptionFixture()
			f.state = encryptionState{nexus: 2, scope: 2, encrypt: 2, decrypt: 2, algorithm: 9, control: 2}
			f.capabilities = encryptionPage(0x10, 44)
			f.capabilities[20], f.capabilities[23], f.capabilities[24] = 9, 20, 0xba
			binary.BigEndian.PutUint16(f.capabilities[30:32], 32)
			binary.BigEndian.PutUint32(f.capabilities[40:44], 0x00010015)
			if malformed {
				f.capabilities[23] = 21
			}
			d := fixtureEncryptionDevice(t, f)
			t.Cleanup(func() { require.NoError(t, d.Cleanup(context.Background())) })

			// The complete report retains known status, optional absence and the actual observation failure.
			report := d.Report()
			observed := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION)
			require.Equal(t, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, observed.Support)
			require.Equal(t, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, observed.Availability)
			require.Equal(t, entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED, observed.Validation)
			require.Empty(t, observed.GetEncryption().Algorithms)
			require.Equal(t, uint32(9), observed.GetEncryption().GetCurrentAlgorithmIndex())
			require.True(t, report.GetTape().GetEncryptionEnabled())
			require.Equal(t, entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, report.Reason)
			if malformed {
				require.Equal(t, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE, observed.Reason)
				require.Contains(t, observed.Detail, ErrMalformed.Error())
			} else {
				require.Equal(t, entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, observed.Reason)
			}

			// Optional diagnostics cannot revoke successful adapter preparation or cause native key commands.
			_, err := d.Configure(t.Context(), encryptionTestProfile(), Read)
			require.NoError(t, err)
			require.Zero(t, f.sets)
		})
	}
}
