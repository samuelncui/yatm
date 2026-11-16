package executor

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func tarTestReadModel(parameters *entity.TapeReadParameters, profile *entity.TarMediaProfile,
	geometry *tape.WrapMap,
) (*mediapkg.ReadCostModel, string) {
	// Tests use the same derived traversal and cost compiler as a read Session.
	if reason := tarReportedWrapsReason(geometry); reason != "" {
		return nil, reason
	}
	value, reason := deriveTarPositionMap(parameters, geometry)
	if value == nil {
		return nil, reason
	}
	return compileTarPositionMap(parameters, profile, value, geometry.Valid)
}

func tarReadParametersFixture() (*entity.TapeReadParameters, *entity.InspectDeviceResponse, *entity.TarMediaProfile) {
	return &entity.TapeReadParameters{AppliesTo: &entity.TapeReadApplicability{
			DriveSerial: proto.String("SN1"), DensityCode: proto.Uint32(0),
		}, Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(90_000_000)},
			Completion: &entity.TapeReadCompletion{FullSpanSeconds: proto.Float64(10), FixedSeconds: proto.Float64(0)}},
		&entity.InspectDeviceResponse{SerialNumber: proto.String("SN1"), Firmware: proto.String("0001"),
			Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{DensityCode: proto.Uint32(0)}}},
		&entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144,
			EncryptionAlgorithm: proto.String("AES-256-GCM-128")}
}

type tarReadMapProvider struct {
	*tarReportingFixture
	endpoints  []tape.WrapEndpoint
	cm         []byte
	cmCalls    int
	partitions byte
}

func (p *tarReadMapProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarReportingFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &tarReadMapHandle{Transport: transport, endpoints: p.endpoints, provider: p}, nil
}

type tarReadMapHandle struct {
	tape.Transport
	endpoints []tape.WrapEndpoint
	provider  *tarReadMapProvider
}

func (h *tarReadMapHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	// CM tests hide only REOWP and supply a frozen directory; all record and control I/O stays real.
	if h.provider.cm != nil {
		if command.CDB[0] == 0x8c && command.CDB[1] == 3 && h.provider.partitions != 0 {
			copy(command.Data, []byte{0, 2, 0, h.provider.partitions})
			return tape.Response{Completed: true}, nil
		}
		if command.CDB[0] == 0x3c {
			h.provider.cmCalls++
			if command.CDB[1] == 3 {
				length := len(h.provider.cm)
				copy(command.Data, []byte{0, byte(length >> 16), byte(length >> 8), byte(length)})
			} else {
				offset := int(command.CDB[3])<<16 | int(command.CDB[4])<<8 | int(command.CDB[5])
				copy(command.Data, h.provider.cm[offset:])
			}
			return tape.Response{Completed: true}, nil
		}
		if command.CDB[0] == 0xa3 && command.CDB[1] == 0x1f {
			sense := tarFixtureSense(5, 0, 0, 0)
			sense[12] = 0x24
			return tape.Response{Completed: true, Status: 2, Sense: sense}, nil
		}
		if command.CDB[0] == 0x12 && command.CDB[1] == 0 {
			response, err := h.Transport.Execute(ctx, command)
			copy(command.Data[8:16], "HP      ")
			copy(command.Data[16:32], "Ultrium 5-SCSI  ")
			return response, err
		}
	}

	// Reuse real passive parsing, navigation and controls; only reported endpoint observations are synthetic.
	if command.CDB[0] != 0xa3 || command.CDB[1] != 0x1f {
		return h.Transport.Execute(ctx, command)
	}
	data := make([]byte, 4+12*len(h.endpoints))
	binary.BigEndian.PutUint16(data, uint16(len(data)-2))
	for index, endpoint := range h.endpoints {
		descriptor := data[4+12*index:]
		binary.BigEndian.PutUint16(descriptor, uint16(endpoint.Wrap))
		binary.BigEndian.PutUint16(descriptor[2:], uint16(endpoint.Partition))
		object := endpoint.Object
		for offset := 11; offset >= 6; offset-- {
			descriptor[offset], object = byte(object), object>>8
		}
	}
	copy(command.Data, data)
	return tape.Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
}

