package tape

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

func fixedSense(code, key, asc, ascq byte, valid bool, flags byte, information int32) []byte {
	data := make([]byte, 18)
	data[0], data[2], data[7], data[12], data[13] = code, key|flags, 10, asc, ascq
	if valid {
		data[0] |= 0x80
	}
	binary.BigEndian.PutUint32(data[3:7], uint32(information))
	return data
}

func TestRecordsPreserveAcceptedCountAndWarningEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response Response
		mode     RecordMode
		known    bool
		bytes    int
		err      error
	}{
		{"good", Response{Completed: true}, RecordMode{MaximumBytes: 512}, true, 512, nil},
		{"current warning", Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 0)}, RecordMode{MaximumBytes: 512, WarningCounts: true}, true, 512, ErrEarlyWarning},
		{"fixed warning", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 1)}, RecordMode{MaximumBytes: 512, FixedBytes: 512, WarningCounts: true}, true, 0, ErrEarlyWarning},
		{"variable zero warning", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 512)}, RecordMode{MaximumBytes: 512, WarningCounts: true}, true, 0, ErrEarlyWarning},
		{"fixed full warning", Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 0)}, RecordMode{MaximumBytes: 512, FixedBytes: 512, WarningCounts: true}, true, 512, ErrEarlyWarning},
		{"fixed count in bytes", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 512)}, RecordMode{MaximumBytes: 512, FixedBytes: 512, WarningCounts: true}, false, 0, ErrUncertain},
		{"warning residual mismatch", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 0)}, RecordMode{MaximumBytes: 512, WarningCounts: true}, false, 0, ErrUncertain},
		{"unqualified warning", Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 0)}, RecordMode{MaximumBytes: 512}, false, 0, ErrUncertain},
		{"deferred warning", Response{Completed: true, Status: 2, Sense: fixedSense(0x71, 0, 0, 2, true, 0x40, 0)}, RecordMode{MaximumBytes: 512, WarningCounts: true}, false, 0, ErrUncertain},
		{"warning without count", Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, false, 0x40, 0)}, RecordMode{MaximumBytes: 512, WarningCounts: true}, false, 0, ErrUncertain},
		{"good short transfer", Response{Completed: true, Residual: 1}, RecordMode{MaximumBytes: 512}, false, 0, ErrUncertain},
		{"clean rejected command", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, RecordMode{MaximumBytes: 512}, true, 0, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Host zero residual or a deferred warning alone cannot acknowledge a physical record.
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) { calls++; return tc.response, nil }, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: tc.mode, Timeout: time.Second}
			result, err := records.Write(context.Background(), make([]byte, 512))
			matches := errors.Is(err, tc.err)
			if tc.err == ErrUnsupported {
				matches = Reason(err).String() == "DEVICE_REASON_UNSUPPORTED"
			}
			if result.AcceptedKnown != tc.known || result.Bytes != tc.bytes || !matches {
				t.Fatalf("result=%+v, error=%v", result, err)
			}
			if errors.Is(err, ErrUncertain) {
				if _, nextErr := records.Write(context.Background(), make([]byte, 512)); !errors.Is(nextErr, ErrUncertain) || calls != 1 {
					t.Fatalf("uncertain lane reused: err=%v calls=%d", nextErr, calls)
				}
			}
		})
	}
}

func TestRecordsWriteCancellationPreservesAdmissionEvidence(t *testing.T) {
	// Pre-command cancellation is known zero acceptance; settled GOOD retains a full accepted record.
	for _, phase := range []string{"before cancel", "before deadline", "accepted cancel", "uncertain cancel"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if phase == "before deadline" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			defer cancel()
			calls := 0
			transport := newSerializedTransport(func(Command) (Response, error) {
				calls++
				cancel()
				response := Response{Completed: true}
				if phase == "uncertain cancel" {
					response.Residual = 1
				}
				return response, nil
			}, func() error { return nil })
			defer transport.Close()
			if phase == "before cancel" {
				cancel()
			}
			records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
			result, err := records.Write(ctx, make([]byte, 512))
			wantCause := context.Canceled
			if phase == "before deadline" {
				wantCause = context.DeadlineExceeded
			}
			if !errors.Is(err, wantCause) {
				t.Fatalf("cancellation missing: %v", err)
			}
			wantCalls, wantBytes := 0, 0
			if phase == "accepted cancel" || phase == "uncertain cancel" {
				wantCalls = 1
			}
			if phase == "accepted cancel" {
				wantBytes = 512
			}
			if result.AcceptedKnown != (phase != "uncertain cancel") || result.Bytes != wantBytes || calls != wantCalls {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
			completionErr := records.Filemarks(context.Background(), 1)
			if phase == "uncertain cancel" {
				if !errors.Is(err, ErrUncertain) || !errors.Is(completionErr, ErrUncertain) || calls != 1 {
					t.Fatalf("uncertain cancellation lane reused: write=%v completion=%v calls=%d", err, completionErr, calls)
				}
			} else if completionErr != nil || calls != wantCalls+1 {
				t.Fatalf("safe completion refused: %v calls=%d", completionErr, calls)
			}
		})
	}
}

