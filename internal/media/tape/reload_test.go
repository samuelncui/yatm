package tape

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

type reloadFixture struct {
	*geometryFixture
	held           bool
	forms          int
	commands       [][]byte
	form           []byte
	formError      error
	heldResponse   *Response
	motionResponse *Response
	failLoad       bool
	foreignKey     bool
	noSerial       bool
	noFirmware     bool
	noSerialQuery  bool
	onMotion       func(bool)
	onQuery        func(Command)
}

func newReloadFixture() *reloadFixture {
	return &reloadFixture{geometryFixture: newGeometryFixture()}
}

func (f *reloadFixture) execute(command Command) (Response, error) {
	// Record native issues separately from the session's admission checks and completion handling.
	f.commands = append(f.commands, append([]byte(nil), command.CDB...))
	if f.onQuery != nil {
		f.onQuery(command)
	}
	if command.CDB[0] == 0xa3 && command.CDB[1] == 0x0c && command.CDB[3] == 0x1b {
		f.forms++
		if f.formError != nil {
			return Response{}, f.formError
		}
		if f.form != nil {
			copy(command.Data, f.form)
			return Response{Completed: true, Residual: int32(len(command.Data) - len(f.form))}, nil
		}
	}

	// Exact synchronous HOLD unthreads; only a successful LOAD changes the fixture back to ready.
	if command.CDB[0] == 0x12 && command.CDB[1] == 1 && f.noSerialQuery {
		return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, nil
	}
	if command.CDB[0] == 0x1b {
		load := command.CDB[4] == 1
		if !reflect.DeepEqual(command.CDB, reloadCommand(load)) || command.Direction != NoData {
			return Response{}, ErrMalformed
		}
		if f.onMotion != nil {
			f.onMotion(load)
		}
		if load == f.failLoad && f.motionResponse != nil {
			return *f.motionResponse, nil
		}
		f.held = !load
		return Response{Completed: true}, nil
	}
	if command.CDB[0] == 0 && f.held {
		if f.heldResponse != nil {
			return *f.heldResponse, nil
		}
		return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 4, false, 0, 0)}, nil
	}

	// Preserve the ordinary observation fixture while allowing a foreign nexus/key refusal.
	response, err := f.geometryFixture.execute(command)
	if command.CDB[0] == 0x12 && f.noSerial && command.CDB[1] == 1 {
		clear(command.Data[2:])
		response.Residual = int32(len(command.Data) - 4)
	}
	if command.CDB[0] == 0x12 && f.noFirmware && command.CDB[1] == 0 {
		copy(command.Data[32:36], "    ")
	}
	if f.foreignKey && command.CDB[0] == 0xa2 && command.CDB[1] == 0x20 && command.CDB[3] == 0x20 {
		command.Data[4] = 1
	}
	return response, err
}