func tarNormalizedParameters() *entity.TapeReadParameters {
	parameters, _, _ := tarReadParametersFixture()
	parameters.Geometry = &entity.TapeReadGeometry{NominalWraps: proto.Uint32(8)}
	parameters.Positioning = &entity.TapeReadPositioning{BaseSeconds: proto.Float64(0),
		ApproachReversalSeconds: proto.Float64(0), SeekSecondsPerWrap: proto.Float64(10)}
	parameters.Reading = &entity.TapeReadTiming{BytesPerSecond: proto.Float64(1e9), FilemarkOverheadSeconds: proto.Float64(0)}
	return parameters
}

func tarReadModelMap(t *testing.T, endpoints []tape.WrapEndpoint) (*tape.WrapMap, func() error) {
	t.Helper()
	provider := &tarReadMapProvider{tarReportingFixture: &tarReportingFixture{tarDeviceFixture: newTarDeviceFixture()}, endpoints: endpoints}
	identity, err := provider.Resolve("/dev/nst0")
	require.NoError(t, err)
	transport, err := provider.Open(identity)
	require.NoError(t, err)
	device, err := tape.NewDevice(context.Background(), identity, transport)
	require.NoError(t, err)
	cleanup := sync.OnceValue(func() error { return device.Cleanup(context.Background()) })
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	geometry, err := device.Geometry(context.Background())
	require.NoError(t, err)
	return geometry, cleanup
}

func TestTarReadModelCompilesObservedCoverageAndPartialMedian(t *testing.T) {
	for _, test := range []struct {
		name      string
		endpoints []uint64
		from, to  uint64
		seconds   float64
	}{
		// Moves behind the head or across the midpoint price the seek-speed span.
		{"full record", []uint64{10, 20, 24}, 9, 0, 9},
		{"short final coverage", []uint64{10, 20, 24}, 24, 20, 4},
		{"even median", []uint64{10, 30, 33}, 33, 30, 2},
		{"long final constrained denominator", []uint64{10, 20, 45}, 20, 45, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoints := make([]tape.WrapEndpoint, len(test.endpoints))
			for index, object := range test.endpoints {
				endpoints[index] = tape.WrapEndpoint{Wrap: uint32(index), Object: object}
			}
			geometry, cleanup := tarReadModelMap(t, endpoints)
			parameters, _, profile := tarReadParametersFixture()
			parameters = tarNormalizedParameters()
			model, reason := tarTestReadModel(parameters, profile, geometry)
			require.NotNil(t, model, reason)
			point := func(object uint64) *entity.TapeBytePosition { return &entity.TapeBytePosition{LogicalObject: object} }
			score, known := model.AccessScore(point(test.from), point(test.to))
			require.True(t, known)
			require.InDelta(t, test.seconds, score, 1e-12)
			last := test.endpoints[len(test.endpoints)-1]
			_, known = model.AccessScore(point(test.from), point(last))
			require.True(t, known, "EOD is a valid host-next endpoint")
			_, known = model.AccessScore(point(test.from), point(last+1))
			require.False(t, known, "an estimated denominator never extends observed coverage")
			require.Equal(t, endpoints, geometry.Endpoints, "compilation cannot rewrite observed objects")
			require.NoError(t, cleanup())
			require.False(t, model.Valid(), "cleanup invalidates the exact map bound into the model")
		})
	}
}

