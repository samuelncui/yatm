package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type stubDeviceSettingsService struct {
	entity.UnimplementedSettingsServiceServer
	value   *entity.DeviceSettings
	updated *entity.SettingsValue
}

func (s *stubDeviceSettingsService) Get(_ context.Context, request *entity.GetSettingsRequest) (*entity.GetSettingsResponse, error) {
	return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Device{Device: s.value}}}, nil
}
func (s *stubDeviceSettingsService) Update(_ context.Context, request *entity.UpdateSettingsRequest) (*entity.UpdateSettingsResponse, error) {
	s.updated = proto.Clone(request.Value).(*entity.SettingsValue)
	return &entity.UpdateSettingsResponse{Value: request.Value}, nil
}

func TestTarFormatUsesDeviceDefaultWithoutLTFSInspection(t *testing.T) {
	for name, saved := range map[string]*entity.DeviceSettings{
		"saved tar": {Devices: []*entity.DeviceConfig{{Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{DefaultFormat: proto.String("tar_pax_v1")}}}}},
		"unset":     {},
	} {
		t.Run(name, func(t *testing.T) {
			// A selected device resolves tar from Settings, also when none is saved, before selecting any physical adapter.
			settings := &stubDeviceSettingsService{value: saved}
			var target *entity.ArchiveTapeTarget
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterSettingsServiceServer(server, settings)
				registerTestServices(server, &stubService{})
				entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(_ context.Context, request *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
					target = proto.Clone(request.Target.GetTape()).(*entity.ArchiveTapeTarget)
					return &entity.WriteArchiveMediaResponse{}, nil
				}})
			}, nil, nil)

			// An explicit option remains present and optional omitted fields stay for server resolution.
			exit, _, stderr := executeTestCLI(server.URL, "", "archive", "write", "tape", "format", "31", "--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tar Tape", "--confirm-format", "ABC123", "--record-bytes=262144")
			require.Equal(t, exitSuccess, exit, stderr)
			require.Equal(t, "tar_pax_v1", target.GetFormat())
			require.EqualValues(t, 262144, target.TarOptions.GetRecordBytes())
			require.Nil(t, target.TarOptions.RecordMode)
			require.Equal(t, 1, recorder.count(entity.SettingsService_Get_FullMethodName))
			require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
			require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
		})
	}
}

func TestTarControlHelpNamesEncryptionRequirements(t *testing.T) {
	// Render the real parser's existing flags without resolving or operating a Tape.
	var stdout bytes.Buffer
	exit := run([]string{"archive", "write", "tape", "format", "--help"}, nil, &stdout, new(bytes.Buffer))

	// Tar writes always leave drive compression off; encryption belongs to the configured Tape script.
	require.Equal(t, exitSuccess, exit)
	help := strings.Join(strings.Fields(stdout.String()), " ")
	require.NotContains(t, help, "--hardware-compression")
	require.NotContains(t, help, "--hardware-encryption")
	require.NotContains(t, help, "--encryption-algorithm")
}

func TestRemovedTarFlagsFailBeforeDeviceAccess(t *testing.T) {
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { registerTestServices(server, &stubService{}) }, nil, nil)
	for _, flag := range []string{"--hardware-encryption=true", "--hardware-encryption=false", "--encryption-algorithm=AES-256-GCM-128", "--hardware-compression=false"} {
		args := []string{"archive", "write", "tape", "format", "31", "--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tar Tape", "--confirm-format", "ABC123", "--format", "tar_pax_v1", flag}
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
	require.Zero(t, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	require.Zero(t, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
}

func TestTarAppendUsesExactRegisteredProfile(t *testing.T) {
	// A fuzzy name on the first catalog page cannot establish the required Tape identity.
	registered := &entity.Media{Id: 8, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC123", Profile: (&entity.TapeMediaProfile{Format: "tar_pax_v1"}).Pack()}
	var target *entity.ArchiveTapeTarget
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{mediaList: func(_ context.Context, request *entity.ListMediaRequest) (*entity.ListMediaResponse, error) {
			if request.GetList().GetAfterId() == 0 {
				return &entity.ListMediaResponse{Media: []*entity.Media{{Id: 4, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Name: "ABC123", Identity: "OTHER1"}}, HasMore: true}, nil
			}
			return &entity.ListMediaResponse{Media: []*entity.Media{registered}}, nil
		}})
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(_ context.Context, request *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
			target = proto.Clone(request.Target.GetTape()).(*entity.ArchiveTapeTarget)
			return &entity.WriteArchiveMediaResponse{}, nil
		}})
	}, nil, nil)

	// APPEND derives format from stored Media and sends no new FORMAT options.
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "write", "tape", "append", "31", "--device", "/dev/nst0", "--barcode", "ABC123")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "tar_pax_v1", target.GetFormat())
	require.Nil(t, target.TarOptions)
	require.Equal(t, 2, recorder.count(entity.MediaService_List_FullMethodName))
	require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
	require.Zero(t, recorder.count(entity.SettingsService_Get_FullMethodName))
}

