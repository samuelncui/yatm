package tape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func capacityTestTransport(execute func(Command) (Response, error), open func() (*serializedTransport, error), close func() error) *sgTransport {
	t := &sgTransport{limit: MaxQueryBytes, serializedTransport: newSerializedTransport(execute, close)}
	t.capacity = newCapacityTransport(t.serializedTransport, open)
	return t
}

func capacityTestResponse(command Command) (Response, error) {
	data := capacityPage(1, 2)
	copy(command.Data, data)
	return Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
}

func capacityTestWrite() Command {
	return Command{CDB: []byte{0x0a, 0, 0, 0, 1, 0}, Direction: ToDevice, Data: []byte{7}, Timeout: time.Second}
}

func capacityTestWait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("native lifetime did not finish")
		return nil
	}
}

func TestPrivateCapacityNativeQueryDoesNotHoldWriteLane(t *testing.T) {
	// Hold the query at the native abstraction, with a distinct header/buffer owner from WRITE.
	entered, release := make(chan struct{}), make(chan struct{})
	var queryIssues, writes atomic.Int32
	query := &sgTransport{limit: MaxQueryBytes}
	query.command = func(header *sgHeader) (bool, error) {
		queryIssues.Add(1)
		require.Equal(t, byte(0x4d), *header.Command)
		close(entered)
		<-release
		data := capacityPage(1, 2)
		copy(unsafe.Slice(header.Data, int(header.DataLength)), data)
		header.Residual = int32(int(header.DataLength) - len(data))
		return false, nil
	}
	query.serializedTransport = newSerializedTransport(query.executeCommand, func() error { return nil })
	primary := &sgTransport{limit: MaxQueryBytes}
	primary.command = func(header *sgHeader) (bool, error) {
		require.Equal(t, byte(0x0a), *header.Command)
		require.Equal(t, []byte{7}, unsafe.Slice(header.Data, int(header.DataLength)))
		writes.Add(1)
		return false, nil
	}
	primary.serializedTransport = newSerializedTransport(primary.executeCommand, func() error { return nil })
	primary.capacity = newCapacityTransport(primary.serializedTransport, func() (*serializedTransport, error) { return query.serializedTransport, nil })
	provider := &Capacity{transport: primary}
	finished := make(chan error, 1)
	go func() {
		sample, issued, err := provider.Observe(context.Background(), nil)
		if err == nil && (!issued || sample == nil || *sample.RemainingBytes != 1_000_000) {
			err = fmt.Errorf("missing query sample")
		}
		finished <- err
	}()
	<-entered

	// The original optional route is still visible, but only its own busy query lane can skip.
	_, issued, err := provider.Observe(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, issued)
	writeDone := make(chan error, 1)
	go func() { _, err := primary.Execute(context.Background(), capacityTestWrite()); writeDone <- err }()
	require.NoError(t, capacityTestWait(t, writeDone))
	require.Equal(t, int32(1), writes.Load())
	select {
	case <-finished:
		t.Fatal("query settled before explicit native release")
	default:
	}
	close(release)
	require.NoError(t, capacityTestWait(t, finished))
	require.NoError(t, primary.Close())
	require.Equal(t, int32(1), queryIssues.Load())
}

type capacityTestDeadlineContext struct{ context.Context }

func (c capacityTestDeadlineContext) Err() error {
	if c.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

func TestPrivateCapacityGoodCompletionAfterDeadlineKeepsWriteAvailable(t *testing.T) {
	// A local deadline expires while the synthetic native query still owns its buffers.
	entered, release := make(chan struct{}), make(chan struct{})
	base, cancel := context.WithCancel(context.Background())
	ctx := capacityTestDeadlineContext{base}
	defer cancel()
	writes := 0
	transport := capacityTestTransport(func(Command) (Response, error) {
		writes++
		return Response{Completed: true}, nil
	}, func() (*serializedTransport, error) {
		return newSerializedTransport(func(command Command) (Response, error) {
			require.Equal(t, 60*time.Second, command.Timeout, "sample deadline must not shrink native settlement")
			close(entered)
			<-release
			return capacityTestResponse(command)
		}, func() error { return nil }), nil
	}, func() error { return nil })
	done := make(chan error, 1)
	go func() {
		sample, issued, err := (&Capacity{transport: transport}).Observe(ctx, nil)
		if sample != nil || !issued {
			err = errors.Join(err, errors.New("late query must discard its sample after native settlement"))
		}
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("query returned before controlled deadline, err=%v", err)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("query did not reach synthetic native submission")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("deadline returned before native settlement")
	default:
	}

	// GOOD native settlement plus a local deadline is not an unknown native timeout or reset.
	close(release)
	err := capacityTestWait(t, done)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrUncertain)
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.NoError(t, err)
	require.Equal(t, 1, writes)
	require.NoError(t, transport.Close())
}