func TestTarReadModelMapsBuffer(t *testing.T) {
	// The buffer converts to whole records; a read-speed locate uses the positioning intercept.
	geometry, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Wrap: 0, Object: 10}, {Wrap: 1, Object: 20}, {Wrap: 2, Object: 24}})
	_, _, profile := tarReadParametersFixture()
	parameters := tarNormalizedParameters()
	parameters.Positioning.BaseSeconds = proto.Float64(7)
	parameters.Positioning.BufferBytes = proto.Uint64(2*262144 + 1)
	point := func(object uint64) *entity.TapeBytePosition { return &entity.TapeBytePosition{LogicalObject: object} }
	model, reason := tarTestReadModel(parameters, profile, geometry)
	require.NotNil(t, model, reason)
	score, known := model.AccessScore(point(1), point(3))
	require.True(t, known)
	require.InDelta(t, 2*262144/1e9, score, 1e-12)
	score, known = model.AccessScore(point(1), point(4))
	require.True(t, known)
	require.InDelta(t, 7+3*262144/1e9, score, 1e-12)

	// The minimum gain is the applied base time, floored at the built-in one.
	require.Equal(t, 7*time.Second, model.MinimumGain())
	parameters.Positioning.BaseSeconds = proto.Float64(0)
	model, reason = tarTestReadModel(parameters, profile, geometry)
	require.NotNil(t, model, reason)
	require.Equal(t, time.Duration(tarSeedBaseSeconds*float64(time.Second)), model.MinimumGain())
	parameters.Positioning.BaseSeconds = proto.Float64(7)

	// A motionless wrap change pays no approach reversal.
	parameters.Positioning.ApproachReversalSeconds = proto.Float64(5)
	model, reason = tarTestReadModel(parameters, profile, geometry)
	require.NotNil(t, model, reason)
	score, known = model.AccessScore(point(8), point(12))
	require.True(t, known)
	require.InDelta(t, 7, score, 1e-12)
}

func TestTarReadModelIncompleteInputsPreserveFallback(t *testing.T) {
	geometry, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 10}, {Wrap: 1, Object: 20}, {Wrap: 2, Object: 24}})
	for name, change := range map[string]func(*entity.TapeReadParameters, *entity.TarMediaProfile, *tape.WrapMap){
		"compressed profile": func(_ *entity.TapeReadParameters, p *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.HardwareCompression = true
		},
		"missing coefficient": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.Positioning.ApproachReversalSeconds = nil
		},
		"missing units": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.Positioning.SeekSecondsPerWrap = nil
		},
		"missing read rate": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.Reading.BytesPerSecond = nil
		},
		"unknown layout": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.Geometry.NominalWraps = nil
		},
		"nonuniform bands": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, _ *tape.WrapMap) {
			p.Geometry.NominalWraps = proto.Uint32(7)
		},
		"too many descriptors": func(p *entity.TapeReadParameters, _ *entity.TarMediaProfile, g *tape.WrapMap) {
			p.Geometry.NominalWraps = proto.Uint32(4)
			g.Endpoints = append(g.Endpoints, tape.WrapEndpoint{Wrap: 3, Object: 29}, tape.WrapEndpoint{Wrap: 4, Object: 34})
		},
		"wrap gap": func(_ *entity.TapeReadParameters, _ *entity.TarMediaProfile, g *tape.WrapMap) {
			g.Endpoints[1].Wrap = 3
		},
		"other partition": func(_ *entity.TapeReadParameters, _ *entity.TarMediaProfile, g *tape.WrapMap) {
			g.Endpoints[1].Partition = 1
		},
		"no full interval": func(_ *entity.TapeReadParameters, _ *entity.TarMediaProfile, g *tape.WrapMap) {
			g.Endpoints = g.Endpoints[:1]
		},
		"empty final interval": func(_ *entity.TapeReadParameters, _ *entity.TarMediaProfile, g *tape.WrapMap) {
			g.Endpoints[2].Object = 20
		},
	} {
		t.Run(name, func(t *testing.T) {
			parameters := tarNormalizedParameters()
			_, _, profile := tarReadParametersFixture()
			copied := *geometry
			copied.Endpoints = append([]tape.WrapEndpoint(nil), geometry.Endpoints...)
			change(parameters, profile, &copied)
			model, reason := tarTestReadModel(parameters, profile, &copied)
			require.Nil(t, model)
			require.NotEmpty(t, reason)
		})
	}
}