func TestDeviceSettingsReplacementNeverInspectsHardware(t *testing.T) {
	// Replace the typed preference group through the existing Settings endpoint.
	settings := &stubDeviceSettingsService{value: &entity.DeviceSettings{}}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)
	filename := filepath.Join(t.TempDir(), "device.json")
	require.NoError(t, os.WriteFile(filename, []byte(`{"devices":[{"device":"inactive","tape":{"defaultFormat":"ltfs_v1","tarWriteOptions":{"recordBytes":262144},"readOrder":"TAPE_READ_ORDER_AUTO"}}]}`), 0o600))

	// Inactive tar settings retain explicit options without any device RPC.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "settings", "devices", "--device-json", filename)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"record_bytes":262144`)
	require.EqualValues(t, 262144, settings.updated.GetDevice().Devices[0].GetTape().TarWriteOptions.GetRecordBytes())
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, settings.updated.GetDevice().Devices[0].GetTape().ReadOrder)
	require.Zero(t, recorder.count(entity.SettingsService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.SettingsService_Update_FullMethodName))
	require.Len(t, recorder.calls, 1)
}

func TestDeviceSettingsReplacementAcceptsEchoedBuiltinReadParameters(t *testing.T) {
	// The read output carries the server's built-in sets; replacing with that output is accepted, and the server drops them.
	builtin := []*entity.TapeReadParameters{{AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x58)},
		Geometry: &entity.TapeReadGeometry{NominalWraps: proto.Uint32(80)}, SourceReference: "built-in"}}
	settings := &stubDeviceSettingsService{value: &entity.DeviceSettings{BuiltinReadParameters: builtin}}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)
	exit, stdout, stderr := executeTestCLI(server.URL, "", "settings", "devices")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"builtin_read_parameters"`)
	filename := filepath.Join(t.TempDir(), "device.json")
	require.NoError(t, os.WriteFile(filename, []byte(stdout), 0o600))
	exit, _, stderr = executeTestCLI(server.URL, "", "settings", "devices", "--device-json", filename)
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, proto.Equal(builtin[0], settings.updated.GetDevice().BuiltinReadParameters[0]))
}

type stubHintRestoreService struct {
	entity.UnimplementedRestoreJobServiceServer
	target *entity.ReadMediaTarget
}

func (s *stubHintRestoreService) RestoreMedia(_ context.Context, request *entity.RestoreMediaRequest) (*entity.RestoreMediaResponse, error) {
	s.target = proto.Clone(request.Target).(*entity.ReadMediaTarget)
	return &entity.RestoreMediaResponse{}, nil
}

type stubHintScanService struct {
	entity.UnimplementedScanJobServiceServer
	target  *entity.ReadMediaTarget
	readErr error
}

func (s *stubHintScanService) ReadMedia(_ context.Context, request *entity.ReadScanMediaRequest) (*entity.ReadScanMediaResponse, error) {
	s.target = proto.Clone(request.Target).(*entity.ReadMediaTarget)
	return &entity.ReadScanMediaResponse{}, s.readErr
}

func TestTapeReadHintsUseExistingOperationWithoutCartridgePreflight(t *testing.T) {
	// Preserve the optional hint while leaving expected identity/profile to each server runner.
	restore := &stubHintRestoreService{}
	scan := &stubHintScanService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterRestoreJobServiceServer(server, restore)
		entity.RegisterScanJobServiceServer(server, scan)
	}, nil, nil)

	// Omission selects automatic identification; the explicit hint uses the same operation.
	exit, _, stderr := executeTestCLI(server.URL, "", "restore", "run", "tape", "31", "--device", "drive")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Nil(t, restore.target.GetTape().MediaId)
	exit, _, stderr = executeTestCLI(server.URL, "", "restore", "run", "tape", "31", "--device", "drive", "--media-id", "8")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 8, restore.target.GetTape().GetMediaId())
	require.Zero(t, restore.target.ExpectedMediaId)
	require.Nil(t, restore.target.ExpectedProfile)
	exit, _, stderr = executeTestCLI(server.URL, "", "verify", "run", "31", "--device", "drive", "--media-id", "8")
	require.Equal(t, exitSuccess, exit, stderr)
	require.EqualValues(t, 8, scan.target.GetTape().GetMediaId())
	require.Zero(t, scan.target.ExpectedMediaId)
	require.Len(t, recorder.calls, 2)
	require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
}

func TestTarWritePreflightFollowsServerVerdict(t *testing.T) {
	for _, operation := range []string{"format", "append"} {
		for _, test := range []struct {
			name, detail string
			allowed      bool
		}{
			{name: "writable", allowed: true},
			{name: "optional diagnostic failure", detail: "Optional serial query is unsupported", allowed: true},
			{name: "device unavailable", detail: "Tape device is unavailable: Tape device is in use"},
			{name: "Media rejected", detail: "Tape already exists in the Library; delete its Media metadata explicitly before formatting"},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				// The server answers the exact requested write; the CLI applies no diagnostic allowlist of its own.
				var requested *entity.InspectDeviceRequest
				service := &stubService{
					mediaList: func(context.Context, *entity.ListMediaRequest) (*entity.ListMediaResponse, error) {
						return &entity.ListMediaResponse{Media: []*entity.Media{{Id: 8, Identity: "ABC123", Kind: entity.MediaKind_MEDIA_KIND_TAPE,
							Profile: (&entity.TapeMediaProfile{Format: "tar_pax_v1"}).Pack()}}}, nil
					},
					deviceInspect: func(_ context.Context, request *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
						requested = proto.Clone(request).(*entity.InspectDeviceRequest)
						reply := &entity.InspectDeviceResponse{Writable: test.allowed}
						if test.allowed {
							reply.Detail = test.detail
						} else {
							reply.WriteDetail = test.detail
						}
						return reply, nil
					},
				}
				server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
					registerTestServices(server, service)
					entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(context.Context, *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
						return &entity.WriteArchiveMediaResponse{}, nil
					}})
				}, nil, nil)

				// The leased server receives usable requests once; physical blockers never submit writes.
				args := []string{"archive", "write", "tape", operation, "31", "--device", "/dev/nst0", "--barcode", "ABC123", "--format", "tar_pax_v1"}
				if operation == "format" {
					args = append(args, "--name", "Tar Tape", "--confirm-format", "ABC123")
				}
				exit, _, stderr := executeTestCLI(server.URL, "", args...)
				require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
				require.Equal(t, "/dev/nst0", requested.GetWriteTarget().GetDevice())
				require.Equal(t, "ABC123", requested.GetWriteTarget().GetBarcode())
				require.Equal(t, "tar_pax_v1", requested.GetWriteTarget().GetFormat())
				require.Equal(t, operation == "format", requested.GetWriteTarget().GetMode() == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT)
				if test.allowed {
					require.Equal(t, exitSuccess, exit, stderr)
					require.Equal(t, 1, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
				} else {
					require.Equal(t, exitUsage, exit)
					require.Contains(t, stderr, test.detail)
					require.Zero(t, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
				}
			})
		}
	}
}