func TestPrivateCapacityFatalAllowsOnlyOnePregrantedWrite(t *testing.T) {
	// Freeze the writer after its shared grant; its native issue may follow the query's fatal receipt.
	granted, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	failure := errors.New("deferred query failure")
	transport := capacityTestTransport(func(Command) (Response, error) {
		writes.Add(1)
		return Response{Completed: true}, nil
	}, func() (*serializedTransport, error) {
		return newSerializedTransport(func(Command) (Response, error) { return Response{Completed: true}, failure }, func() error { return nil }), nil
	}, func() error { return nil })
	writeDone := make(chan error, 1)
	go func() {
		_, err := transport.ExecuteObserved(context.Background(), capacityTestWrite(), func() error {
			close(granted)
			<-release
			return nil
		})
		writeDone <- err
	}()
	<-granted
	_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), nil)
	require.True(t, issued)
	require.ErrorIs(t, err, failure)
	close(release)
	require.NoError(t, capacityTestWait(t, writeDone), "the pregranted WRITE must retain its actual result")

	// Every later primary/query grant fails before callback/native exposure; settled cleanup can succeed.
	callbacks := 0
	_, err = transport.ExecuteObserved(context.Background(), capacityTestWrite(), func() error { callbacks++; return nil })
	require.ErrorIs(t, err, failure)
	_, issued, err = transport.TryExecute(context.Background(), capacityCommand(), func() error { callbacks++; return nil })
	require.False(t, issued)
	require.ErrorIs(t, err, failure)
	require.Zero(t, callbacks)
	require.Equal(t, int32(1), writes.Load())
	require.NoError(t, transport.Close())
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.Error(t, err)
}

func TestPrivateCapacityCompletionClassificationBeforeNextGrant(t *testing.T) {
	for _, tc := range []struct {
		name              string
		run               func(Command) (Response, error)
		optional, healthy bool
	}{
		{"known unsupported", func(Command) (Response, error) {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
		}, true, false},
		{"invalid native residual", func(Command) (Response, error) {
			return Response{Completed: true, Residual: -1}, nil
		}, false, false},
		{"malformed native sense", func(Command) (Response, error) {
			return Response{Completed: true, Status: 2, Sense: []byte{0x70}}, nil
		}, false, false},
		{"deferred", func(Command) (Response, error) {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x71, 3, 0x11, 0, false, 0, 0)}, nil
		}, false, false},
		{"no medium", func(Command) (Response, error) {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, nil
		}, false, false},
		{"generic", func(Command) (Response, error) { return Response{Completed: true}, syscall.EIO }, false, false},
		{"joined unsupported fatal", func(Command) (Response, error) {
			return Response{Completed: true}, errors.Join(ErrUnsupported, syscall.EIO)
		}, false, false},
		{"missing optional fields", func(command Command) (Response, error) {
			copy(command.Data, []byte{0x0c, 0, 0, 0})
			return Response{Completed: true, Residual: MaxQueryBytes - 4}, nil
		}, false, true},
		{"unknown counter", func(command Command) (Response, error) {
			data := capacityPage(^uint32(0), 2)
			copy(command.Data, data)
			return Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Decode at transport completion, before Observe's later payload interpretation can run.
			writes, queries := 0, 0
			transport := capacityTestTransport(func(Command) (Response, error) { writes++; return Response{Completed: true}, nil }, func() (*serializedTransport, error) {
				return newSerializedTransport(func(c Command) (Response, error) { queries++; return tc.run(c) }, func() error { return nil }), nil
			}, func() error { return nil })
			_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), nil)
			require.True(t, issued)
			require.Equal(t, tc.optional, CleanUnsupported(err))
			if tc.healthy {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			_, writeErr := transport.Execute(context.Background(), capacityTestWrite())
			if tc.optional || tc.healthy {
				require.NoError(t, writeErr)
				require.Equal(t, 1, writes)
				require.NoError(t, transport.Close())
			} else {
				require.Error(t, writeErr)
				require.Zero(t, writes)
				closeErr := transport.Close()
				if errors.Is(err, ErrUncertain) {
					require.ErrorIs(t, closeErr, ErrUncertain)
				} else {
					require.NoError(t, closeErr, "known settled attempt failure is not a cleanup failure")
				}
			}
			require.Equal(t, 1, queries)
		})
	}
}

