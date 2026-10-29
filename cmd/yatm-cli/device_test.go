package main

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestTapeDeviceInspectPreservesUnknownDiagnostics(t *testing.T) {
	// Serve a busy device without pretending that passive observation established hardware facts.
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{deviceInspect: func(
			_ context.Context, request *entity.InspectDeviceRequest,
		) (*entity.InspectDeviceResponse, error) {
			require.Equal(t, "/dev/nst0", request.Device)
			return &entity.InspectDeviceResponse{
				Device: &entity.DeviceSummary{
					Device: request.Device, Kind: entity.DeviceKind_DEVICE_KIND_TAPE,
					Busy: true, Detail: "Archive owns the device",
				},
				Reason: entity.DeviceReason_DEVICE_REASON_BUSY,
			}, nil
		}})
	}, nil, nil)

	// The device command uses its dedicated RPC, retaining absent values in ProtoJSON.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", " /dev/nst0 ")
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{
		"device":{"device":"/dev/nst0","kind":"DEVICE_KIND_TAPE","busy":true,"detail":"Archive owns the device"},
		"reason":"DEVICE_REASON_BUSY"
	}`, stdout)
	require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
}

func TestTapeDeviceInspectRejectsEmptySelector(t *testing.T) {
	// Invalid selectors must not acquire or inspect a device.
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{})
	}, nil, nil)

	// Reject the selector before making a hardware request.
	exit, _, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", " ")
	require.Equal(t, exitUsage, exit, stderr)
	require.Zero(t, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
}

func TestTapeDeviceHelpSeparatesPassiveDeviceAndCartridgeInspection(t *testing.T) {
	// Render the device namespace through the real parser without connecting to a server.
	var stdout bytes.Buffer
	exit := run([]string{"tape", "device", "--help"}, nil, &stdout, new(bytes.Buffer))

	// Keep configuration listing and passive diagnostics distinct from cartridge inspection.
	require.Equal(t, exitSuccess, exit)
	require.Contains(t, stdout.String(), "List configured Tape devices and availability")
	require.Contains(t, stdout.String(), "Inspect a Tape device without moving or writing Media")
}

func TestTapeDeviceInspectKeepsReportedSupportUnvalidated(t *testing.T) {
	// A passive report can describe support without establishing present availability or qualification.
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		registerTestServices(server, &stubService{deviceInspect: func(
			_ context.Context, request *entity.InspectDeviceRequest,
		) (*entity.InspectDeviceResponse, error) {
			return &entity.InspectDeviceResponse{
				Device: &entity.DeviceSummary{Device: request.Device, Kind: entity.DeviceKind_DEVICE_KIND_TAPE},
				Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{
					MediumPresent: proto.Bool(false),
					Features: []*entity.DeviceFeature{{
						Feature:      entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
						Support:      entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
						Availability: entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN,
						Validation:   entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
						Evidence:     entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED,
					}},
				}},
			}, nil
		}})
	}, nil, nil)

	// Preserve each independent state and the known false observation in the public JSON output.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", "/dev/nst0")
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{
		"device":{"device":"/dev/nst0","kind":"DEVICE_KIND_TAPE"},
		"tape":{"medium_present":false,"features":[{
			"feature":"DEVICE_FEATURE_KIND_LOCATE","support":"DEVICE_FEATURE_SUPPORT_SUPPORTED",
			"availability":"DEVICE_FEATURE_AVAILABILITY_UNKNOWN","validation":"DEVICE_FEATURE_VALIDATION_UNVALIDATED",
			"evidence":"DEVICE_FEATURE_EVIDENCE_REPORTED"
		}]}
	}`, stdout)
}

func TestTapeDeviceInspectPreservesTransientCapacity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capacity *entity.TapeCapacityLimits
		json     string
	}{
		{"estimate", &entity.TapeCapacityLimits{
			Partition: proto.Uint32(0), RemainingBytes: proto.Uint64(math.MaxUint64), Quality: entity.TapeCapacityQuality_TAPE_CAPACITY_QUALITY_ESTIMATE,
			IssuedAtNs: proto.Int64(1792029600000000000), CompletedAtNs: proto.Int64(1792029660000000000), ObservationErrorBytes: proto.Uint64(0),
		}, `{"partition":0,"remaining_bytes":"18446744073709551615","quality":"TAPE_CAPACITY_QUALITY_ESTIMATE","issued_at_ns":"1792029600000000000","completed_at_ns":"1792029660000000000","observation_error_bytes":"0"}`},
		{"unknown", &entity.TapeCapacityLimits{}, `{}`},
		{"observed zero", &entity.TapeCapacityLimits{RemainingBytes: proto.Uint64(0)}, `{"remaining_bytes":"0"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Passive capacity keeps exact uint64 units and unknown allowances without claiming physical qualification.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				registerTestServices(server, &stubService{deviceInspect: func(context.Context, *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
					return &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{Features: []*entity.DeviceFeature{{
						Feature: entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING, Validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
						Limits: &entity.DeviceFeature_Capacity{Capacity: tc.capacity},
					}}}}}, nil
				}})
			}, nil, nil)

			// The existing diagnostics command forwards the transient report with no alternate RPC or setting write.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", "/dev/nst0")
			require.Equal(t, exitSuccess, exit, stderr)
			require.JSONEq(t, `{"tape":{"features":[{"feature":"DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING","validation":"DEVICE_FEATURE_VALIDATION_UNVALIDATED","capacity":`+tc.json+`}]}}`, stdout)
			require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
			require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
		})
	}
}

func TestTapeDeviceInspectPreservesCompressionFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits *entity.TapeCompressionLimits
		json   string
	}{
		{"unknown", &entity.TapeCompressionLimits{}, `{}`},
		{"known zero and false", &entity.TapeCompressionLimits{CurrentAlgorithm: proto.Uint32(0), EnableChangeable: proto.Bool(false)}, `{"current_algorithm":0,"enable_changeable":false}`},
		{"known maximum and true", &entity.TapeCompressionLimits{CurrentAlgorithm: proto.Uint32(math.MaxUint32), EnableChangeable: proto.Bool(true)}, `{"current_algorithm":4294967295,"enable_changeable":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Algorithm identifiers and changeability are observations, not a supported list or qualification.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				registerTestServices(server, &stubService{deviceInspect: func(context.Context, *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
					return &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{
						CompressionEnabled: proto.Bool(true), DecompressionEnabled: proto.Bool(false),
						Features: []*entity.DeviceFeature{{Feature: entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION,
							Support: entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, Availability: entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY,
							Validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED, Limits: &entity.DeviceFeature_Compression{Compression: tc.limits}}},
					}}}, nil
				}})
			}, nil, nil)

			// ProtoJSON retains known false/zero and absent facts independently of support and current state.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", "/dev/nst0")
			require.Equal(t, exitSuccess, exit, stderr)
			require.JSONEq(t, `{"tape":{"compression_enabled":true,"decompression_enabled":false,"features":[{"feature":"DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION","support":"DEVICE_FEATURE_SUPPORT_SUPPORTED","availability":"DEVICE_FEATURE_AVAILABILITY_READY","validation":"DEVICE_FEATURE_VALIDATION_UNVALIDATED","compression":`+tc.json+`}]}}`, stdout)
			require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
			require.Zero(t, recorder.count(entity.MediaService_Inspect_FullMethodName))
		})
	}
}

