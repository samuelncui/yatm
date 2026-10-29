package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestWrapFramesRejectMalformedIdentityWithoutGuessingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"missing header", []byte{0, 2}},
		{"bad length", []byte{0, 1, 0, 0}},
		{"partial descriptor", []byte{0, 3, 0, 0, 0}},
		{"short form for all request", []byte{0, 8, 0, 0, 0, 0, 0, 0, 0, 77}},
		{"duplicate pair", wrapData(WrapEndpoint{Wrap: 3, Object: 7}, WrapEndpoint{Wrap: 3, Object: 9})},
		{"truncated transfer", wrapData(WrapEndpoint{Wrap: 3, Object: 7})[:8]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if observed, complete, err := decodeWrapMap(tc.data, true); observed != nil || complete || !errors.Is(err, ErrMalformed) {
				t.Fatalf("map=%+v complete=%t error=%v", observed, complete, err)
			}
		})
	}
	for _, offset := range []int{2, 3, 8, 9} {
		data := wrapData(WrapEndpoint{Wrap: 3, Object: 7})
		data[offset] = 1
		if observed, _, err := decodeWrapMap(data, true); observed != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("reserved offset=%d map=%v error=%v", offset, observed, err)
		}
	}
	// Forty-eight bits are preserved, including the maximum encoding without an invented unknown sentinel.
	observed, complete, err := decodeWrapMap(wrapData(WrapEndpoint{Partition: 65535, Wrap: 65535, Object: 1<<48 - 1}), true)
	if err != nil || !complete || observed.Endpoints[0].Object != 1<<48-1 || observed.Endpoints[0].Partition != 65535 {
		t.Fatalf("wide map=%+v complete=%t error=%v", observed, complete, err)
	}
}

func TestWrapObservationRemainsBoundedAndNeverDeclaresFreshness(t *testing.T) {
	for _, count := range []int{0, 341, 342} {
		// The fixed query envelope is a resource bound, not an advertised hardware wrap maximum.
		endpoints := make([]WrapEndpoint, count)
		for i := range endpoints {
			endpoints[i] = WrapEndpoint{Wrap: uint32(i), Object: uint64(i + 1)}
		}
		f := newGeometryFixture()
		f.data = wrapData(endpoints...)
		d := geometryDevice(t, f)
		observed, err := d.Geometry(context.Background())
		limits := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION).GetGeometry()
		if err != nil || limits.Fresh != nil || count <= 341 && (observed == nil || len(observed.Endpoints) != count || limits.ReportedWraps == nil || limits.GetReportedWraps() != uint32(count)) || count == 342 && (observed != nil || limits.ReportedWraps != nil) {
			t.Fatalf("count=%d map=%+v limits=%v error=%v", count, observed, limits, err)
		}
		if err := d.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOptionalGeometryFailurePreservesBaselineButReadinessLossBlocksControls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *Response
		data     []byte
		blocked  bool
	}{
		{"malformed response", nil, []byte{0, 3, 0, 0, 0}, false},
		{"clean unsupported query", &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil, false},
		{"medium loss", &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, nil, true},
		{"not ready", &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 4, 1, false, 0, 0)}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGeometryFixture()
			f.response = tc.response
			if tc.data != nil {
				f.data = tc.data
			}
			d := geometryDevice(t, f)
			report := d.Report()
			profile := encryptionTestProfile()
			profile.EncryptionAlgorithm = nil
			_, err := d.Configure(context.Background(), profile, Read)
			if (err != nil) != tc.blocked || !tc.blocked && report.Reason != 0 || tc.blocked && report.Reason == 0 || tc.blocked && (f.selects != 0 || f.motions != 0 || f.writes != 0) {
				t.Fatalf("report=%v configure=%v mode=%d motion=%d write=%d", report.Reason, err, f.selects, f.motions, f.writes)
			}
			if tc.name == "medium loss" && report.GetTape().GetMediumPresent() || tc.name == "not ready" && !report.GetTape().GetMediumPresent() {
				t.Fatalf("presence=%v", report.GetTape().MediumPresent)
			}
			if err := d.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeometryCopiesInvalidateOnWritePreparationRecordWriteAndReset(t *testing.T) {
	for _, operation := range []string{"write preparation", "record write", "filemark", "reset"} {
		t.Run(operation, func(t *testing.T) {
			f := newGeometryFixture()
			d := geometryDevice(t, f)
			snapshot, err := d.Geometry(context.Background())
			if err != nil || !snapshot.Valid() {
				t.Fatalf("snapshot=%v error=%v", snapshot, err)
			}
			profile := encryptionTestProfile()
			profile.EncryptionAlgorithm = nil
			records, err := d.Configure(context.Background(), profile, Read)
			if err != nil {
				t.Fatal(err)
			}
			if err := records.Rewind(context.Background()); err != nil || !snapshot.Valid() {
				t.Fatalf("ordinary motion=%v validity=%t", err, snapshot.Valid())
			}
			switch operation {
			case "write preparation":
				_, err = d.Configure(context.Background(), profile, Append)
			case "record write":
				_, err = records.Write(context.Background(), make([]byte, 512))
			case "filemark":
				err = records.Filemarks(context.Background(), 1)
			case "reset":
				f.readResponse = &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 6, 0x29, 0, false, 0, 0)}
				_, err = records.Read(context.Background(), make([]byte, 512))
			}
			if snapshot.Valid() || operation != "reset" && err != nil || operation == "reset" && !errors.Is(err, ErrUncertain) {
				t.Fatalf("operation=%s error=%v validity=%t", operation, err, snapshot.Valid())
			}
			if current, err := d.Geometry(context.Background()); current != nil || err != nil || f.wrapQueries != 1 {
				t.Fatalf("withdrawn=%v error=%v queries=%d", current, err, f.wrapQueries)
			}
			if err := d.Cleanup(context.Background()); operation != "reset" && err != nil || operation == "reset" && !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
		})
	}
}