func TestRecordsReadShortVariableAndBoundaryEvents(t *testing.T) {
	for _, tc := range []struct {
		name          string
		response      Response
		bytes         int
		filemark, eod bool
		err           error
	}{
		{"short", Response{Completed: true, Status: 2, Residual: 12, Sense: fixedSense(0x70, 0, 0, 0, true, 0x20, 12)}, 500, false, false, nil},
		{"overlong", Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 0, true, 0x20, -12)}, 0, false, false, io.ErrShortBuffer},
		{"filemark", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 0, 0, 1, true, 0x80, 512)}, 0, true, false, nil},
		{"EOD", Response{Completed: true, Status: 2, Residual: 512, Sense: fixedSense(0x70, 8, 0, 5, true, 0x40, 512)}, 0, false, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newSerializedTransport(func(Command) (Response, error) { return tc.response, nil }, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
			result, err := records.Read(context.Background(), make([]byte, 512))
			if result.Bytes != tc.bytes || result.Filemark != tc.filemark || result.EOD != tc.eod || !errors.Is(err, tc.err) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRecordCDBUnitsNavigationAndPhysicalCompletion(t *testing.T) {
	// One fixed record uses count 1; variable count is bytes, and sync/filemark use explicit counts.
	var commands [][]byte
	transport := newSerializedTransport(func(command Command) (Response, error) {
		commands = append(commands, append([]byte(nil), command.CDB...))
		return Response{Completed: true}, nil
	}, func() error { return nil })
	defer transport.Close()
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 1024}, Timeout: time.Second}
	if _, err := records.Write(context.Background(), make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	records.Mode.FixedBytes = 512
	if _, err := records.Write(context.Background(), make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	if err := records.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := records.Filemarks(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := records.Space(context.Background(), 1, -1); err != nil {
		t.Fatal(err)
	}
	if err := records.Locate(context.Background(), 2, 1<<40); err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{0x0a, 0, 0, 2, 0, 0}, {0x0a, 1, 0, 0, 1, 0}, {0x10, 0, 0, 0, 0, 0}, {0x10, 0, 0, 0, 1, 0}, {0x11, 1, 0xff, 0xff, 0xff, 0}, {0x92, 2, 0, 2, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands=%x, want=%x", commands, want)
	}

	// Invalid lengths/counts are refused before the transport can observe a command.
	for _, err := range []error{records.Filemarks(context.Background(), 1<<24), records.Space(context.Background(), 1, 1<<23)} {
		if err == nil {
			t.Fatal("overflowing command accepted")
		}
	}
	if _, err := records.Write(context.Background(), make([]byte, 511)); err == nil {
		t.Fatal("partial fixed record accepted")
	}
	if len(commands) != len(want) {
		t.Fatal("invalid request issued a command")
	}
}

func TestDescriptorSensePreservesFlagsAndSignedInformation(t *testing.T) {
	// A descriptor information field and stream descriptor carry distinct validity and boundary flags.
	data := make([]byte, 24)
	data[0], data[7], data[8], data[9], data[10] = 0x72, 16, 0, 10, 0x80
	binary.BigEndian.PutUint64(data[12:20], ^uint64(0))
	data[20], data[21], data[23] = 4, 2, 0xe0
	sense, err := DecodeSense(data)
	if err != nil || !sense.InformationValid || sense.Information != -1 || !sense.Filemark || !sense.EOM || !sense.ILI {
		t.Fatalf("sense=%+v error=%v", sense, err)
	}
	for _, malformed := range [][]byte{data[:23], {0x70}, {0x72, 0, 0, 0, 0, 0, 0, 1, 4}} {
		if _, err := DecodeSense(malformed); err == nil {
			t.Fatal("truncated sense accepted")
		}
	}
}

func TestFilemarkCurrentEWWithoutInformationCompletesAndKeepsLane(t *testing.T) {
	// Retain the real current-sense response from the failed non-immediate full-run filemark.
	sense, err := hex.DecodeString("700040000000001000000000000200009436000000000000")
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []uint32{0, 1} {
		t.Run(fmt.Sprintf("count=%d", count), func(t *testing.T) {
			// A synthetic transport exposes exactly the captured completion and no native device.
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				calls++
				if calls == 1 {
					if !reflect.DeepEqual(command.CDB, []byte{0x10, 0, 0, 0, byte(count), 0}) || command.Direction != NoData {
						t.Fatalf("filemark form changed: %x", command.CDB)
					}
					return Response{Completed: true, Status: 2, DriverStatus: 8, Sense: sense}, nil
				}
				if command.CDB[0] != 0x5a {
					t.Fatalf("unexpected cleanup command: %x", command.CDB)
				}
				return Response{Completed: true}, nil
			}, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: RecordMode{WarningCounts: true}, Timeout: time.Second}

			// SSC synchronous completion permits ordinary control verification despite absent residual information.
			if err := records.Filemarks(context.Background(), count); err != ErrEarlyWarning {
				t.Fatalf("completed warning refused: %v", err)
			}
			if _, err := transport.Execute(context.Background(), Command{CDB: []byte{0x5a, 0, 0x3f, 0, 0, 0, 0, 0x10, 0, 0},
				Direction: FromDevice, Data: make([]byte, 4096), Timeout: time.Second}); err != nil {
				t.Fatalf("settled warning blocked ordinary cleanup: %v", err)
			}
			if calls != 2 {
				t.Fatalf("control verification did not run exactly once: calls=%d", calls)
			}
			if err := transport.Close(); err != nil {
				t.Fatalf("known completion poisoned Close: %v", err)
			}
		})
	}
}

func TestFilemarkWarningRequiresProvenFullCompletion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		valid     bool
		remaining int32
		deferred  bool
		want      bool
	}{
		{"completed current warning", true, 0, false, true}, {"completed without count", false, 0, false, true},
		{"mark not completed", true, 1, false, false}, {"deferred", true, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Synchronous command completion admits EW; valid remaining counts must still be zero.
			code := byte(0x70)
			if tc.deferred {
				code = 0x71
			}
			calls := 0
			transport := newSerializedTransport(func(Command) (Response, error) {
				calls++
				return Response{Completed: true, Status: 2, Sense: fixedSense(code, 0, 0, 2, tc.valid, 0x40, tc.remaining)}, nil
			}, func() error { return nil })
			records := Records{Transport: transport, Mode: RecordMode{WarningCounts: true}, Timeout: time.Second}
			err := records.Filemarks(context.Background(), 1)
			if (err == ErrEarlyWarning) != tc.want {
				t.Fatalf("completion=%v", err)
			}

			// Unknown mark acceptance poisons the lane before a cleanup/control command can follow.
			if !tc.want {
				if err := records.Rewind(context.Background()); !errors.Is(err, ErrUncertain) || calls != 1 {
					t.Fatalf("unknown mark allowed command: %v", err)
				}
			}
			_ = transport.Close()
		})
	}
}

