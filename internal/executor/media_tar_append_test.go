package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

type tarAppendFixtureProvider struct {
	*tarDeviceFixture
	caseName      string
	commands      [][]byte
	readObjects   []int
	eodMotions    int
	tailPositions int
	tailReads     int
	tailLocates   int
	beforeCommand func(tape.Command)
}

func (p *tarAppendFixtureProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &fakeTransport{Transport: transport, execute: func(ctx context.Context, command tape.Command) (tape.Response, error) {
		return p.execute(ctx, command, transport)
	}}, nil
}

func (p *tarAppendFixtureProvider) execute(ctx context.Context, command tape.Command, base tape.Transport) (tape.Response, error) {
	// Observe real factory preparation over synthetic records; inject only this case's identified failure.
	p.commands = append(p.commands, bytes.Clone(command.CDB))
	if p.beforeCommand != nil {
		p.beforeCommand(command)
	}
	opcode := command.CDB[0]
	if opcode == 0x11 && command.CDB[1] == 3 {
		p.eodMotions++
		if p.caseName == "failed SPACE" {
			return tape.Response{}, errors.New("injected SPACE failure")
		}
		if p.caseName == "unknown SPACE completion" {
			return tape.Response{}, nil
		}
	}
	if p.eodMotions != 0 && (opcode == 0x92 || opcode == 0x2b) {
		p.tailLocates++
		if p.caseName == "failed first LOCATE" && p.tailLocates == 1 ||
			p.caseName == "failed restoration" && p.tailLocates == 2 {
			return tape.Response{}, errors.New("injected LOCATE failure")
		}
	}
	if opcode == 8 {
		p.readObjects = append(p.readObjects, p.cursor)
		if p.eodMotions != 0 {
			p.tailReads++
		}
	}
	response, err := base.Execute(ctx, command)

	// A completed SPACE may expose unknown counters, which refuse APPEND like any contradiction.
	if p.eodMotions != 0 && opcode == 0x34 {
		p.tailPositions++
		if p.tailPositions == 1 && p.caseName == "unknown EOD file" {
			command.Data[0] |= 8
		}
		if p.tailPositions == 1 && p.caseName == "medium changed" {
			sense := tarFixtureSense(6, 0, 0, 0)
			sense[12] = 0x28
			return tape.Response{Completed: true, Status: 2, Sense: sense}, nil
		}
		if p.tailPositions == 2 && p.caseName == "file ordinal contradiction" ||
			p.tailPositions == 3 && p.caseName == "restored ordinal contradiction" {
			binary.BigEndian.PutUint64(command.Data[16:24], 0)
		}
	}
	if p.eodMotions != 0 && opcode == 8 && p.tailReads == 2 && p.caseName == "data after final mark" {
		return tape.Response{Completed: true}, nil
	}
	return response, err
}

func tarAppendFixture(t *testing.T, mode entity.TapeRecordMode) (*Executor, *mediaBackend, *tarDeviceFixture) {
	t.Helper()
	// Use the existing writer/publication path to create a complete label and nontrivial archived prefix.
	exe, backend, fixture, _ := tarFactoryFixture(t)
	write, err := newTarFixtureWriteSession(backend.NewWriteSession(t.Context(), tarFormatTarget(mode)))
	require.NoError(t, err)
	tarWriteFixtureMember(t, write, "prefix", bytes.Repeat([]byte{0x4a}, 128<<10))
	result, err := write.Finalize(t.Context(), nil)
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(t.Context(), &library.Media{
		Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
	})
	require.NoError(t, err)
	return exe, backend, fixture
}

