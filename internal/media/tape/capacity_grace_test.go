package tape

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCapacityQueriesUseLTFSNativeBound(t *testing.T) {
	// Preparation, passive inspection and recurring observation use the same LOG SENSE bound.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var timeouts []time.Duration
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] != 0x4d {
			return inspectionFixture(command)
		}
		timeouts = append(timeouts, command.Timeout)
		return capacityTestResponse(command)
	}, func() error { return nil })
	defer func() { require.NoError(t, transport.Close()) }()
	_, err := inspectTransport(ctx, Identity{}, transport)
	require.NoError(t, err)
	sample, issued, err := preparationCapacity(ctx, transport, 0)
	require.NoError(t, err)
	require.True(t, issued)
	require.NotNil(t, sample)
	sample, issued, err = (&Capacity{transport: transport}).Observe(ctx, nil)
	require.NoError(t, err)
	require.True(t, issued)
	require.NotNil(t, sample)
	require.Equal(t, []time.Duration{60 * time.Second, 60 * time.Second, 60 * time.Second}, timeouts)
}

func TestPrivateCapacityMalformedPayloadKeepsWriteAndRecoveryAvailable(t *testing.T) {
	// A GOOD settled native response with undecodable counters cannot invalidate an independent WRITE.
	writes, queries, opens := 0, 0, 0
	transport := capacityTestTransport(func(Command) (Response, error) {
		writes++
		return Response{Completed: true}, nil
	}, func() (*serializedTransport, error) {
		opens++
		return newSerializedTransport(func(command Command) (Response, error) {
			queries++
			if queries == 1 {
				return Response{Completed: true}, nil // Valid native envelope, invalid all-zero page.
			}
			return capacityTestResponse(command)
		}, func() error { return nil }), nil
	}, func() error { return nil })
	provider := &Capacity{transport: transport}
	sample, issued, err := provider.Observe(context.Background(), nil)
	require.True(t, issued)
	require.Nil(t, sample)
	require.ErrorIs(t, err, ErrMalformed)
	require.NotErrorIs(t, err, ErrUncertain)
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.NoError(t, err)

	// A later usable observation can recover on the same attachment; no local miss is published as a value.
	sample, issued, err = provider.Observe(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, issued)
	require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
	require.Equal(t, 1, writes)
	require.Equal(t, 2, queries)
	require.Equal(t, 1, opens)
	require.NoError(t, transport.Close())
}

func TestCapacitySubmissionRejectionOnlyWithdrawsQuery(t *testing.T) {
	for _, failure := range []error{syscall.EINVAL, syscall.ENOTTY, syscall.EACCES, syscall.EPERM,
		fmt.Errorf("submit SG header failed, %w", syscall.EINVAL), fmt.Errorf("submit SG header failed, %w", syscall.ENOTTY),
		fmt.Errorf("submit SG header failed, %w", syscall.EACCES), fmt.Errorf("submit SG header failed, %w", syscall.EPERM)} {
		t.Run(failure.Error(), func(t *testing.T) {
			for _, recurring := range []bool{false, true} {
				// Both preparation and the private lane may discover unavailable optional native commands.
				queries, writes := 0, 0
				native := &sgTransport{limit: MaxQueryBytes}
				native.command = func(header *sgHeader) (bool, error) {
					if *header.Command == 0x0a {
						writes++
						return false, nil
					}
					queries++
					return false, failure
				}
				native.serializedTransport = newSerializedTransport(native.executeCommand, func() error { return nil })
				transport := native
				if recurring {
					transport = capacityTestTransport(func(Command) (Response, error) {
						writes++
						return Response{Completed: true}, nil
					}, func() (*serializedTransport, error) { return native.serializedTransport, nil }, func() error { return nil })
				} else {
					native.capacity = newCapacityTransport(native.serializedTransport, func() (*serializedTransport, error) {
						t.Fatal("preparation unexpectedly opened another descriptor")
						return nil, nil
					})
				}

				var err error
				if recurring {
					_, _, err = (&Capacity{transport: transport}).Observe(context.Background(), nil)
				} else {
					_, _, err = preparationCapacity(context.Background(), transport, 0)
				}
				require.True(t, CleanUnsupported(err))
				require.Contains(t, err.Error(), failure.Error())
				_, err = transport.Execute(context.Background(), capacityTestWrite())
				require.NoError(t, err)
				require.Equal(t, 1, writes)
				if recurring {
					_, issued, err := (&Capacity{transport: transport}).Observe(context.Background(), nil)
					require.False(t, issued)
					require.True(t, CleanUnsupported(err))
				}
				require.Equal(t, 1, queries)
				require.NoError(t, transport.Close())
			}
		})
	}
}