func TestFilemarkSynchronousWarningDoesNotReadInvalidInformation(t *testing.T) {
	for _, count := range []uint32{0, 1} {
		for _, tc := range []struct {
			name  string
			sense []byte
		}{
			{"invalid positive information", fixedSense(0x70, 0, 0, 2, false, 0x40, 27)},
			{"invalid negative information", fixedSense(0x70, 0, 0, 2, false, 0x40, -1)},
			{"descriptor without information", []byte{0x72, 0, 0, 2, 0, 0, 0, 4, 4, 2, 0, 0x40}},
			{"existing zero additional sense", fixedSense(0x70, 0, 0, 0, false, 0x40, 27)},
		} {
			t.Run(fmt.Sprintf("count=%d/%s", count, tc.name), func(t *testing.T) {
				// Invalid information carries no count: successful command semantics provide the proof.
				transport := newSerializedTransport(func(Command) (Response, error) {
					return Response{Completed: true, Status: 2, DriverStatus: 8, Sense: tc.sense}, nil
				}, func() error { return nil })
				defer transport.Close()
				records := Records{Transport: transport, Mode: RecordMode{WarningCounts: true}, Timeout: time.Second}
				if err := records.Filemarks(context.Background(), count); err != ErrEarlyWarning {
					t.Fatalf("invalid information interpreted as a remaining count: %v", err)
				}
			})
		}
	}
}