func TestTarAppendBoundedTailPublishesNextArchive(t *testing.T) {
	for _, mode := range []entity.TapeRecordMode{
		entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			// SPACE EOD and two boundary READs prove the tail without reading the archived prefix.
			exe, backend, fixture := tarAppendFixture(t, mode)
			provider := &tarAppendFixtureProvider{tarDeviceFixture: fixture, caseName: "bounded"}
			exe.tapeProvider = provider
			logger, hook := logtest.NewNullLogger()
			backend.logger = logger
			end := len(fixture.records)
			writes := fixture.writes
			session, err := newTarFixtureWriteSession(backend.NewWriteSession(t.Context(), (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack()))
			require.NoError(t, err)
			require.Equal(t, writes, fixture.writes, "tail proof cannot write")
			require.Equal(t, end, fixture.cursor, "prepared position must follow the original final mark")
			labelEnd := 0
			for !fixture.records[labelEnd].mark {
				labelEnd++
			}
			require.Equal(t, 1, provider.eodMotions)
			require.Equal(t, 2, provider.tailReads)
			require.Equal(t, 2, provider.tailLocates)
			for _, object := range provider.readObjects {
				if object < end-1 {
					require.LessOrEqual(t, object, labelEnd, "only bounded label records can precede the terminal proof")
				}
			}

			// Success is logged once with the real ordinal.
			verified := 0
			for _, entry := range hook.AllEntries() {
				if entry.Message == "tar APPEND tail verified" {
					verified++
					require.Equal(t, logrus.InfoLevel, entry.Level)
					require.EqualValues(t, 2, entry.Data["next_archive"])
					require.GreaterOrEqual(t, entry.Data["elapsed_ms"].(int64), int64(0))
				}
			}
			require.Equal(t, 1, verified)

			tarWriteFixtureMember(t, session, "after", []byte("new content"))
			result, err := session.Finalize(t.Context(), nil)
			require.NoError(t, err)
			var published []*mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(session, result, t.Context(), func(value *mediapkg.CompletedFile) error {
				published = append(published, value)
				return nil
			}))
			require.Len(t, published, 1)
			require.EqualValues(t, 2, published[0].Storage.Metadata.GetTar().TapeFile)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

func TestTarAppendBadTailNeverStartsWriter(t *testing.T) {
	for _, name := range []string{"failed SPACE", "unknown SPACE completion", "failed first LOCATE", "medium changed",
		"file ordinal contradiction", "unterminated last record", "data after final mark", "failed restoration",
		"restored ordinal contradiction", "unknown EOD file", "short position"} {
		t.Run(name, func(t *testing.T) {
			// A failed or incomplete proof must return before APPEND write configuration and Session creation.
			exe, backend, fixture := tarAppendFixture(t, entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
			fixture.shortPosition = name == "short position"
			provider := &tarAppendFixtureProvider{tarDeviceFixture: fixture, caseName: name}
			exe.tapeProvider = provider
			logger, hook := logtest.NewNullLogger()
			backend.logger = logger
			if name == "unterminated last record" {
				fixture.records = append(fixture.records, tarFixtureRecord{data: []byte("unclosed")})
			}
			writes, marks, records := fixture.writes, fixture.filemarks, len(fixture.records)
			session, err := backend.NewWriteSession(t.Context(), (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack())
			require.Error(t, err)
			require.Nil(t, session)
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, marks, fixture.filemarks)
			require.Len(t, fixture.records, records)
			require.Equal(t, fixture.opens, fixture.closes)
			require.Equal(t, map[bool]int{false: 1, true: 0}[name == "short position"], provider.eodMotions)
			for _, entry := range hook.AllEntries() {
				require.NotEqual(t, "tar APPEND tail verified", entry.Message, "failed proof must not log success")
			}

			// No post-failure rewind may conceal the motion/tail failure by scanning the old payload.
			eod := false
			for _, cdb := range provider.commands {
				if cdb[0] == 0x11 && cdb[1] == 3 {
					eod = true
				}
				if eod {
					require.NotEqualValues(t, 1, cdb[0], "bad proof must not rewind to scan from BOT")
					require.NotEqualValues(t, 0x0a, cdb[0], "bad proof must not issue WRITE")
				}
			}
		})
	}
}
