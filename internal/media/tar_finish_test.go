package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/hashicorp/go-multierror"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTarReadFinishRequiresSettledSuccessfulContent(t *testing.T) {
	for _, outcome := range []string{"normal", "canceled", "read-failure", "active-reader", "cleanup-fallback", "finish-failure"} {
		t.Run(outcome, func(t *testing.T) {
			// Produce real member framing and locators before testing the selected read lifecycle.
			device := &tarTestDevice{records: []tarTestRecord{{mark: true}}}
			options := tarTestOptions(t, false, 512)
			writer, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			payload := bytes.Repeat([]byte("content"), 1024)
			writeTarTestMember(t, writer, "file", payload, nil)
			result, err := writer.Finalize(context.Background(), nil)
			require.NoError(t, err)
			files := tarTestCompleted(t, writer, result)
			require.Len(t, files, 1)
			finishes, cleanups := 0, 0
			failure := errors.New("normal release failed")
			options.Finish = func(context.Context) error {
				finishes++
				if outcome == "finish-failure" {
					return failure
				}
				return nil
			}
			options.Cleanup = func(context.Context) error { cleanups++; return nil }
			if outcome == "cleanup-fallback" {
				options.Finish = nil
			}
			session, err := NewTarReadSession(device, options)
			require.NoError(t, err)
			ref, err := session.Source(ReadRequest{Path: files[0].Path, Storage: files[0].Storage})
			require.NoError(t, err)
			reader, err := ref.Content.Open(context.Background())
			require.NoError(t, err)
			if outcome == "read-failure" {
				device.readErrorAt = device.cursor + 1
			}
			if outcome != "active-reader" {
				content, err := io.ReadAll(reader)
				if outcome == "read-failure" {
					require.Error(t, err)
					require.Error(t, reader.Close())
				} else {
					require.NoError(t, err)
					require.Equal(t, payload, content)
					require.NoError(t, reader.Close())
				}
			}

			// A missing caller cause cannot conceal a stored read failure or an unsettled source.
			var cause error
			if outcome == "canceled" {
				cause = context.Canceled
			}
			err = session.Finalize(context.Background(), cause)
			if outcome == "finish-failure" {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
			if outcome == "normal" || outcome == "finish-failure" {
				require.Equal(t, 1, finishes)
				require.Zero(t, cleanups)
			} else {
				require.Zero(t, finishes)
				require.Equal(t, 1, cleanups)
			}
			require.Error(t, session.Finalize(context.Background(), nil))
			require.Equal(t, 1, finishes+cleanups)
			if outcome == "active-reader" {
				require.ErrorIs(t, reader.Close(), io.ErrUnexpectedEOF)
			}
		})
	}
}

func TestTarWriteFinishDistinguishesSafeWarningFromJoinedFailure(t *testing.T) {
	for _, extra := range []error{nil, context.Canceled, errors.New("independent failure")} {
		name := "safe-prefix"
		if extra != nil {
			name = extra.Error()
		}
		t.Run(name, func(t *testing.T) {
			// A physical EW during the next header retains one complete earlier member.
			device := &tarTestDevice{warningAt: 3}
			options := tarTestOptions(t, false, 512)
			finishes, cleanups := 0, 0
			options.Finish = func(context.Context) error {
				finishes++
				require.Equal(t, 1, device.marks, "the physical filemark must settle before unload")
				return nil
			}
			options.Cleanup = func(context.Context) error { cleanups++; return nil }
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "first", 1)
			ref, err := session.Target("later", 1)
			require.NoError(t, err)
			_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
			require.ErrorIs(t, cause, ErrTargetNoSpace)
			cause = fmt.Errorf("transfer stopped: %w", &multierror.Error{
				Errors: []error{cause, errors.Join(ErrCapacityBoundary, extra)},
			})

			// Only an exclusively capacity/EW error tree qualifies for normal completion.
			result, err := session.Finalize(context.Background(), cause)
			if extra == nil {
				require.NoError(t, err)
				require.Len(t, tarTestCompleted(t, session, result), 1)
				require.Equal(t, 1, finishes)
				require.Zero(t, cleanups)
			} else {
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrFinalizeUnusable)
				require.ErrorIs(t, err, extra)
				require.Zero(t, device.marks)
				require.Zero(t, finishes)
				require.Equal(t, 1, cleanups)
			}
			_, err = session.Finalize(context.Background(), nil)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.Equal(t, 1, finishes+cleanups)
		})
	}
}