func TestTapeDeviceInspectPreservesEncryptionFactsWithoutKeyPresence(t *testing.T) {
	for _, counter := range []*uint32{nil, proto.Uint32(0)} {
		// Non-secret protocol observations keep absent counters distinct from zero and empirical qualification.
		server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
			registerTestServices(server, &stubService{deviceInspect: func(context.Context, *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
				return &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{
					EncryptionEnabled: proto.Bool(false), Features: []*entity.DeviceFeature{{
						Feature: entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION, Validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
						Limits: &entity.DeviceFeature_Encryption{Encryption: &entity.TapeEncryptionLimits{
							Algorithms:            []*entity.TapeEncryptionAlgorithm{{Name: "AES-256-GCM-128", Index: 17, KeyBytes: 32}},
							CurrentAlgorithmIndex: proto.Uint32(0), EncryptionMode: proto.Uint32(0), DecryptionMode: proto.Uint32(2), ITNexusScope: proto.Uint32(0), KeyScope: proto.Uint32(2), ParametersControl: proto.Uint32(0), KeyInstanceCounter: counter,
						}},
					}},
				}}}, nil
			}})
		}, nil, nil)

		// The existing ProtoJSON path exposes only the typed observations, never a matching-key claim.
		exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", "/dev/nst0")
		require.Equal(t, exitSuccess, exit, stderr)
		counterJSON := ""
		if counter != nil {
			counterJSON = `,"key_instance_counter":0`
		}
		require.JSONEq(t, `{"tape":{"encryption_enabled":false,"features":[{"feature":"DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION","validation":"DEVICE_FEATURE_VALIDATION_UNVALIDATED","encryption":{"algorithms":[{"name":"AES-256-GCM-128","index":17,"key_bytes":32}],"current_algorithm_index":0,"encryption_mode":0,"decryption_mode":2,"i_t_nexus_scope":0,"key_scope":2,"parameters_control":0`+counterJSON+`}}]}}`, stdout)
		require.NotContains(t, stdout, "v1:")
		require.NotContains(t, stdout, "key_present")
		require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	}
}

func TestTapeDeviceInspectPreservesGeometryUnknownAndFallback(t *testing.T) {
	for _, fresh := range []*bool{nil, proto.Bool(false)} {
		// An observed long response/all-wrap form does not establish H6 coverage, freshness or a qualified ordering model.
		server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
			registerTestServices(server, &stubService{deviceInspect: func(context.Context, *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
				return &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{Features: []*entity.DeviceFeature{{
					Feature: entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER, Validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
					Detail: "No qualified geometry/timing model; effective read order is LOGICAL",
					Limits: &entity.DeviceFeature_Geometry{Geometry: &entity.TapeGeometryLimits{AllWraps: proto.Bool(true), ReportedWraps: proto.Uint32(0), Fresh: fresh, Source: proto.String("REOWP long response")}},
				}}}}}, nil
			}})
		}, nil, nil)
		exit, stdout, stderr := executeTestCLI(server.URL, "", "tape", "device", "inspect", "--device", "/dev/nst0")
		require.Equal(t, exitSuccess, exit, stderr)
		freshJSON := ""
		if fresh != nil {
			freshJSON = `,"fresh":false`
		}
		require.JSONEq(t, `{"tape":{"features":[{"feature":"DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER","validation":"DEVICE_FEATURE_VALIDATION_UNVALIDATED","detail":"No qualified geometry/timing model; effective read order is LOGICAL","geometry":{"all_wraps":true,"reported_wraps":0`+freshJSON+`,"source":"REOWP long response"}}]}}`, stdout)
		require.Equal(t, 1, recorder.count(entity.MediaService_InspectDevice_FullMethodName))
	}
}
