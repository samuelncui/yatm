package executor

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type tarBaselineProvider struct {
	*tarDeviceFixture
	commandsLock sync.Mutex
	commands     [][]byte
}

func (p *tarBaselineProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &fakeNativeTransport{Transport: &fakeTransport{Transport: transport, execute: func(ctx context.Context, command tape.Command) (tape.Response, error) {
		// A documented protocol family reports REOWP unsupported; the command trace detects accidental probes.
		p.commandsLock.Lock()
		p.commands = append(p.commands, append([]byte(nil), command.CDB...))
		p.commandsLock.Unlock()
		if command.CDB[0] == 0xa3 && command.CDB[1] == 0x0c && command.CDB[3] == 0xa3 {
			clear(command.Data)
			command.Data[1] = 1
			return tape.Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
		}
		response, err := transport.Execute(ctx, command)
		if command.CDB[0] == 0x12 && command.CDB[1] == 0 && err == nil {
			copy(command.Data[8:16], "IBM     ")
		}
		return response, err
	}}, limit: 4096}, nil
}

func TestTarFactoryBaselineFormatAppendSelectedACPReadWithoutOptionalFeatures(t *testing.T) {
	for _, source := range []string{"builtin", "learned"} {
		t.Run(source, func(t *testing.T) {
			// Admit AUTO and ordinary passive read learning while optional hardware facts remain unavailable; MAM identity is required.
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			unsupported := byte(1)
			fixture.navigationSupport = &unsupported
			fixture.capacityEOD, fixture.capacityEW = proto.Uint32(math.MaxUint32), proto.Uint32(math.MaxUint32)
			fixture.mode[8] = 0x58
			originalMode := append([]byte(nil), fixture.mode...)
			provider := &tarBaselineProvider{tarDeviceFixture: fixture}
			exe.tapeProvider = provider
			_, err := exe.settings.Device.Save(t.Context(), &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
				Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
					ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, PassiveReadLearning: proto.Bool(true),
				}},
			}}})
			require.NoError(t, err)
			if source == "learned" {
				parameters := tarNormalizedParameters()
				parameters.AppliesTo.DensityCode, parameters.AppliesTo.RecordBytes = proto.Uint32(0x58), proto.Uint32(4096)
				parameters.AppliesTo.EncryptionAlgorithm = proto.String("AES-256-GCM-128")
				parameters.Geometry.NominalWraps = proto.Uint32(80)
				parameters.SourceReference = "Synthetic independently validated timing fixture"
				applied, err := exe.settings.Device.PublishTapeReadModel(t.Context(), nil, &entity.TapeLearnedReadParameters{
					Device: "/dev/nst0", Parameters: parameters, FitVersion: "native-linear-1", UpdatedAtNs: time.Now().UnixNano(),
					TrainingObservations: 32, MaeSeconds: 1, MaxErrorSeconds: 2,
				})
				require.NoError(t, err)
				require.True(t, applied)
			}
			ctx, err := exe.withDeviceSettings(t.Context())
			require.NoError(t, err)

			// FORMAT publishes ordinary file-relative locators without manufacturing physical object anchors.
			contents := [][]byte{[]byte("first archive content")}
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			copied := []acp.Result{tarWriteFixtureMember(t, write, "first.txt", contents[0])}
			formatted, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, "ABC001", formatted.Media.Identity)
			require.NotEmpty(t, formatted.Media.Profile.GetTape().Encryption)
			require.Equal(t, "AES-256-GCM-128", formatted.Media.Profile.GetTape().Tar.GetEncryptionAlgorithm())
			require.Nil(t, formatted.Media.TapePositionMap)
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: formatted.Media.Kind,
				Identity: formatted.Media.Identity, Profile: formatted.Media.Profile})
			require.NoError(t, err)
			var members []*mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, formatted, ctx, func(member *mediapkg.CompletedFile) error {
				members = append(members, member)
				return nil
			}))

			// APPEND requires long READ POSITION/LOCATE for its bounded tail proof and never scans from BOT.
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			writes, marks := fixture.writes, fixture.filemarks
			_, err = backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack())
			require.ErrorContains(t, err, "APPEND requires long READ POSITION and LOCATE support")
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, marks, fixture.filemarks)
			require.Len(t, members, 1)
			for index, member := range members {
				locator := member.Storage.Metadata.GetTar()
				require.Equal(t, uint64(index+1), locator.TapeFile)
				require.Nil(t, locator.Header)
				require.Nil(t, locator.End)
				require.EqualValues(t, len(contents[index]), member.Size)
			}

			// The actual read factory keeps LOGICAL access even when a matching timing source exists.
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			read, err := backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
			require.NoError(t, err)
			order, reason, err := read.(mediapkg.OrderedReadSession).ReadOrder(ctx)
			require.NoError(t, err)
			require.Nil(t, order)
			require.Contains(t, reason, "pairing is unavailable")
			status := exe.lastPreparedRead("/dev/nst0")
			require.Equal(t, source, status.ParametersSource)
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL.String(), status.EffectiveOrder)
			var inventory []*mediapkg.InventoryEntry
			require.NoError(t, read.(mediapkg.InventorySession).WalkInventory(ctx, func(entry *mediapkg.InventoryEntry) error {
				inventory = append(inventory, entry)
				return nil
			}))
			require.Len(t, inventory, 1)
			for index, entry := range inventory {
				require.Equal(t, members[index].Path, entry.Path)
				require.True(t, proto.Equal(members[index].Storage, entry.Storage))
			}

			// Selected ACP reads preserve bytes/hash/file metadata.
			for _, index := range []int{0} {
				member := members[index]
				ref, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
				require.NoError(t, err)
				output := filepath.Join(t.TempDir(), "restored")
				restored := runTarFixtureStream(t, &tarFixtureItem{SimpleJob: &acp.SimpleJob{
					Path: member.Path, Dsts: []string{output}}, source: ref.Content})
				require.Equal(t, copied[index].SHA256, restored.SHA256)
				require.Equal(t, copied[index].Mode, restored.Mode)
				require.True(t, copied[index].ModTime.Equal(restored.ModTime))
				actual, err := os.ReadFile(output)
				require.NoError(t, err)
				require.Equal(t, contents[index], actual)
				info, err := os.Stat(output)
				require.NoError(t, err)
				require.Equal(t, copied[index].Mode.Perm(), info.Mode().Perm())
				require.True(t, copied[index].ModTime.Equal(info.ModTime()))
			}
			require.NoError(t, read.Finalize(ctx, nil))

			// Optional observations cannot invent geometry, navigation commands or an admitted capacity value.
			require.Len(t, fixture.positionCommands, fixture.opens, "only the passive partition observation runs per descriptor")
			require.Empty(t, fixture.locateCommands)
			require.NotEmpty(t, fixture.capacityQueries)
			for _, command := range provider.commands {
				require.False(t, command[0] == 0xa3 && command[1] == 0x1f, "unsupported REOWP must never be issued")
			}
			reloaded, err := exe.Lib().GetMedia(ctx, stored.ID)
			require.NoError(t, err)
			require.Nil(t, reloaded.TapePositionMap)
			require.True(t, proto.Equal(stored.Profile, reloaded.Profile))
			require.Equal(t, originalMode, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
			require.Equal(t, 6, fixture.opens, "FORMAT, refused APPEND and READ each close their preflight descriptor before the script and reopen")
		})
	}
}