func TestTarFactoryLinearModelReachesSharedActionsAndWholeSequence(t *testing.T) {
	for _, test := range []struct {
		order entity.TapeReadOrder
		cm    bool
	}{
		{entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, false}, {entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, false},
		{entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, true}, {entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, true},
	} {
		source := "REOWP"
		if test.cm {
			source = "CM"
		}
		t.Run(source+"/"+test.order.String(), func(t *testing.T) {
			// Write actual members before opening a fresh read session with either directory source.
			order := test.order
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			write, err := newTarFixtureWriteSession(openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			contents := make([][]byte, 4)
			for index := range contents {
				contents[index] = make([]byte, 8192)
				for offset := range contents[index] {
					contents[index][offset] = byte(index + 1)
				}
				tarWriteFixtureMember(t, write, []string{"a", "b", "c", "d"}[index], contents[index])
			}
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			var members []*mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(member *mediapkg.CompletedFile) error { members = append(members, member); return nil }))
			require.Len(t, members, 4)
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
			require.NoError(t, err)
			parameters := tarNormalizedParameters()
			require.NotEmpty(t, stored.Profile.GetTape().Encryption)
			parameters.Positioning.BaseSeconds = proto.Float64(.001)
			parameters.Positioning.SeekSecondsPerWrap = proto.Float64(0)
			parameters.Reading.BytesPerSecond = proto.Float64(1e6) // The known gap costs more than the synthetic millisecond seek.
			settings := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
				Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: order, ReadParameters: []*entity.TapeReadParameters{parameters}}},
			}}}
			_, err = exe.settings.Device.Save(ctx, settings)
			require.NoError(t, err)
			admitted, err := exe.withDeviceSettings(ctx)
			require.NoError(t, err)
			settings.Devices[0].GetTape().ReadParameters = nil
			_, err = exe.settings.Device.Save(ctx, settings)
			require.NoError(t, err)
			deviceProvider := &tarReadMapProvider{tarReportingFixture: &tarReportingFixture{tarDeviceFixture: fixture}, endpoints: []tape.WrapEndpoint{
				{Object: 5}, {Wrap: 1, Object: 9}, {Wrap: 2, Object: uint64(len(fixture.records))},
			}}
			if test.cm {
				deviceProvider.cm, err = os.ReadFile("../media/tape/testdata/cm/single-partition-v4.bin")
				require.NoError(t, err)
			}
			exe.tapeProvider = deviceProvider

			// The prepared model and execution index consume the same complete member intervals.
			read, err := openRead(admitted, backend, tarEncryptedReadTarget(stored))
			require.NoError(t, err)
			finalize := sync.OnceValue(func() error { return read.Finalize(ctx, nil) })
			t.Cleanup(func() { require.NoError(t, finalize()) })
			provider, reason, err := read.(mediapkg.OrderedReadSession).ReadOrder(ctx)
			require.NoError(t, err)
			ids := []int64{4, 2, 1, 3}
			if order == entity.TapeReadOrder_TAPE_READ_ORDER_AUTO {
				require.NotNil(t, provider, reason)
				input := iter.Seq2[readplan.Span, error](func(yield func(readplan.Span, error) bool) {
					for index, member := range members {
						ranges, known := provider.Ranges(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
						require.True(t, known)
						if !yield(readplan.Span{ID: int64(index + 1), Ranges: ranges}, nil) {
							return
						}
					}
				})
				sequence, err := readplan.Plan(ctx, t.TempDir(), provider, input)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sequence.Close()) })
				ids = nil
				for {
					page, err := sequence.Next(ctx, 1)
					require.NoError(t, err)
					if len(page) == 0 {
						break
					}
					ids = append(ids, page...)
				}
				require.ElementsMatch(t, []int64{1, 2, 3, 4}, ids)
			} else {
				require.Nil(t, provider)
			}
			// Read the committed order and check bytes; directory queries must not scale with member count.
			before := len(fixture.readCompression)
			for index, id := range ids {
				member := members[id-1]
				source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
				require.NoError(t, err)
				file, err := source.Content.Open(ctx)
				require.NoError(t, err)
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, contents[id-1], data)
				if order == entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL && index == 0 {
					require.Len(t, fixture.locateCommands, 1, "LOGICAL also uses complete seek pricing rather than reading the gap")
					require.Less(t, len(fixture.readCompression)-before, int(member.Storage.Metadata.GetTar().Header.LogicalObject), "only target records are read")
				}
			}
			require.NoError(t, finalize())
			if provider != nil {
				_, known := provider.Evaluate(readplan.State{Position: &entity.TapeBytePosition{}}, readplan.Range{Start: &entity.TapeBytePosition{}, End: &entity.TapeBytePosition{LogicalObject: 1}})
				require.False(t, known)
			}
			require.Equal(t, fixture.opens, fixture.closes)
			if test.cm {
				require.Equal(t, 3, deviceProvider.cmCalls)
				require.Contains(t, exe.lastPreparedRead("/dev/nst0").PositionSource, "LTO-CM")
			}
		})
	}
}

