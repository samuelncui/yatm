package tape

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeviceNormalFinishRestoresBeforeOneSynchronousUnload(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(map[bool]string{true: "normal-finish", false: "cleanup-only"}[finish], func(t *testing.T) {
			// A prepared reader owns changed record controls; key management remains outside this Device.
			fixture := newDeviceFixture()
			original := bytes.Clone(fixture.mode)
			var commands []byte
			unloads, closes := 0, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				commands = append(commands, command.CDB[0])
				if command.CDB[0] == 0x1b {
					unloads++
					require.Equal(t, []byte{0x1b, 0, 0, 0, 0, 0}, command.CDB)
					require.Equal(t, NoData, command.Direction)
					require.Empty(t, command.Data)
					require.Equal(t, blockLength(original), blockLength(fixture.mode), "record controls must be restored before unload")
					return Response{Completed: true}, nil
				}
				return fixture.execute(command)
			}, func() error { commands = append(commands, 0xff); closes++; return nil })
			device, err := NewDevice(context.Background(), Identity{}, transport)
			require.NoError(t, err)
			_, err = device.Configure(context.Background(), encryptionTestProfile(), Read)
			require.NoError(t, err)
			commands = nil

			// The first release choice owns all later calls; cleanup alone can never request motion.
			if finish {
				require.NoError(t, device.Finish(context.Background()))
			} else {
				require.NoError(t, device.Cleanup(context.Background()))
			}
			require.Equal(t, blockLength(original), blockLength(fixture.mode))
			require.NotEqual(t, -1, bytes.IndexByte(commands, 0x55))

			require.NotContains(t, commands, byte(0xb5), "native cleanup never changes script keys")
			require.NotContains(t, commands, byte(1), "LOAD UNLOAD already performs its required rewind")
			require.Equal(t, map[bool]int{true: 1, false: 0}[finish], unloads)
			require.Equal(t, byte(0xff), commands[len(commands)-1])
			if finish {
				require.Equal(t, byte(0x1b), commands[len(commands)-2], "no medium-dependent query may follow unload")
			}
			before := bytes.Clone(commands)
			require.NoError(t, device.Finish(context.Background()))
			require.NoError(t, device.Cleanup(context.Background()))
			require.Equal(t, before, commands)
			require.Equal(t, 1, closes)
		})
	}
}

func TestDeviceFinishFailureStillClosesWithoutAnotherMotion(t *testing.T) {
	for _, failure := range []string{"mode-restore", "unload", "close"} {
		t.Run(failure, func(t *testing.T) {
			// Each injected failure occurs after ordinary preparation has acquired its controls.
			fixture := newDeviceFixture()
			unloadErr, closeErr := errors.New("unload failed"), errors.New("close failed")
			unloads, rewinds, closes := 0, 0, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 1 {
					rewinds++
				}
				if command.CDB[0] == 0x1b {
					unloads++
					if failure == "unload" {
						return Response{Completed: true}, unloadErr
					}
					return Response{Completed: true}, nil
				}
				return fixture.execute(command)
			}, func() error {
				closes++
				if failure == "close" {
					return closeErr
				}
				return nil
			})
			device, err := NewDevice(context.Background(), Identity{}, transport)
			require.NoError(t, err)
			_, err = device.Configure(context.Background(), encryptionTestProfile(), Read)
			require.NoError(t, err)
			fixture.failureCleanup = failure == "mode-restore"

			// Failed restoration suppresses unload; failed unload is never retried after descriptor closure.
			err = device.Finish(context.Background())
			require.Error(t, err)
			if failure == "close" {
				require.ErrorIs(t, err, closeErr)
			} else {
				require.ErrorIs(t, err, ErrUncertain)
			}
			if failure == "unload" {
				require.ErrorIs(t, err, unloadErr)
			}
			wantUnloads := 1
			if failure == "mode-restore" {
				wantUnloads = 0
			}
			require.Equal(t, wantUnloads, unloads)
			require.Zero(t, rewinds)
			require.Equal(t, 1, closes)
			require.Equal(t, err, device.Cleanup(context.Background()))
			require.Equal(t, err, device.Finish(context.Background()))
			require.Equal(t, wantUnloads, unloads)
			require.Equal(t, 1, closes)
		})
	}
}

func TestDeviceFinishWaitsForUnloadBeforeClosing(t *testing.T) {
	// The native synchronous command retains the opened transport until physical settlement.
	issued, settle, finished := make(chan Command, 1), make(chan struct{}), make(chan error, 1)
	closes := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-settle:
		default:
			close(settle)
		}
	})
	transport := newSerializedTransport(func(command Command) (Response, error) {
		issued <- command
		<-settle
		return Response{Completed: true}, nil
	}, func() error { closes <- struct{}{}; return nil })
	device := &Device{transport: transport, records: Records{Transport: transport, Timeout: time.Minute}}
	go func() { finished <- device.Finish(context.Background()) }()
	var command Command
	select {
	case command = <-issued:
	case err := <-finished:
		t.Fatalf("Finish returned without issuing unload: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Finish did not issue unload")
	}
	require.Equal(t, []byte{0x1b, 0, 0, 0, 0, 0}, command.CDB)
	select {
	case <-finished:
		t.Fatal("Finish returned before synchronous unload settled")
	case <-closes:
		t.Fatal("transport closed while unload was active")
	default:
	}

	// Settlement permits exactly one close; raw transport Close itself does not submit motion.
	close(settle)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Finish did not return after unload settled")
	}
	<-closes
	require.NoError(t, transport.Close())
	select {
	case <-issued:
		t.Fatal("Close issued a Tape command")
	case <-closes:
		t.Fatal("transport closed twice")
	default:
	}
}