func reloadDevice(t *testing.T, f *reloadFixture) *Device {
	t.Helper()
	d, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, newSerializedTransport(f.execute, func() error { f.closed++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func reloadMotions(f *reloadFixture) [][]byte {
	var motions [][]byte
	for _, cdb := range f.commands {
		if cdb[0] == 0x1b {
			motions = append(motions, cdb)
		}
	}
	return motions
}

func TestReloadCapturesNewObservationAndKeepsFreshnessUnknown(t *testing.T) {
	for _, vendor := range []string{"IBM", "HP", "HPE"} {
		t.Run(vendor, func(t *testing.T) {
			// Passive eligibility and synthetic command completion never establish hardware validation.
			f := newReloadFixture()
			f.vendor = vendor
			d := reloadDevice(t, f)
			before, _ := d.Geometry(context.Background())
			beforeNavigation := d.navigation
			capability := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD)
			if capability.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || capability.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY || capability.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED || f.forms != 1 {
				t.Fatalf("capability=%v forms=%d", capability, f.forms)
			}
			f.onMotion = func(load bool) {
				if before.Valid() || d.navigation != nil {
					t.Fatal("old observations survived motion admission")
				}
				if load {
					f.data = wrapData(WrapEndpoint{Wrap: 9, Object: 456})
				}
			}

			// The sequence must contain a held discriminator between one HOLD and one real LOAD.
			start := len(f.commands)
			if err := d.RefreshPositionMap(context.Background()); err != nil {
				t.Fatal(err)
			}
			motions := reloadMotions(f)
			if !reflect.DeepEqual(motions, [][]byte{reloadCommand(false), reloadCommand(true)}) {
				t.Fatalf("motions=%v", motions)
			}
			for i, cdb := range f.commands[start:] {
				if cdb[0] == 0x1b && cdb[4] == 8 && (f.commands[start+i+1][0] != 0 || !reflect.DeepEqual(f.commands[start+i+2], reloadCommand(true))) {
					t.Fatal("HOLD was not immediately followed by the held check and LOAD")
				}
			}
			after, err := d.Geometry(context.Background())
			if err != nil || before.Valid() || !after.Valid() || after.Fresh != nil || len(after.Endpoints) != 1 || after.Endpoints[0].Object != 456 || d.navigation == beforeNavigation || f.wrapQueries != 2 || f.forms != 2 || f.selects != 0 || f.writes != 0 {
				t.Fatalf("before=%v after=%+v err=%v forms=%d wraps=%d", before.Valid(), after, err, f.forms, f.wrapQueries)
			}

			// Cleanup withdraws the replacement and closes exactly once, without control/key mutation.
			if err := d.Cleanup(context.Background()); err != nil || after.Valid() {
				t.Fatalf("cleanup=%v valid=%t", err, after.Valid())
			}
			if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 || f.selects != 0 || f.writes != 0 {
				t.Fatalf("cleanup=%v closed=%d", err, f.closed)
			}
		})
	}
}

func TestReloadRefusesMissingPrerequisitesBeforeMotion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*reloadFixture)
		alter func(*Device)
	}{
		{name: "HOLD mask absent", setup: func(f *reloadFixture) { f.form = []byte{0, 3, 0, 6, 0x1b, 0, 0, 0, 1, 0} }},
		{name: "LOAD mask absent", setup: func(f *reloadFixture) { f.form = []byte{0, 3, 0, 6, 0x1b, 0, 0, 0, 8, 0} }},
		{name: "unknown command support", setup: func(f *reloadFixture) { f.form = []byte{0, 0} }},
		{name: "unsupported command", setup: func(f *reloadFixture) { f.unsupported = true }},
		{name: "unscoped family", setup: func(f *reloadFixture) { f.vendor = "OTHER" }},
		{name: "unscoped HP family", setup: func(f *reloadFixture) { f.vendor, f.model = "HP", "DAT160" }},
		{name: "unready", setup: func(f *reloadFixture) { f.notReady = true }},
		{name: "short map", setup: func(f *reloadFixture) { f.shortOnly = true }},
		{name: "empty map", setup: func(f *reloadFixture) { f.data = wrapData() }},
		{name: "foreign key during inspection", setup: func(f *reloadFixture) { f.foreignKey = true }},
		{name: "foreign key after inspection", alter: func(d *Device) {}},
		{name: "failed validation", alter: func(d *Device) {
			feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD).Validation = entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED
		}},
		{name: "configured", alter: func(d *Device) { d.operation = Read }},
		{name: "captured controls", alter: func(d *Device) { d.original = []byte{1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Baseline inspection may retain optional diagnostics, but no absent prerequisite authorizes motion.
			f := newReloadFixture()
			if tc.setup != nil {
				tc.setup(f)
			}
			d := reloadDevice(t, f)
			if tc.alter != nil {
				tc.alter(d)
			}
			if tc.name == "foreign key after inspection" {
				f.foreignKey = true
			}
			if err := d.RefreshPositionMap(context.Background()); err == nil || len(reloadMotions(f)) != 0 || f.selects != 0 || f.writes != 0 {
				t.Fatalf("refresh=%v motions=%v", err, reloadMotions(f))
			}
			for _, cdb := range f.commands {
				if cdb[0] == 0xb5 {
					t.Fatal("reload changed encryption state")
				}
			}

			// These never-issued refreshes retain ordinary sole Close and do not clear a foreign key.
			d.original = nil
			if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 {
				t.Fatalf("cleanup=%v closed=%d", err, f.closed)
			}
		})
	}
}