func TestCapacitySubmissionRejectionCannotHideJoinedFailure(t *testing.T) {
	// A permission rejection joined with another device/cleanup failure remains attempt-wide.
	writes := 0
	query := &sgTransport{limit: MaxQueryBytes}
	query.command = func(*sgHeader) (bool, error) {
		return false, fmt.Errorf("submit: %w", errors.Join(syscall.EACCES, syscall.EIO))
	}
	query.serializedTransport = newSerializedTransport(query.executeCommand, func() error { return nil })
	transport := capacityTestTransport(func(Command) (Response, error) {
		writes++
		return Response{Completed: true}, nil
	}, func() (*serializedTransport, error) { return query.serializedTransport, nil }, func() error { return nil })
	_, _, err := (&Capacity{transport: transport}).Observe(context.Background(), nil)
	require.ErrorIs(t, err, syscall.EIO)
	require.False(t, CleanUnsupported(err))
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.ErrorIs(t, err, syscall.EIO)
	require.Zero(t, writes)
	require.NoError(t, transport.Close())
}

func TestPrivateCapacitySettledFailureKeepsWriteAndQueryAvailable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		host, driver uint16
		status       byte
		want         error
	}{
		{"host timeout", 3, 0, 0, context.DeadlineExceeded},
		{"driver timeout", 0, 6, 0, context.DeadlineExceeded},
		{"host and driver timeout", 3, 6, 0, context.DeadlineExceeded},
		{"host busy", 2, 0, 0, syscall.EBUSY},
		{"driver busy", 0, 1, 0, syscall.EBUSY},
		{"host and driver busy", 2, 1, 0, syscall.EBUSY},
		{"SCSI busy", 0, 0, 0x08, syscall.EBUSY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The generic checker stays strict; only the exact capacity command owns this exception.
			require.Error(t, CheckResponse(Response{Completed: true, Status: tc.status, HostStatus: tc.host, DriverStatus: tc.driver}))

			// A matched read-only rejection discards the sample without poisoning the shared attempt.
			queries, writes, opens, closes := 0, 0, 0, 0
			query := &sgTransport{limit: MaxQueryBytes}
			query.command = func(header *sgHeader) (bool, error) {
				queries++
				require.Equal(t, uint32(60_000), header.Timeout)
				if queries == 1 {
					header.HostStatus, header.DriverStatus = tc.host, tc.driver
					header.Status = tc.status
					return false, nil
				}
				data := capacityPage(1, 2)
				copy(unsafe.Slice(header.Data, int(header.DataLength)), data)
				header.Residual = int32(int(header.DataLength) - len(data))
				return false, nil
			}
			query.serializedTransport = newSerializedTransport(query.executeCommand, func() error { closes++; return nil })
			transport := capacityTestTransport(func(Command) (Response, error) { writes++; return Response{Completed: true}, nil },
				func() (*serializedTransport, error) { opens++; return query.serializedTransport, nil }, func() error { return nil })
			provider := &Capacity{transport: transport}
			sample, issued, err := provider.Observe(context.Background(), nil)
			require.True(t, issued)
			require.Nil(t, sample)
			require.ErrorIs(t, err, tc.want)
			require.NotErrorIs(t, err, ErrUncertain)

			// Writing and a later successful query retain the same attachment, without command replay.
			_, err = transport.Execute(context.Background(), capacityTestWrite())
			require.NoError(t, err)
			sample, issued, err = provider.Observe(context.Background(), nil)
			require.NoError(t, err)
			require.True(t, issued)
			require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
			require.Equal(t, 1, writes)
			require.Equal(t, 2, queries)
			require.Equal(t, 1, opens)
			require.NoError(t, transport.Close())
			require.Equal(t, 1, closes)
		})
	}
}

