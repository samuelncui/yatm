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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// A selected device resolves tar from Settings before selecting any physical adapter.
	settings := &stubDeviceSettingsService{value: &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{DefaultFormat: proto.String("tar_pax_v1")}}}}}}
	var target *entity.ArchiveTapeTarget
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterSettingsServiceServer(server, settings)
		registerTestServices(server, &stubService{})
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(_ context.Context, request *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
			target = proto.Clone(request.Target.GetTape()).(*entity.ArchiveTapeTarget)
			return &entity.WriteArchiveMediaResponse{}, nil
		}})
	}, nil, nil)

	// Explicit false remains present and optional omitted fields stay for server resolution.
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "write", "tape", "format", "31", "--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tar Tape", "--confirm-format", "ABC123", "--hardware-compression=false")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "tar_pax_v1", target.GetFormat())
	require.NotNil(t, target.TarOptions.HardwareCompression)
	require.False(t, target.TarOptions.GetHardwareCompression())
	require.Nil(t, target.TarOptions.RecordBytes)
	require.Equal(t, 1, recorder.count(entity.SettingsService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
}

func TestTarFormatCompressionChoicesPreservePresence(t *testing.T) {
	for _, choice := range []string{"", "false", "true"} {
		t.Run("compression="+choice, func(t *testing.T) {
			// The client preserves explicit choices; actual compression eligibility belongs to the leased server attempt.
			var target *entity.ArchiveTapeTarget
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				registerTestServices(server, &stubService{})
				entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{writeMedia: func(_ context.Context, request *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
					target = proto.Clone(request.Target.GetTape()).(*entity.ArchiveTapeTarget)
					return &entity.WriteArchiveMediaResponse{}, nil
				}})
			}, nil, nil)

			// An omitted choice stays for saved-default/recommendation resolution; On and Off arrive unchanged.
			args := []string{"archive", "write", "tape", "format", "31", "--device", "/dev/nst0", "--barcode", "ABC123", "--name", "Tar Tape", "--confirm-format", "ABC123", "--format", "tar_pax_v1"}
			if choice != "" {
				args = append(args, "--hardware-compression="+choice)
			}
			exit, _, stderr := executeTestCLI(server.URL, "", args...)
			require.Equal(t, exitSuccess, exit, stderr)
			if choice == "" {
				require.Nil(t, target.TarOptions)
			} else {
				require.NotNil(t, target.TarOptions.HardwareCompression)
				require.Equal(t, choice == "true", target.TarOptions.GetHardwareCompression())
			}
			require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
			require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
			require.Zero(t, recorder.count(entity.SettingsService_Get_FullMethodName))
		})
	}
}

func TestTarControlHelpNamesEncryptionRequirements(t *testing.T) {
	// Render the real parser's existing flags without resolving or operating a Tape.
	var stdout bytes.Buffer
	exit := run([]string{"archive", "write", "tape", "format", "--help"}, nil, &stdout, new(bytes.Buffer))

	// Compression remains configurable; encryption belongs to the configured Tape script.
	require.Equal(t, exitSuccess, exit)
	help := strings.Join(strings.Fields(stdout.String()), " ")
	require.Contains(t, help, "Tar write compression; the selected drive is rechecked before writing")
	require.NotContains(t, help, "--hardware-encryption")
	require.NotContains(t, help, "--encryption-algorithm")
}

