package tape

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestCapacityUsesCurrentModePartitionWithoutLogicalSeek(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		// Current Device Configuration page10 supplies partition independently of RP service support/exactness.
		queries, modes := 0, 0
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x5a && command.CDB[2] != 0x7f {
				modes++
				if command.CDB[2] != 0x3f {
					t.Fatal("partition inferred from default/changeable mode values")
				}
			}
			if command.CDB[0] == 0x34 && unsupported {
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
			}
			response, err := inspectionFixture(command)
			if command.CDB[0] == 0x34 {
				command.Data[0] |= 4
				binary.BigEndian.PutUint32(command.Data[4:8], 99) // LONU makes this reported address undefined.
			}
			if command.CDB[0] == 0x4d {
				queries++
			}
			return response, err
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING).GetCapacity()
		if err != nil || report.Reason != 0 || queries != 1 || modes != 1 || limits == nil || limits.Partition == nil || limits.GetPartition() != 0 || limits.GetRemainingBytes() != 100_000_000 {
			t.Fatalf("unsupported=%t report=%v err=%v queries=%d modes=%d", unsupported, report, err, queries, modes)
		}
	}
}

func TestCapacityPartitionMissingOrContradictoryNeverBecomesAValue(t *testing.T) {
	for _, contradiction := range []bool{false, true} {
		// No active-partition source means unknown; disagreement between two exact sources cannot select one.
		queries := 0
		transport := newSerializedTransport(func(command Command) (Response, error) {
			response, err := inspectionFixture(command)
			if command.CDB[0] == 0x5a {
				if contradiction {
					command.Data[43] = 1
				} else {
					binary.BigEndian.PutUint16(command.Data[:2], 38) // The valid envelope omits page10 entirely.
				}
			}
			if command.CDB[0] == 0x34 && !contradiction {
				command.Data[0] |= 4
			}
			if command.CDB[0] == 0x4d {
				queries++
			}
			return response, err
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
		want := entity.DeviceReason_DEVICE_REASON_NOT_READY
		if contradiction {
			want = entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE
		}
		if err != nil || report.Reason != 0 || queries != 0 || f.GetCapacity() != nil || f.Reason != want {
			t.Fatalf("contradiction=%t feature=%v overall=%v err=%v queries=%d", contradiction, f, report.Reason, err, queries)
		}
	}
}

func TestCapacityModePartitionLosesApplicabilityAfterReadinessLoss(t *testing.T) {
	for _, noMedium := range []bool{false, true} {
		// Later passive readiness rejection invalidates the previously successful current mode association.
		queries := 0
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0xa2 {
				asc := byte(4)
				if noMedium {
					asc = 0x3a
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, asc, 0, false, 0, 0)}, nil
			}
			if command.CDB[0] == 0x4d {
				queries++
			}
			return inspectionFixture(command)
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
		want := entity.DeviceReason_DEVICE_REASON_NOT_READY
		if noMedium {
			want = entity.DeviceReason_DEVICE_REASON_NO_MEDIUM
		}
		if err != nil || report.Reason != want || queries != 0 || f.GetCapacity() != nil || f.Reason != want || report.GetTape().GetMediumPresent() == noMedium {
			t.Fatalf("no-medium=%t feature=%v overall=%v err=%v queries=%d", noMedium, f, report.Reason, err, queries)
		}
	}
}

func TestCapacityCrossChecksSelectedPositionForm(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		mode, initial, latest bool
		partition             byte
		queries               int
		reason                entity.DeviceReason
	}{
		{"mode and initial conflict with latest", true, true, true, 1, 0, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE},
		{"position forms conflict without mode", false, true, true, 1, 0, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE},
		{"latest exact position supplies missing partition", false, false, true, 7, 1, 0},
		{"unknown latest partition is ignored", true, true, false, 99, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// RSOC selects short position after the initial long observation, without issuing any motion.
			queries, longQueries, shortQueries := 0, 0, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 0xa3 && command.CDB[3] == 0x34 && command.CDB[5] == 6 {
					command.Data[1] = 1
					return Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
				}
				response, err := inspectionFixture(command)
				if command.CDB[0] == 0x5a && !tc.mode {
					binary.BigEndian.PutUint16(command.Data[:2], 38)
				}
				if command.CDB[0] == 0x34 {
					if command.CDB[1] == 6 {
						longQueries++
						if !tc.initial {
							command.Data[0] |= 4
						}
					} else {
						shortQueries++
						command.Data[1] = tc.partition
						if !tc.latest {
							command.Data[0] |= 4
						}
					}
				}
				if command.CDB[0] == 0x4d {
					queries++
				}
				return response, err
			}, func() error { return nil })
			defer transport.Close()

			// Every exact source must agree; an unknown latest address cannot invent or invalidate a partition.
			report, err := inspectTransport(context.Background(), Identity{}, transport)
			f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
			if err != nil || report.Reason != 0 || queries != tc.queries || longQueries != 1 || shortQueries != 1 || f.Reason != tc.reason {
				t.Fatalf("feature=%v overall=%v err=%v capacity=%d long=%d short=%d", f, report.Reason, err, queries, longQueries, shortQueries)
			}
			if tc.queries == 0 {
				if f.GetCapacity() != nil {
					t.Fatalf("conflicting partitions produced capacity: %v", f)
				}
				return
			}
			partition := uint32(0)
			if tc.latest {
				partition = uint32(tc.partition)
			}
			limits := f.GetCapacity()
			if limits == nil || limits.Partition == nil || limits.GetPartition() != partition || limits.GetRemainingBytes() != 100_000_000 {
				t.Fatalf("exact partition association missing: %v", f)
			}
		})
	}
}