func TestGeometryUncertainInspectionPreservesUnavailableCleanupEvidence(t *testing.T) {
	f := newGeometryFixture()
	f.error = &UncertainError{Cause: errors.New("synthetic completion loss")}
	d, err := NewDevice(context.Background(), Identity{}, newSerializedTransport(f.execute, func() error { f.closed++; return nil }))
	if d != nil || !errors.Is(err, ErrUncertain) || f.closed != 1 || f.motions != 0 || f.selects != 0 || f.writes != 0 {
		t.Fatalf("device=%v error=%v closed=%d motion=%d mode=%d write=%d", d, err, f.closed, f.motions, f.selects, f.writes)
	}
}

func TestGeometryReplacementWithdrawsPreviousRequestOwnedCopies(t *testing.T) {
	// Re-observation replaces the owned snapshot; cleanup cannot leave an earlier clone usable.
	f := newGeometryFixture()
	d := geometryDevice(t, f)
	before, err := d.Geometry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.inspectGeometry(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := d.Geometry(context.Background())
	if err != nil || before.Valid() || !after.Valid() || after.Fresh != nil {
		t.Fatalf("before=%t after=%t error=%v", before.Valid(), after.Valid(), err)
	}
	if err := d.Cleanup(context.Background()); err != nil || after.Valid() {
		t.Fatalf("cleanup=%v validity=%t", err, after.Valid())
	}
}

func TestGeometryReadinessLossDistinguishesFormFromEndpointObservation(t *testing.T) {
	for _, afterGeometry := range []bool{false, true} {
		// A reported CDB form is retained; lost cartridge identity withdraws endpoint/count facts and snapshot use.
		f := newGeometryFixture()
		f.noMedium, f.capacityLoss = !afterGeometry, afterGeometry
		d := geometryDevice(t, f)
		geometry, err := d.Geometry(context.Background())
		feature := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
		limits := feature.GetGeometry()
		if err != nil || geometry != nil || limits == nil || limits.AllWraps == nil || !limits.GetAllWraps() || limits.ReportedWraps != nil || limits.Fresh != nil || feature.Reason != entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || feature.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE {
			t.Fatalf("afterGeometry=%t map=%v error=%v feature=%v", afterGeometry, geometry, err, feature)
		}
		wantQueries, detail := 0, "no endpoint response observed"
		if afterGeometry {
			wantQueries, detail = 1, "endpoint response invalidated"
		}
		if f.wrapQueries != wantQueries || !strings.Contains(feature.Detail, detail) {
			t.Fatalf("afterGeometry=%t queries=%d detail=%s", afterGeometry, f.wrapQueries, feature.Detail)
		}
		if err := d.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGeometryPassiveAllowlistRejectsOtherMaintenanceForms(t *testing.T) {
	// No refresh, arbitrary qualifier, WNV or unsafe reserved bits pass the passive boundary.
	for _, all := range []bool{false, true} {
		command := Command{CDB: wrapCommand(all), Data: make([]byte, int(binary.BigEndian.Uint32(wrapCommand(all)[6:10]))), Direction: FromDevice}
		if !passiveCommand(command) {
			t.Fatalf("valid form rejected: %v", command.CDB)
		}
		for _, offset := range []int{1, 2, 3, 4, 5, 10, 11} {
			changed := command
			changed.CDB = append([]byte(nil), command.CDB...)
			changed.CDB[offset] |= 0x80
			if passiveCommand(changed) {
				t.Fatalf("unsafe byte%d accepted: %v", offset, changed.CDB)
			}
		}
	}
	if passiveCommand(Command{CDB: []byte{0x1b, 0, 0, 0, 8, 0}, Direction: NoData}) {
		t.Fatal("HOLD motion was accepted as passive")
	}
}