func TestTarWriteFailedCompletionGateNeverFinishes(t *testing.T) {
	for _, gate := range []string{"filemark", "final-position"} {
		t.Run(gate, func(t *testing.T) {
			// A completed member alone cannot authorize unloading or usable publication.
			device := newTarWritePositionDevice()
			options := tarTestOptions(t, false, 512)
			options.Navigator = device
			failure := errors.New("injected completion gate failure")
			finishes, cleanups := 0, 0
			options.Finish = func(context.Context) error { finishes++; return nil }
			options.Cleanup = func(context.Context) error { cleanups++; return nil }
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "file", 1)
			switch gate {
			case "filemark":
				device.completionErr = failure
			case "final-position":
				device.observe = func(call int, position tape.Position) (tape.Position, error) {
					if call == 2 {
						return tape.Position{}, failure
					}
					return position, nil
				}
			}

			// Failed physical settlement uses cleanup exactly once and retains its failure.
			result, err := session.Finalize(context.Background(), nil)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.ErrorIs(t, err, failure)
			require.Zero(t, finishes)
			require.Equal(t, 1, cleanups)
			_, err = session.Finalize(context.Background(), nil)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.Equal(t, 1, cleanups)
		})
	}
}

func TestTarWriteNormalFinishRequiresSuccessfulRelease(t *testing.T) {
	for _, failure := range []error{nil, errors.New("normal release failed")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			// A physically complete archive still depends on successful normal device release.
			device := &tarTestDevice{}
			options := tarTestOptions(t, false, 512)
			finishes, cleanups := 0, 0
			options.Finish = func(context.Context) error { finishes++; return failure }
			options.Cleanup = func(context.Context) error { cleanups++; return nil }
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			writeTarPositionMember(t, session, "file", 1)
			result, err := session.Finalize(context.Background(), nil)
			if failure != nil {
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrFinalizeUnusable)
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
				require.Len(t, tarTestCompleted(t, session, result), 1)
			}
			require.Equal(t, 1, finishes)
			require.Zero(t, cleanups, "normal Finish already owns restoration and descriptor closure")
			_, err = session.Finalize(context.Background(), nil)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.Equal(t, 1, finishes)
			require.Zero(t, cleanups)
		})
	}
}

func TestTarStorageCutoffTracksOnlyCompletedMembers(t *testing.T) {
	for _, size := range []int{1, 3584} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			// Short content stays buffered; the aligned variant ends exactly at an accepted record.
			session, err := newTarTestWriteSession(&tarTestDevice{}, tarTestOptions(t, false, 4096))
			require.NoError(t, err)
			first, err := session.Target("first", int64(size))
			require.NoError(t, err)
			require.Nil(t, first.Storage())
			writer, err := first.Content.Open(context.Background(), acp.ContentMetadata{
				Info: tarWritePositionInfo{tarTestInfo{name: "first", size: int64(size)}},
			})
			require.NoError(t, err)
			_, err = writer.Write(make([]byte, size))
			require.NoError(t, err)
			require.NoError(t, writer.Finish(nil))
			staged := first.Storage()
			require.NotNil(t, staged)
			require.Zero(t, staged.Metadata.GetTar().HeaderOffsetBytes)
			state := session.tarWriteSession
			if size == 3584 {
				require.NotNil(t, state.acceptedHeader)
				require.Zero(t, *state.acceptedHeader)
			} else {
				require.Nil(t, state.acceptedHeader)
				require.NotNil(t, state.bufferedHeader)
			}

			// A later active member fills a record but cannot confirm itself before successful Finish.
			later, err := session.Target("later", 8192)
			require.NoError(t, err)
			active, err := later.Content.Open(context.Background(), acp.ContentMetadata{
				Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 8192}},
			})
			require.NoError(t, err)
			_, err = active.Write(make([]byte, 4096))
			require.NoError(t, err)
			require.NotNil(t, state.acceptedHeader)
			require.Zero(t, *state.acceptedHeader)
			failure := errors.New("source stopped")
			require.ErrorIs(t, active.Finish(failure), failure)
			require.Nil(t, later.Storage())

			// Earlier accepted offsets remain provisional when the following member is interrupted.
			result, err := session.Finalize(context.Background(), failure)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.ErrorIs(t, err, failure)
		})
	}
}