func TestRemovedTarEncryptionFlagsFailBeforeDeviceAccess(t *testing.T) {
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { registerTestServices(server, &stubService{}) }, nil, nil)
	for _, flag := range []string{"--hardware-encryption=true", "--hardware-encryption=false", "--encryption-algorithm=AES-256-GCM-128"} {
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
	// Read and replace the typed preference group through the existing Settings endpoints.
	settings := &stubDeviceSettingsService{value: &entity.DeviceSettings{}}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterSettingsServiceServer(server, settings) }, nil, nil)
	filename := filepath.Join(t.TempDir(), "device.json")
	require.NoError(t, os.WriteFile(filename, []byte(`{"devices":[{"device":"inactive","tape":{"defaultFormat":"ltfs_v1","tarWriteOptions":{"hardwareCompression":false},"readOrder":"TAPE_READ_ORDER_AUTO"}}]}`), 0o600))

	// Inactive tar settings retain explicit false without any device RPC.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "settings", "devices", "--device-json", filename)
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"hardware_compression":false`)
	require.NotNil(t, settings.updated.GetDevice().Devices[0].GetTape().TarWriteOptions.HardwareCompression)
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, settings.updated.GetDevice().Devices[0].GetTape().ReadOrder)
	require.Equal(t, 1, recorder.count(entity.SettingsService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.SettingsService_Update_FullMethodName))
	require.Len(t, recorder.calls, 2)
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

func TestTapeRefreshUsesExistingReadRPCWithoutDuplicatePreflight(t *testing.T) {
	// The server owns frozen policy/profile and qualified reload validation; clients only forward this explicit action.
	restore, scan := &stubHintRestoreService{}, &stubHintScanService{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterRestoreJobServiceServer(server, restore)
		entity.RegisterScanJobServiceServer(server, scan)
	}, nil, nil)
	for _, args := range [][]string{
		{"restore", "run", "tape", "31", "--device", "drive", "--refresh-position-map"},
		{"verify", "run", "31", "--device", "drive", "--media-id", "8", "--refresh-position-map"},
		{"scan", "run", "31", "--device", "drive", "--refresh-position-map"},
	} {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitSuccess, exit, stderr)
		target := scan.target
		if args[0] == "restore" {
			target = restore.target
		}
		require.True(t, target.GetTape().RefreshPositionMap)
		require.Empty(t, target.ExpectedIdentity)
		require.Zero(t, target.ExpectedMediaId)
	}
	require.Equal(t, 1, recorder.count(entity.RestoreJobService_RestoreMedia_FullMethodName))
	require.Equal(t, 2, recorder.count(entity.ScanJobService_ReadMedia_FullMethodName))
	require.Zero(t, recorder.count(entity.ScanJobService_GetCreation_FullMethodName))
	require.Zero(t, recorder.count(entity.SettingsService_Get_FullMethodName))
	require.Zero(t, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
}

func TestTapeRefreshReportsAuthoritativeScanAdmissionRejection(t *testing.T) {
	for _, reason := range []string{"position map refresh requires tar VERIFY_COPIES", "position map refresh is unavailable for LTFS", "non-ejecting reload is unqualified"} {
		// A spelling of verify run cannot establish the frozen Job's policy or hardware applicability.
		service := &stubHintScanService{readErr: status.Error(codes.FailedPrecondition, reason)}
		server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterScanJobServiceServer(server, service) }, nil, nil)
		exit, _, stderr := executeTestCLI(server.URL, "", "verify", "run", "31", "--device", "drive", "--refresh-position-map")
		require.NotEqual(t, exitSuccess, exit)
		require.Contains(t, stderr, reason)
		require.True(t, service.target.GetTape().RefreshPositionMap)
		require.Equal(t, 1, recorder.count(entity.ScanJobService_ReadMedia_FullMethodName))
		require.Zero(t, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
		require.Zero(t, recorder.count(entity.ScanJobService_GetCreation_FullMethodName))
	}
}

func TestTarWritePreflightKeepsOptionalDiagnosticsSeparate(t *testing.T) {
	for _, operation := range []string{"format", "append"} {
		for _, test := range []struct {
			name                              string
			reason                            entity.DeviceReason
			busy, missing, protected, allowed bool
		}{
			{name: "ready", allowed: true},
			{name: "optional unsupported", reason: entity.DeviceReason_DEVICE_REASON_UNSUPPORTED, allowed: true},
			{name: "optional malformed", reason: entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE, allowed: true},
			{name: "optional not implemented", reason: entity.DeviceReason_DEVICE_REASON_NOT_IMPLEMENTED, allowed: true},
			{name: "busy", busy: true},
			{name: "missing medium", missing: true, reason: entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
			{name: "write protected", protected: true},
			{name: "not ready", reason: entity.DeviceReason_DEVICE_REASON_NOT_READY},
			{name: "unknown ownership", reason: entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				// Serve independent optional diagnostics and required physical availability facts.
				service := &stubService{
					mediaList: func(context.Context, *entity.ListMediaRequest) (*entity.ListMediaResponse, error) {
						if operation == "format" {
							return &entity.ListMediaResponse{}, nil
						}
						return &entity.ListMediaResponse{Media: []*entity.Media{{Id: 8, Identity: "ABC123", Kind: entity.MediaKind_MEDIA_KIND_TAPE,
							Profile: (&entity.TapeMediaProfile{Format: "tar_pax_v1"}).Pack()}}}, nil
					},
					deviceInspect: func(context.Context, *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
						return &entity.InspectDeviceResponse{Reason: test.reason, Detail: test.name,
							Device: &entity.DeviceSummary{Busy: test.busy},
							Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{MediumPresent: proto.Bool(!test.missing), WriteProtected: proto.Bool(test.protected)}}}, nil
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
				if test.allowed {
					require.Equal(t, exitSuccess, exit, stderr)
					require.Equal(t, 1, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
				} else {
					require.NotEqual(t, exitSuccess, exit)
					require.Zero(t, recorder.count(entity.ArchiveJobService_WriteMedia_FullMethodName))
				}
			})
		}
	}
}