func TestFilemarkWarningRejectsContradictoryCompletionAndPoisonsLane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Response)
	}{
		{"unknown completion", func(r *Response) { r.Completed = false }},
		{"host failure", func(r *Response) { r.HostStatus = 1 }},
		{"message failure", func(r *Response) { r.MessageStatus = 1 }},
		{"driver failure", func(r *Response) { r.DriverStatus = 1 }},
		{"different status", func(r *Response) { r.Status = 8 }},
		{"deferred", func(r *Response) { r.Sense[0] = 0x71 }},
		{"filemark flag", func(r *Response) { r.Sense[2] |= 0x80 }},
		{"ILI flag", func(r *Response) { r.Sense[2] |= 0x20 }},
		{"missing EOM", func(r *Response) { r.Sense[2] = 0 }},
		{"volume overflow", func(r *Response) { r.Sense[2] |= 0x0d }},
		{"different sense key", func(r *Response) { r.Sense[2] |= 1 }},
		{"different ASC", func(r *Response) { r.Sense[12] = 1 }},
		{"different ASCQ", func(r *Response) { r.Sense[13] = 5 }},
		{"positive SG residual", func(r *Response) { r.Residual = 1 }},
		{"negative SG residual", func(r *Response) { r.Residual = -1 }},
		{"valid remaining mark", func(r *Response) { r.Sense = fixedSense(0x70, 0, 0, 2, true, 0x40, 1) }},
		{"valid negative information", func(r *Response) { r.Sense = fixedSense(0x70, 0, 0, 2, true, 0x40, -1) }},
	} {
		for _, count := range []uint32{0, 1} {
			t.Run(fmt.Sprintf("count=%d/%s", count, tc.name), func(t *testing.T) {
				// A coherent current NO SENSE warning is the baseline; each row contradicts one required fact.
				response := Response{Completed: true, Status: 2, DriverStatus: 8,
					Sense: fixedSense(0x70, 0, 0, 2, false, 0x40, 0)}
				tc.change(&response)
				calls := 0
				transport := newSerializedTransport(func(Command) (Response, error) { calls++; return response, nil },
					func() error { return nil })
				defer transport.Close()
				records := Records{Transport: transport, Mode: RecordMode{WarningCounts: true}, Timeout: time.Second}

				// Unknown mark completion poisons the same lane instead of authorizing cleanup by residual alone.
				if err := records.Filemarks(context.Background(), count); !errors.Is(err, ErrUncertain) {
					t.Fatalf("contradictory completion accepted: %v", err)
				}
				if err := records.Rewind(context.Background()); !errors.Is(err, ErrUncertain) || calls != 1 {
					t.Fatalf("uncertain lane reused: %v calls=%d", err, calls)
				}
			})
		}
	}
}

func TestFilemarkWarningKeepsQualificationAndCountBounds(t *testing.T) {
	for _, tc := range []struct {
		count     uint32
		qualified bool
	}{
		{0, false}, {1, false}, {2, true},
	} {
		t.Run(fmt.Sprintf("count=%d/qualified=%t", tc.count, tc.qualified), func(t *testing.T) {
			// Successful synchronous semantics do not widen the existing admitted command/count scope.
			transport := newSerializedTransport(func(Command) (Response, error) {
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, false, 0x40, 0)}, nil
			}, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: RecordMode{WarningCounts: tc.qualified}, Timeout: time.Second}
			if err := records.Filemarks(context.Background(), tc.count); !errors.Is(err, ErrUncertain) {
				t.Fatalf("unqualified completion accepted: %v", err)
			}
		})
	}
}