func TestTapeReadParametersSelectByDensityAndDriveSerial(t *testing.T) {
	// The set for the loaded density applies; current write controls and the Media profile take no part.
	parameters, report, _ := tarReadParametersFixture()
	report.GetTape().CompressionEnabled = proto.Bool(true)
	report.GetTape().FixedRecords = proto.Bool(true)
	report.GetTape().RecordBytes = proto.Uint32(512)
	other := proto.Clone(parameters).(*entity.TapeReadParameters)
	other.AppliesTo.DensityCode, other.SourceReference = proto.Uint32(0x58), "other density"
	settings := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
		Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadParameters: []*entity.TapeReadParameters{other, parameters}}},
	}}}
	selected, source, reason := selectTapeReadParameters(settings, "/dev/nst0", report)
	require.Equal(t, "configured", source)
	require.Empty(t, reason)
	require.True(t, proto.Equal(parameters, selected))
	require.Nil(t, selected.Reading.FilemarkOverheadSeconds, "selection does not fill unmeasured action inputs")

	// A set without a drive serial applies to any drive.
	parameters.AppliesTo.DriveSerial = nil
	report.SerialNumber = nil
	_, source, reason = selectTapeReadParameters(settings, "/dev/nst0", report)
	require.Equal(t, "configured", source)
	require.Empty(t, reason)

	// Another drive serial, another density or an unknown density uses the built-in values.
	for name, change := range map[string]func(*entity.InspectDeviceResponse){
		"different serial": func(r *entity.InspectDeviceResponse) {
			parameters.AppliesTo.DriveSerial = proto.String("SN1")
			r.SerialNumber = proto.String("SN2")
		},
		"unknown serial": func(r *entity.InspectDeviceResponse) {
			parameters.AppliesTo.DriveSerial = proto.String("SN1")
			r.SerialNumber = nil
		},
		"other density":   func(r *entity.InspectDeviceResponse) { r.GetTape().DensityCode = proto.Uint32(0x46) },
		"unknown density": func(r *entity.InspectDeviceResponse) { r.GetTape().DensityCode = nil },
	} {
		t.Run(name, func(t *testing.T) {
			_, report, _ := tarReadParametersFixture()
			parameters.AppliesTo.DriveSerial = nil
			change(report)
			selected, source, reason := selectTapeReadParameters(settings, "/dev/nst0", report)
			require.Equal(t, "builtin", source)
			require.Equal(t, tarBuiltinSourceReference, selected.GetSourceReference())
			if parameters.AppliesTo.DriveSerial != nil {
				require.Contains(t, reason, "drive serial")
			} else {
				require.Empty(t, reason)
			}
		})
	}

	// Without a Tape report no parameters are available.
	selected, source, reason = selectTapeReadParameters(settings, "/dev/nst0", &entity.InspectDeviceResponse{})
	require.Nil(t, selected)
	require.Equal(t, "unavailable", source)
	require.NotEmpty(t, reason)
}