func TestPreparationCapacitySettledFailureKeepsWriteAvailable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		host, driver uint16
		status       byte
		want         error
	}{
		{"host timeout", 3, 0, 0, context.DeadlineExceeded},
		{"driver timeout", 0, 6, 0, context.DeadlineExceeded},
		{"host busy", 2, 0, 0, syscall.EBUSY},
		{"driver busy", 0, 1, 0, syscall.EBUSY},
		{"SCSI busy", 0, 0, 0x08, syscall.EBUSY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Preparation shares the WRITE lane and must retain it after a clean counter-query rejection.
			queries, writes, closes := 0, 0, 0
			transport := &sgTransport{limit: MaxQueryBytes}
			transport.command = func(header *sgHeader) (bool, error) {
				if *header.Command == 0x0a {
					writes++
					return false, nil
				}
				queries++
				require.Equal(t, uint32(60_000), header.Timeout)
				if queries == 1 {
					header.HostStatus, header.DriverStatus, header.Status = tc.host, tc.driver, tc.status
					return false, nil
				}
				data := capacityPage(1, 2)
				copy(unsafe.Slice(header.Data, int(header.DataLength)), data)
				header.Residual = int32(int(header.DataLength) - len(data))
				return false, nil
			}
			transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error { closes++; return nil })
			transport.capacity = newCapacityTransport(transport.serializedTransport, func() (*serializedTransport, error) {
				t.Error("preparation opened the recurring query attachment")
				return nil, ErrUnsupported
			})
			sample, issued, err := preparationCapacity(context.Background(), transport, 0)
			require.True(t, issued)
			require.Nil(t, sample)
			require.ErrorIs(t, err, tc.want)
			require.NotErrorIs(t, err, ErrUncertain)

			// The same primary native owner accepts WRITE and a later successful query without replay.
			_, err = transport.Execute(context.Background(), capacityTestWrite())
			require.NoError(t, err)
			sample, issued, err = preparationCapacity(context.Background(), transport, 0)
			require.NoError(t, err)
			require.True(t, issued)
			require.NotNil(t, sample)
			require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
			require.Equal(t, 1, writes)
			require.Equal(t, 2, queries)
			require.NoError(t, transport.Close())
			require.Equal(t, 1, closes)
		})
	}
}

func TestCapacityInspectionSettledTimeoutKeepsBaselineAvailable(t *testing.T) {
	// A settled optional timeout retains the independently observed drive and medium facts.
	queries, writes, closes := 0, 0, 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x0a {
			writes++
			return Response{Completed: true}, nil
		}
		if command.CDB[0] == 0x4d {
			queries++
			if queries == 1 {
				return Response{Completed: true, HostStatus: 3}, nil
			}
			return capacityTestResponse(command)
		}
		return inspectionFixture(command)
	}, func() error { closes++; return nil })
	report, err := inspectTransport(context.Background(), Identity{}, transport)
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, report.Reason)
	require.True(t, report.GetTape().GetMediumPresent())
	f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
	require.Nil(t, f.GetCapacity())
	require.Equal(t, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_UNSPECIFIED, f.Evidence)
	require.Contains(t, f.Detail, context.DeadlineExceeded.Error())

	// The same inspected owner can WRITE and later observe capacity without reopening or replaying.
	_, err = transport.Execute(context.Background(), capacityTestWrite())
	require.NoError(t, err)
	sample, issued, err := preparationCapacity(context.Background(), transport, 0)
	require.NoError(t, err)
	require.True(t, issued)
	require.NotNil(t, sample)
	require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
	require.Equal(t, 1, writes)
	require.Equal(t, 2, queries)
	require.NoError(t, transport.Close())
	require.Equal(t, 1, closes)
}

func TestCapacityInspectionWaitsForNativeLifetimeAndRespectsCaller(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		for _, cancelCaller := range []bool{false, true} {
			t.Run(fmt.Sprintf("timeout=%t/caller_deadline=%t", timeout, cancelCaller), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					// An optional query may consume its full native timeout; only the caller can cancel inspection.
					ctx := context.Background()
					if cancelCaller {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
						defer cancel()
					}
					queries, closes := 0, 0
					transport := newSerializedTransport(func(command Command) (Response, error) {
						if command.CDB[0] != 0x4d {
							return inspectionFixture(command)
						}
						queries++
						require.Equal(t, 60*time.Second, command.Timeout)
						time.Sleep(61 * time.Second) // Virtual time includes native error handling.
						if timeout {
							return Response{Completed: true, HostStatus: 3}, nil
						}
						return capacityTestResponse(command)
					}, func() error { closes++; return nil })
					device, err := NewDevice(ctx, Identity{}, transport)
					require.Equal(t, 1, queries)
					if cancelCaller {
						require.ErrorIs(t, err, context.DeadlineExceeded)
						require.Nil(t, device)
						require.Equal(t, 1, closes)
						return
					}
					require.NoError(t, err)
					require.NotNil(t, device)
					require.Zero(t, device.Report().Reason)
					require.True(t, device.Report().GetTape().GetMediumPresent())
					require.NoError(t, transport.Close())
					require.Equal(t, 1, closes)
				})
			})
		}
	}
}