func TestFilemarkWarningIsCanonicalOnlyWithoutOtherFailure(t *testing.T) {
	for _, tc := range []struct {
		name               string
		count              uint32
		cancel             bool
		failure            error
		warning, uncertain bool
	}{
		{"synchronized count zero", 0, false, nil, true, false},
		{"completed count one", 1, false, nil, true, false},
		{"unqualified multiple marks", 2, false, nil, false, true},
		{"settled caller cancellation", 1, true, nil, false, false},
		{"mixed uncertain failure", 1, false, errors.Join(ErrEarlyWarning, ErrUncertain), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A canonical warning is a completion proof; extra transport/caller failures retain identity.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				calls++
				if command.CDB[0] != 0x10 || command.CDB[1] != 0 || command.CDB[4] != byte(tc.count) {
					t.Fatalf("completion form changed: %x", command.CDB)
				}
				if tc.cancel {
					cancel()
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 0)}, tc.failure
			}, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: RecordMode{WarningCounts: true}, Timeout: time.Second}
			err := records.Filemarks(ctx, tc.count)
			if (err == ErrEarlyWarning) != tc.warning || errors.Is(err, ErrUncertain) != tc.uncertain || calls != 1 {
				t.Fatalf("completion=%v warning=%t uncertain=%t calls=%d", err, tc.warning, tc.uncertain, calls)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) || tc.failure != nil && !errors.Is(err, tc.failure) {
				t.Fatalf("additional failure lost: %v", err)
			}
			if tc.uncertain {
				if err := records.Rewind(context.Background()); !errors.Is(err, ErrUncertain) || calls != 1 {
					t.Fatalf("unknown completion lane reused: %v calls=%d", err, calls)
				}
			}
		})
	}
}

func TestWriteWarningPreservesCountsAndRejectsMixedFailures(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, remaining := range []int64{0, 512} {
			t.Run(fmt.Sprintf("fixed=%t/remaining=%d", fixed, remaining), func(t *testing.T) {
				// Joined raw warning evidence retains fixed-record units and settled caller cancellation.
				mode := RecordMode{MaximumBytes: 512, WarningCounts: true}
				information := remaining
				if fixed {
					mode.FixedBytes = 512
					information /= 512
				}
				failure := &CommandError{Response: Response{Completed: true, Status: 2, Residual: int32(remaining)},
					Sense: Sense{EOM: true, ASCQ: 2, InformationValid: true, Information: information}}
				warning := errors.Join(ErrEarlyWarning, fmt.Errorf("WRITE failed, %w", failure), context.Canceled)
				if !WriteWarning(warning, remaining, mode) || WriteWarning(warning, remaining+1, mode) {
					t.Fatalf("inconsistent WRITE warning classification, remaining=%d fixed=%t", remaining, fixed)
				}

				// One incompatible cause invalidates the whole joined result without hiding its safe leaves.
				for _, cause := range []error{ErrUncertain, ErrMalformed, errors.New("hardware failure"), &UncertainError{Cause: failure}} {
					if WriteWarning(errors.Join(warning, cause), remaining, mode) {
						t.Fatalf("mixed warning accepted, cause=%v", cause)
					}
				}
				mode.WarningCounts = false
				if WriteWarning(warning, remaining, mode) {
					t.Fatal("unqualified raw warning accepted")
				}
			})
		}
	}
}

func TestWriteWarningRequiresCurrentCoherentCommandEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*CommandError)
	}{
		{"unknown completion", func(e *CommandError) { e.Response.Completed = false }},
		{"host failure", func(e *CommandError) { e.Response.HostStatus = 1 }},
		{"message failure", func(e *CommandError) { e.Response.MessageStatus = 1 }},
		{"driver failure", func(e *CommandError) { e.Response.DriverStatus = 1 }},
		{"deferred", func(e *CommandError) { e.Sense.Deferred = true }},
		{"missing information", func(e *CommandError) { e.Sense.InformationValid = false }},
		{"filemark", func(e *CommandError) { e.Sense.Filemark = true }},
		{"ILI", func(e *CommandError) { e.Sense.ILI = true }},
		{"different count", func(e *CommandError) { e.Sense.Information = 1 }},
		{"different residual", func(e *CommandError) { e.Response.Residual = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A parsed warning flag alone cannot override contradictory physical completion evidence.
			failure := &CommandError{Response: Response{Completed: true, Status: 2},
				Sense: Sense{EOM: true, ASCQ: 2, InformationValid: true}}
			test.change(failure)
			if WriteWarning(errors.Join(ErrEarlyWarning, failure), 0, RecordMode{MaximumBytes: 512, WarningCounts: true}) {
				t.Fatal("incoherent command accepted as a settled warning")
			}
		})
	}
}