func TestTarReadParametersFreezeBeforeDevicePreparation(t *testing.T) {
	// Freeze nested measurements through the actual StartJob admission, before any device wait.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	parameters, _, _ := tarReadParametersFixture()
	settings := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
		Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadParameters: []*entity.TapeReadParameters{parameters}}},
	}}}
	_, err := exe.settings.Device.Save(ctx, settings)
	require.NoError(t, err)
	job := readyReadOrderJob(t, exe, entity.JobKind_JOB_KIND_RESTORE)
	observed := make(chan *entity.TapeReadParameters, 2)
	release := make(chan struct{})
	releaseRun := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseRun)
	require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_RESTORE, func(runCtx context.Context, _ Runner) error {
		backend := &mediaBackend{executor: exe}
		for attempt := 0; attempt < 2; attempt++ {
			defaults, err := backend.executor.tapeDefaults(runCtx, "/dev/nst0")
			if err != nil {
				return err
			}
			observed <- defaults.ReadParameters[0]
			if attempt == 0 {
				<-release
			}
		}
		return errors.New("controlled Media failure")
	}))
	first := <-observed
	require.True(t, proto.Equal(parameters, first))
	first.Reading.BytesPerSecond = proto.Float64(1)
	parameters.AppliesTo.DriveSerial = proto.String("replacement-drive")
	parameters.Reading.BytesPerSecond = proto.Float64(123)
	_, err = exe.settings.Device.Save(ctx, settings)
	require.NoError(t, err)
	releaseRun()

	// Neither an isolated default edit nor a saved replacement can mutate the admitted measurement.
	second := <-observed
	require.Equal(t, "SN1", second.AppliesTo.GetDriveSerial())
	require.Equal(t, float64(90_000_000), second.Reading.GetBytesPerSecond())
	waitJobReleased(t, exe, job.ID)
	fresh, err := exe.tapeDefaults(ctx, "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, "replacement-drive", fresh.ReadParameters[0].AppliesTo.GetDriveSerial())
	require.Equal(t, float64(123), fresh.ReadParameters[0].Reading.GetBytesPerSecond())
}

func TestTarFactoryLogicalReadUsesApplicableTimingAndRetainedRecord(t *testing.T) {
	for _, condition := range []string{"configured", "different drive", "no drive serial", "unknown encrypted algorithm"} {
		t.Run(condition, func(t *testing.T) {
			// A real writer provides two members sharing one physical record and frozen read conditions.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			fixture.hideAlgorithm = condition == "unknown encrypted algorithm"
			write, err := newTarFixtureWriteSession(openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			contents := [][]byte{[]byte("first content"), []byte("second content")}
			for index, data := range contents {
				tarWriteFixtureMember(t, write, []string{"first", "second"}[index], data)
			}
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			var members []*mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(member *mediapkg.CompletedFile) error {
				members = append(members, member)
				return nil
			}))
			require.Len(t, members, 2)
			require.Equal(t, members[0].Storage.Metadata.GetTar().Header.LogicalObject,
				members[1].Storage.Metadata.GetTar().Header.LogicalObject)
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
			})
			require.NoError(t, err)
			parameters, _, _ := tarReadParametersFixture()
			profile := stored.Profile.GetTape().Tar
			require.NotEmpty(t, stored.Profile.GetTape().Encryption)
			if fixture.hideAlgorithm {
				require.Nil(t, profile.EncryptionAlgorithm, "the saved key does not establish a public algorithm")
			}
			switch condition {
			case "different drive":
				parameters.AppliesTo.DriveSerial = proto.String("another drive")
			case "no drive serial":
				parameters.AppliesTo.DriveSerial = nil
			}
			settings := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
				Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, ReadParameters: []*entity.TapeReadParameters{parameters},
				}},
			}}}
			_, err = exe.settings.Device.Save(ctx, settings)
			require.NoError(t, err)
			admitted, err := exe.withDeviceSettings(ctx)
			require.NoError(t, err)
			parameters.AppliesTo.DriveSerial = proto.String("replacement after admission")
			_, err = exe.settings.Device.Save(ctx, settings)
			require.NoError(t, err)

			// Reading policy is independent of AUTO: the admitted or built-in rate prices Continue without seek timing.
			read, err := openRead(admitted, backend, tarEncryptedReadTarget(stored))
			require.NoError(t, err)
			finalize := sync.OnceValue(func() error { return read.Finalize(ctx, nil) })
			t.Cleanup(func() { require.NoError(t, finalize()) })
			order, _, err := read.(mediapkg.OrderedReadSession).ReadOrder(ctx)
			require.NoError(t, err)
			require.Nil(t, order, "LOGICAL does not expose candidate ranking")
			prepared := exe.lastPreparedRead("/dev/nst0")
			if condition == "different drive" {
				require.Equal(t, "builtin", prepared.ParametersSource)
				require.Contains(t, prepared.ApplicabilityReason, "another drive")
			} else {
				require.Equal(t, "configured", prepared.ParametersSource)
				require.Empty(t, prepared.ApplicabilityReason)
			}
			beforeReads, beforeLocates := len(fixture.readCompression), len(fixture.locateCommands)
			for _, index := range []int{0, 1, 0} {
				member := members[index]
				source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
				require.NoError(t, err)
				file, err := source.Content.Open(ctx)
				require.NoError(t, err)
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, contents[index], data)
				require.Len(t, fixture.readCompression, beforeReads+1, "later and repeated members reuse the actual retained record")
			}
			require.Len(t, fixture.locateCommands, beforeLocates)
			require.NoError(t, finalize())
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