func TestPrivateCapacityAttachmentAbsenceAndCleanupFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		optional bool
	}{
		{"permission", fmt.Errorf("open: %w", syscall.EACCES), true},
		{"denied", syscall.EPERM, true},
		{"unsupported", fmt.Errorf("attach: %w", ErrUnsupported), true},
		{"missing", syscall.ENOENT, false},
		{"generic", syscall.EIO, false},
		{"cleanup joined", errors.Join(syscall.EACCES, syscall.EIO), false},
		{"cleanup uncertain", &UncertainError{Cause: errors.Join(ErrUnsupported, syscall.EIO)}, false},
		{"resource cleanup joined", errors.Join(syscall.EMFILE, syscall.EIO), false},
		{"resource joined", errors.Join(syscall.EMFILE), false},
		{"resource wrapped cleanup", fmt.Errorf("attach: %w", errors.Join(syscall.ENFILE, syscall.EIO)), false},
		{"resource uncertain", &UncertainError{Cause: syscall.ENOMEM}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No attachment failure retries or issues a query; only known clean absence leaves WRITE available.
			opens, writes, callbacks := 0, 0, 0
			transport := capacityTestTransport(func(Command) (Response, error) { writes++; return Response{Completed: true}, nil }, func() (*serializedTransport, error) { opens++; return nil, tc.err }, func() error { return nil })
			for i := 0; i < 2; i++ {
				_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), func() error { callbacks++; return nil })
				require.False(t, issued)
				require.Equal(t, tc.optional, CleanUnsupported(err))
			}
			_, err := transport.Execute(context.Background(), capacityTestWrite())
			if tc.optional {
				require.NoError(t, err)
				require.Equal(t, 1, writes)
			} else {
				require.ErrorIs(t, err, tc.err)
				require.Zero(t, writes)
			}
			require.Equal(t, 1, opens)
			require.Zero(t, callbacks)
			if errors.Is(tc.err, ErrUncertain) {
				require.ErrorIs(t, transport.Close(), ErrUncertain)
			} else {
				require.NoError(t, transport.Close())
			}
		})
	}
}

func TestPrivateCapacityAttachmentResourceMissesKeepWriteAndRetry(t *testing.T) {
	for _, cause := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOMEM} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", cause, wrapped), func(t *testing.T) {
				// Inject clean local allocation failures before any query command or issue callback.
				failure := cause
				if wrapped {
					failure = fmt.Errorf("attach: %w", fmt.Errorf("allocate: %w", cause))
				}
				opens, writes, queries, callbacks, primaryClosed, queryClosed := 0, 0, 0, 0, 0, 0
				transport := capacityTestTransport(func(Command) (Response, error) {
					writes++
					return Response{Completed: true}, nil
				}, func() (*serializedTransport, error) {
					opens++
					if opens <= 2 {
						return nil, failure
					}
					return newSerializedTransport(func(command Command) (Response, error) {
						queries++
						return capacityTestResponse(command)
					}, func() error { queryClosed++; return nil }), nil
				}, func() error { primaryClosed++; return nil })
				t.Cleanup(func() { _ = transport.Close() })
				provider := &Capacity{transport: transport}
				for i := 0; i < 2; i++ {
					sample, issued, err := provider.Observe(context.Background(), func() error { callbacks++; return nil })
					require.ErrorIs(t, err, cause)
					require.False(t, CleanUnsupported(err))
					require.Nil(t, sample)
					require.False(t, issued)
					require.Zero(t, callbacks)
					require.Zero(t, queries)
					_, err = transport.Execute(context.Background(), capacityTestWrite())
					require.NoError(t, err)
				}

				// The next ordinary poll attaches successfully without reopening the healthy primary lane.
				sample, issued, err := provider.Observe(context.Background(), func() error { callbacks++; return nil })
				require.NoError(t, err)
				require.True(t, issued)
				require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
				require.Equal(t, 3, opens)
				require.Equal(t, 2, writes)
				require.Equal(t, 1, queries)
				require.Equal(t, 1, callbacks)
				require.NoError(t, transport.Close())
				require.NoError(t, transport.Close())
				require.Equal(t, 1, primaryClosed)
				require.Equal(t, 1, queryClosed)
			})
		}
	}
}