func TestPrivateCapacityTimeoutCannotHideOtherNativeFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sgHeader)
		err    error
	}{
		{"reset", func(h *sgHeader) { h.HostStatus = 8 }, nil},
		{"check condition", func(h *sgHeader) { h.Status = 2 }, nil},
		{"deferred sense", func(h *sgHeader) {
			h.Status = 2
			sense := fixedSense(0x71, 3, 0x11, 0, false, 0, 0)
			copy(unsafe.Slice(h.Sense, int(h.SenseLength)), sense)
			h.SenseWritten = uint8(len(sense))
		}, nil},
		{"medium loss", func(h *sgHeader) {
			h.Status = 2
			sense := fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)
			copy(unsafe.Slice(h.Sense, int(h.SenseLength)), sense)
			h.SenseWritten = uint8(len(sense))
		}, nil},
		{"negative residual", func(h *sgHeader) { h.Residual = -1 }, nil},
		{"excess residual", func(h *sgHeader) { h.Residual = int32(h.DataLength) + 1 }, nil},
		{"message status", func(h *sgHeader) { h.MessageStatus = 1 }, nil},
		{"driver suggestion", func(h *sgHeader) { h.DriverStatus = 0x16 }, nil},
		{"native info", func(h *sgHeader) { h.Info = 2 }, nil},
		{"joined transport failure", func(*sgHeader) {}, errors.Join(context.DeadlineExceeded, syscall.EIO)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A timeout must not mask independently fatal native or transport evidence.
			queries, writes := 0, 0
			query := &sgTransport{limit: MaxQueryBytes}
			query.command = func(header *sgHeader) (bool, error) {
				queries++
				header.HostStatus = 3
				tc.change(header)
				return false, tc.err
			}
			query.serializedTransport = newSerializedTransport(query.executeCommand, func() error { return nil })
			transport := capacityTestTransport(func(Command) (Response, error) { writes++; return Response{Completed: true}, nil },
				func() (*serializedTransport, error) { return query.serializedTransport, nil }, func() error { return nil })
			sample, issued, err := (&Capacity{transport: transport}).Observe(context.Background(), nil)
			require.True(t, issued)
			require.Nil(t, sample)
			require.ErrorIs(t, err, ErrUncertain)
			if tc.err != nil {
				require.ErrorIs(t, err, syscall.EIO)
			}

			// Shared admission remains closed after the failed sample; no native command is replayed.
			_, err = transport.Execute(context.Background(), capacityTestWrite())
			require.ErrorIs(t, err, ErrUncertain)
			_, issued, err = (&Capacity{transport: transport}).Observe(context.Background(), nil)
			require.False(t, issued)
			require.ErrorIs(t, err, ErrUncertain)
			require.Equal(t, 1, queries)
			require.Zero(t, writes)
			require.ErrorIs(t, transport.Close(), ErrUncertain)
		})
	}
}

func TestSettledTimeoutRemainsFatalOutsideExactCapacityQuery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command Command
	}{
		{"WRITE", capacityTestWrite()},
		{"other LOG SENSE page", func() Command { c := capacityCommand(); c.CDB[2] = 0x71; return c }()},
		{"other LOG SENSE subpage", func() Command { c := capacityCommand(); c.CDB[3] = 1; return c }()},
		{"different transfer size", func() Command { c := capacityCommand(); c.Data = make([]byte, 16); return c }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Optional timeout policy never weakens WRITE or another command's completion contract.
			calls := 0
			transport := &sgTransport{limit: MaxQueryBytes}
			transport.command = func(header *sgHeader) (bool, error) {
				calls++
				header.HostStatus = 3
				return false, nil
			}
			transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error { return nil })
			response, err := transport.Execute(context.Background(), tc.command)
			require.True(t, response.Completed)
			require.ErrorIs(t, err, ErrUncertain)

			// The generic lane keeps the failure and refuses a subsequent command before submission.
			_, err = transport.Execute(context.Background(), capacityTestWrite())
			require.ErrorIs(t, err, ErrUncertain)
			require.Equal(t, 1, calls)
			require.ErrorIs(t, transport.Close(), ErrUncertain)
		})
	}
}