type tarReadInitialProvider struct {
	*tarDeviceFixture
	position func([]byte)
}

func (p *tarReadInitialProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &tarReadInitialHandle{Transport: transport, position: p.position}, nil
}

type tarReadInitialHandle struct {
	tape.Transport
	position  func([]byte)
	labelRead bool
}

func (h *tarReadInitialHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	// Alter only the post-label observation, after real native label records and its FILEMARK settle.
	response, err := h.Transport.Execute(ctx, command)
	if err != nil {
		return response, err
	}
	if command.CDB[0] == 8 && response.Status == 2 && len(response.Sense) > 2 && response.Sense[2]&0x80 != 0 {
		h.labelRead = true
	}
	if h.labelRead && command.CDB[0] == 0x34 && command.CDB[1] == 6 && response.Status == 0 {
		h.position(command.Data)
	}
	return response, nil
}

func TestTarFactoryPostLabelPositionChecksKnownFile(t *testing.T) {
	for _, test := range []struct {
		name      string
		position  func([]byte)
		wantError bool
	}{
		{"beginning", func(data []byte) {
			data[0] = 0x80
			clear(data[8:24])
		}, true},
		{"different known file", func(data []byte) { binary.BigEndian.PutUint64(data[16:24], 2) }, true},
		{"unknown object with different known file", func(data []byte) {
			data[0] |= 4
			binary.BigEndian.PutUint64(data[16:24], 2)
		}, true},
		{"unknown file", func(data []byte) {
			data[0] |= 8
			binary.BigEndian.PutUint64(data[16:24], 1000) // Undefined counters are not file evidence.
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Freeze a real writer's profile and locator before injecting one contradictory optional report.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			write, err := newTarFixtureWriteSession(openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			target := []byte("ordinary selected content")
			tarWriteFixtureMember(t, write, "member", target)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			var member *mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
			})
			require.NoError(t, err)
			parameters, _, _ := tarReadParametersFixture()
			require.NotEmpty(t, stored.Profile.GetTape().Encryption)
			_, err = exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
				Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadParameters: []*entity.TapeReadParameters{parameters}}},
			}}})
			require.NoError(t, err)
			exe.tapeProvider = &tarReadInitialProvider{tarDeviceFixture: fixture, position: test.position}
			read, err := openRead(ctx, backend, tarEncryptedReadTarget(stored))
			if test.wantError {
				require.Nil(t, read)
				require.ErrorIs(t, err, mediapkg.ErrReadUnavailable)
				require.ErrorContains(t, err, "tar position differs from the label's Tape file")
				require.Empty(t, fixture.locateCommands)
			} else {
				require.NoError(t, err)
				finalize := sync.OnceValue(func() error { return read.Finalize(ctx, nil) })
				t.Cleanup(func() { require.NoError(t, finalize()) })
				source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
				require.NoError(t, err)
				file, err := source.Content.Open(ctx)
				require.NoError(t, err)
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, target, data)
				require.NoError(t, finalize())
			}
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}