func TestPrivateCapacityCloseJoinsAttachmentAndCanceledNative(t *testing.T) {
	for _, attaching := range []bool{false, true} {
		t.Run(fmt.Sprintf("attachment=%t", attaching), func(t *testing.T) {
			// Parent Close must join open/native even after cancellation; neither native owner closes early.
			entered, release := make(chan struct{}), make(chan struct{})
			var queryClosed, primaryClosed atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			transport := capacityTestTransport(func(Command) (Response, error) { return Response{Completed: true}, nil }, func() (*serializedTransport, error) {
				if attaching {
					close(entered)
					<-release
				}
				return newSerializedTransport(func(command Command) (Response, error) {
					if !attaching {
						close(entered)
						<-release
					}
					return capacityTestResponse(command)
				}, func() error { queryClosed.Add(1); return nil }), nil
			}, func() error { primaryClosed.Add(1); return nil })
			queryDone := make(chan error, 1)
			go func() { _, _, err := transport.TryExecute(ctx, capacityCommand(), nil); queryDone <- err }()
			<-entered
			cancel()
			closeDone := make(chan error, 1)
			go func() { closeDone <- transport.Close() }()
			select {
			case <-closeDone:
				t.Fatal("Close overtook active query lifetime")
			case <-time.After(20 * time.Millisecond):
			}
			require.Zero(t, queryClosed.Load())
			require.Zero(t, primaryClosed.Load())

			// Closing joins once, remembers closed admission, and cancellation does not poison healthy settlement.
			close(release)
			require.ErrorIs(t, capacityTestWait(t, queryDone), context.Canceled)
			require.NoError(t, capacityTestWait(t, closeDone))
			require.NoError(t, transport.Close())
			require.Equal(t, int32(1), queryClosed.Load())
			require.Equal(t, int32(1), primaryClosed.Load())
			_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), nil)
			require.False(t, issued)
			require.ErrorIs(t, err, io.ErrClosedPipe)
		})
	}
}

func TestPrivateCapacityIssueAbortAndCloseErrors(t *testing.T) {
	// A known unissued callback abort does not poison admission; final cleanup failures remain terminal.
	issues := 0
	cleanup := errors.New("query close failed")
	transport := capacityTestTransport(func(Command) (Response, error) { return Response{Completed: true}, nil }, func() (*serializedTransport, error) {
		return newSerializedTransport(func(command Command) (Response, error) { issues++; return capacityTestResponse(command) }, func() error { return cleanup }), nil
	}, func() error { return syscall.EIO })
	abort := errors.New("window closed")
	_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), func() error { return abort })
	require.False(t, issued)
	require.ErrorIs(t, err, abort)
	require.Zero(t, issues)
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.NoError(t, err)
	_, issued, err = transport.TryExecute(context.Background(), capacityCommand(), nil)
	require.True(t, issued)
	require.NoError(t, err)
	require.Equal(t, 1, issues)
	err = transport.Close()
	require.ErrorIs(t, err, cleanup)
	require.ErrorIs(t, err, syscall.EIO)
	require.ErrorIs(t, err, ErrUncertain)
	require.ErrorIs(t, transport.Close(), cleanup)
}