func TestReloadFailureNeverRetriesOrLoadsAfterUnconfirmedHold(t *testing.T) {
	for _, load := range []bool{false, true} {
		for _, uncertain := range []bool{false, true} {
			t.Run(map[bool]string{false: "HOLD", true: "LOAD"}[load]+map[bool]string{false: " known", true: " uncertain"}[uncertain], func(t *testing.T) {
				// Known rejected commands retain clean cleanup; missing native completion preserves transport poison.
				f := newReloadFixture()
				d := reloadDevice(t, f)
				before, _ := d.Geometry(context.Background())
				f.failLoad = load
				if uncertain {
					f.motionResponse = &Response{Completed: true, HostStatus: 3}
				} else {
					f.motionResponse = &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}
				}
				err := d.RefreshPositionMap(context.Background())
				wantMotions := 1
				if load {
					wantMotions = 2
				}
				if err == nil || errors.Is(err, ErrUncertain) != uncertain || len(reloadMotions(f)) != wantMotions || before.Valid() || d.navigation != nil || f.wrapQueries != 1 {
					t.Fatalf("refresh=%v motions=%v", err, reloadMotions(f))
				}

				// Cleanup performs no recovery LOAD, retry, key command, rewind, eject or filemark.
				if err := d.Cleanup(context.Background()); errors.Is(err, ErrUncertain) != uncertain || f.closed != 1 || len(reloadMotions(f)) != wantMotions {
					t.Fatalf("cleanup=%v closed=%d motions=%v", err, f.closed, reloadMotions(f))
				}
			})
		}
	}
}

func TestReloadHeldDiscriminatorRequiresCurrentNotReady3A04(t *testing.T) {
	for _, response := range []Response{
		{Completed: true},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 4, 1, false, 0, 0)},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x3a, 4, false, 0, 0)},
	} {
		// A GOOD no-op or any other readiness code cannot authorize the LOAD step.
		f := newReloadFixture()
		d := reloadDevice(t, f)
		f.heldResponse = &response
		if err := d.RefreshPositionMap(context.Background()); err == nil || len(reloadMotions(f)) != 1 {
			t.Fatalf("held=%v refresh=%v motions=%v", response, err, reloadMotions(f))
		}
		if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 {
			t.Fatalf("cleanup=%v closed=%d", err, f.closed)
		}
	}
}

func TestReloadCancellationStopsNewWorkAfterIssuedCommandSettles(t *testing.T) {
	for _, stage := range []string{"before", "HOLD", "held check", "LOAD"} {
		t.Run(stage, func(t *testing.T) {
			// Cancellation is injected inside native execution so that issued work must return before cleanup.
			f := newReloadFixture()
			d := reloadDevice(t, f)
			before, _ := d.Geometry(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before" {
				cancel()
			}
			f.onMotion = func(load bool) {
				if stage == "HOLD" && !load || stage == "LOAD" && load {
					cancel()
				}
			}
			f.onQuery = func(command Command) {
				if stage == "held check" && command.CDB[0] == 0 && f.held {
					cancel()
				}
			}
			err := d.RefreshPositionMap(ctx)
			want := 1
			if stage == "before" {
				want = 0
			} else if stage == "LOAD" {
				want = 2
			}
			if !errors.Is(err, context.Canceled) || len(reloadMotions(f)) != want || before.Valid() != (stage == "before") || f.wrapQueries != 1 {
				t.Fatalf("refresh=%v motions=%v old=%t", err, reloadMotions(f), before.Valid())
			}

			// Known settled cancellation does not manufacture uncertain cleanup or an automatic LOAD.
			if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 || len(reloadMotions(f)) != want {
				t.Fatalf("cleanup=%v closed=%d", err, f.closed)
			}
		})
	}
}

func TestReloadPostLoadObservationFailuresWithdrawReplacement(t *testing.T) {
	for _, change := range []string{"identity", "unready", "query failure", "malformed", "truncation", "navigation", "foreign key"} {
		t.Run(change, func(t *testing.T) {
			// The post-load path reobserves actual facts instead of promoting stale map/readiness evidence.
			f := newReloadFixture()
			d := reloadDevice(t, f)
			before, _ := d.Geometry(context.Background())
			f.onMotion = func(load bool) {
				if !load {
					return
				}
				switch change {
				case "identity":
					f.model = "Ultrium changed"
				case "unready":
					f.notReady = true
				case "query failure":
					f.response = &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}
				case "malformed":
					f.data = []byte{0, 3, 0, 0, 0}
				case "truncation":
					f.data = make([]byte, MaxQueryBytes)
					f.data[0], f.data[1] = 0x10, 0x0a
				case "navigation":
					f.deviceFixture.unsupported = 0x34
				case "foreign key":
					f.foreignKey = true
				}
			}
			err := d.RefreshPositionMap(context.Background())
			after, geometryErr := d.Geometry(context.Background())
			if err == nil || geometryErr != nil || after != nil || before.Valid() || d.navigation != nil || len(reloadMotions(f)) != 2 {
				t.Fatalf("refresh=%v geometry=%v old=%t motions=%v", err, after, before.Valid(), reloadMotions(f))
			}

			// Failed required observation stays a clean known failure and never causes a second cycle.
			if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 || f.selects != 0 || f.writes != 0 || len(reloadMotions(f)) != 2 {
				t.Fatalf("cleanup=%v closed=%d motions=%v", err, f.closed, reloadMotions(f))
			}
		})
	}
}