func TestPrivateCapacityFactoryPreparationDoesNotOpenAttachment(t *testing.T) {
	// Inspect includes the same LOG SENSE as preparation, followed by Configure mutations and label work.
	fixture := newDeviceFixture()
	opens := 0
	transport := capacityTestTransport(fixture.execute, func() (*serializedTransport, error) {
		opens++
		return newSerializedTransport(capacityTestResponse, func() error { return nil }), nil
	}, func() error { fixture.closed++; return nil })
	device, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
	require.NoError(t, err)
	_, err = device.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
	require.NoError(t, err)
	require.NoError(t, device.Initialize(context.Background()))
	provider, err := device.Capacity(context.Background())
	require.NoError(t, err)
	require.Zero(t, opens, "Initial-only/Off must not attach a recurring query descriptor")

	// Only the live optional route opens once; prior identical CDBs and controls cannot disable it.
	for i := 0; i < 2; i++ {
		sample, issued, err := provider.Observe(context.Background(), nil)
		require.NoError(t, err)
		require.True(t, issued)
		require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
	}
	require.Equal(t, 1, opens)
	require.NoError(t, device.Cleanup(context.Background()))
	require.Equal(t, 1, fixture.closed)
}

func TestPrivateCapacityPrimaryFailureClosesQueryAdmission(t *testing.T) {
	for _, failure := range []Response{
		{},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 4, 0, false, 0, 0)},
	} {
		// A primary uncertainty/readiness failure closes lazy query admission before any attachment.
		opens := 0
		transport := capacityTestTransport(func(Command) (Response, error) { return failure, nil }, func() (*serializedTransport, error) { opens++; return nil, ErrUnsupported }, func() error { return nil })
		_, err := transport.Execute(context.Background(), capacityTestWrite())
		require.Error(t, err)
		_, issued, err := transport.TryExecute(context.Background(), capacityCommand(), nil)
		require.Error(t, err)
		require.False(t, issued)
		require.Zero(t, opens)
		if errors.Is(err, ErrUncertain) {
			require.ErrorIs(t, transport.Close(), ErrUncertain)
		} else {
			require.NoError(t, transport.Close())
		}
	}
}

func TestPrivateCapacityAllowlistDoesNotExposeOtherCommands(t *testing.T) {
	for _, change := range []func(*Command){
		func(c *Command) { c.CDB[2] = 0x4d },
		func(c *Command) { c.CDB[5] = 1 },
		func(c *Command) { c.Direction = ToDevice },
		func(c *Command) { c.Data = c.Data[:MaxQueryBytes-1] },
		func(c *Command) { c.Timeout = capacityObservationTimeout + time.Second },
	} {
		// The private native owner has one exact software-passive form, not the broad Inspect allowlist.
		opens := 0
		primary := newSerializedTransport(capacityTestResponse, func() error { return nil })
		owner := newCapacityTransport(primary, func() (*serializedTransport, error) { opens++; return nil, ErrUnsupported })
		command := capacityCommand()
		change(&command)
		_, issued, err := owner.tryExecute(context.Background(), command, nil)
		require.Error(t, err)
		require.False(t, issued)
		require.Zero(t, opens)
		require.NoError(t, owner.Close())
		require.NoError(t, primary.Close())
	}
}

func TestPrivateCapacityRetainsAttachmentCleanupFailureAfterPrimaryFailure(t *testing.T) {
	// Lazy open is pregranted before a primary fatal; its later failed cleanup cannot be hidden by that first cause.
	entered, release := make(chan struct{}), make(chan struct{})
	primaryFailure := errors.New("known primary rejection")
	cleanupFailure := errors.New("attachment cleanup failed")
	transport := capacityTestTransport(func(Command) (Response, error) { return Response{Completed: true}, primaryFailure }, func() (*serializedTransport, error) {
		close(entered)
		<-release
		return nil, &UncertainError{Cause: cleanupFailure}
	}, func() error { return nil })
	queryDone := make(chan error, 1)
	go func() {
		_, _, err := transport.TryExecute(context.Background(), capacityCommand(), nil)
		queryDone <- err
	}()
	<-entered
	_, err := transport.Execute(context.Background(), capacityTestWrite())
	require.ErrorIs(t, err, primaryFailure)
	close(release)

	// Both actual causes survive into Close; uncertainty retains resource unavailability despite a known first failure.
	err = capacityTestWait(t, queryDone)
	require.ErrorIs(t, err, primaryFailure)
	require.ErrorIs(t, err, cleanupFailure)
	err = transport.Close()
	require.ErrorIs(t, err, ErrUncertain)
	require.ErrorIs(t, err, cleanupFailure)
}