func TestReloadOptionalDiscoveryFailurePreservesBaseline(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		// Optional reporting failure remains local to reload and does not revoke ordinary sequential access.
		f := newReloadFixture()
		if malformed {
			f.form = []byte{0, 3}
		} else {
			f.formError = ErrUnsupported
		}
		d := reloadDevice(t, f)
		if d.Report().Reason != 0 || len(reloadMotions(f)) != 0 {
			t.Fatalf("report=%v motions=%v", d.Report(), reloadMotions(f))
		}
		if _, err := d.Configure(context.Background(), &entity.TarMediaProfile{
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512,
		}, Read); err != nil {
			t.Fatal(err)
		}

		// Baseline preparation and cleanup never consume the unavailable reload capability.
		if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 || len(reloadMotions(f)) != 0 {
			t.Fatalf("cleanup=%v closed=%d motions=%v", err, f.closed, reloadMotions(f))
		}
	}
}

func TestReloadOptionalDriveIdentityMayRemainAbsent(t *testing.T) {
	for _, absent := range []string{"serial", "firmware", "serial query unsupported"} {
		// Initial absence remains unknown; only an already known identity field must survive the cycle.
		f := newReloadFixture()
		f.noSerial, f.noFirmware = absent == "serial", absent == "firmware"
		f.noSerialQuery = absent == "serial query unsupported"
		d := reloadDevice(t, f)
		if err := d.RefreshPositionMap(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 {
			t.Fatalf("cleanup=%v closed=%d", err, f.closed)
		}
	}
}

func TestReloadCleanupRetainsPreCycleControlsAcrossConfigure(t *testing.T) {
	for _, configure := range []bool{false, true} {
		// LOAD can reset non-secret controls; the later record preparation must not replace their original owner.
		f := newReloadFixture()
		d := reloadDevice(t, f)
		original := append([]byte(nil), f.mode...)
		f.onMotion = func(load bool) {
			if load {
				f.mode[3], f.mode[14], f.mode[19], f.mode[46] = 0, 4, 0, 0
				f.mode[18] &^= 0x80
			}
		}
		if err := d.RefreshPositionMap(context.Background()); err != nil {
			t.Fatal(err)
		}
		if configure {
			if _, err := d.Configure(context.Background(), &entity.TarMediaProfile{
				RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true,
			}, Read); err != nil {
				t.Fatal(err)
			}
		}

		// Existing mode cleanup restores only owned controls while preserving algorithms, partition and EOD facts.
		if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 || !reflect.DeepEqual(f.mode, original) || len(reloadMotions(f)) != 2 {
			t.Fatalf("cleanup=%v closed=%d mode=%x want=%x motions=%v", err, f.closed, f.mode, original, reloadMotions(f))
		}
	}
}

func TestReloadHeldCleanupNeverSelectsModeOrPretendsRestoration(t *testing.T) {
	for _, readable := range []bool{false, true} {
		// Cancellation leaves the cartridge held; unreadable or changed controls must remain a cleanup failure.
		f := newReloadFixture()
		d := reloadDevice(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		f.onMotion = func(load bool) {
			if !load {
				f.mode[14] = 4
				cancel()
			}
		}
		if err := d.RefreshPositionMap(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !readable {
			f.onQuery = func(command Command) {
				if command.CDB[0] == 0x5a {
					f.mode = []byte{0, 6, 0, 0, 0, 0, 0, 0}
				}
			}
		}

		// Cleanup uses its existing uncertain-failure boundary and sole Close without speculative control commands.
		if err := d.Cleanup(context.Background()); !errors.Is(err, ErrUncertain) || f.selects != 0 || f.closed != 1 || len(reloadMotions(f)) != 1 {
			t.Fatalf("cleanup=%v selects=%d closed=%d motions=%v", err, f.selects, f.closed, reloadMotions(f))
		}
	}
}
