package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

type geometryFixture struct {
	*deviceFixture
	vendor, model            string
	data, form               []byte
	formQueries, wrapQueries int
	afterWrapQueries         int
	shortOnly, unsupported   bool
	unknown, wrongAction     bool
	response                 *Response
	error                    error
	noMedium, capacityLoss   bool
	notReady                 bool
	onWrap                   func()
}

func newGeometryFixture() *geometryFixture {
	return &geometryFixture{
		deviceFixture: newDeviceFixture(), vendor: "IBM", model: "Ultrium 5-SCSI",
		data: wrapData(WrapEndpoint{Wrap: 3, Object: 1 << 40}, WrapEndpoint{Wrap: 8, Object: 1 << 40},
			WrapEndpoint{Partition: 1, Wrap: 3, Object: 77}),
	}
}

func wrapData(endpoints ...WrapEndpoint) []byte {
	data := make([]byte, 4+12*len(endpoints))
	binary.BigEndian.PutUint16(data[:2], uint16(len(data)-2))
	for i, endpoint := range endpoints {
		offset := 4 + i*12
		binary.BigEndian.PutUint16(data[offset:offset+2], uint16(endpoint.Wrap))
		binary.BigEndian.PutUint16(data[offset+2:offset+4], uint16(endpoint.Partition))
		object := endpoint.Object
		for j := 11; j >= 6; j-- {
			data[offset+j], object = byte(object), object>>8
		}
	}
	return data
}

func (f *geometryFixture) execute(command Command) (Response, error) {
	// Independent synthetic maintenance frames never issue LOAD, measure geometry or claim freshness.
	if f.wrapQueries != 0 {
		f.afterWrapQueries++
	}
	if command.CDB[0] == 0 && f.noMedium || command.CDB[0] == 0x4d && f.capacityLoss {
		return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, nil
	}
	if command.CDB[0] == 0 && f.notReady {
		return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 4, 1, false, 0, 0)}, nil
	}

	// Supply a standard report or an explicit vendor report for the requested opcode/service action.
	if command.CDB[0] == 0xa3 && command.CDB[1] == 0x0c && command.CDB[3] == 0xa3 {
		f.formQueries++
		data := make([]byte, 16)
		data[1], data[3], data[4], data[5] = 3, 12, 0xa3, 0x1f
		for i := 6; i < len(data); i++ {
			data[i] = 0xff
		}
		if f.shortOnly {
			data[7] = 0
		}
		if f.unsupported {
			data = []byte{0, 1}
		}
		if f.unknown {
			data = []byte{0, 0}
		}
		if f.wrongAction {
			data[5] = 0xff
		}
		if f.form != nil {
			data = f.form
		}
		copy(command.Data, data)
		return Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
	}

	// Record issue before injecting completion or cancellation, without manufacturing another submitted form.
	if command.CDB[0] == 0xa3 && command.CDB[1] == 0x1f {
		f.wrapQueries++
		if f.onWrap != nil {
			f.onWrap()
		}
		if f.response != nil || f.error != nil {
			if f.response != nil {
				return *f.response, f.error
			}
			return Response{}, f.error
		}
		if !passiveCommand(command) || command.CDB[2] != 0x45 || command.CDB[3] != 0 && command.CDB[3] != 2 {
			return Response{}, ErrMalformed
		}
		data := f.data
		if command.CDB[3] == 0 {
			data = []byte{0, 8, 0, 0, 0, 0, 0, 0, 0, 77}
		}
		copied := copy(command.Data, data)
		return Response{Completed: true, Residual: int32(len(command.Data) - copied)}, nil
	}

	// Identity selection uses a product family, independently of firmware or synthetic endpoint data.
	response, err := f.deviceFixture.execute(command)
	if command.CDB[0] == 0x12 && command.CDB[1] == 0 {
		copy(command.Data[8:16], []byte("        "))
		copy(command.Data[8:16], f.vendor)
		copy(command.Data[16:32], []byte("                "))
		copy(command.Data[16:32], f.model)
	}
	return response, err
}

func geometryDevice(t *testing.T, f *geometryFixture) *Device {
	t.Helper()
	d, err := NewDevice(context.Background(), Identity{}, newSerializedTransport(f.execute, func() error { f.closed++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGeometryPreservesReportedPairsGapsObjectsAndUnknownFreshness(t *testing.T) {
	// Equal objects and repeated wrap numbers across partitions are not fabricated boundaries or duplicate identities.
	f := newGeometryFixture()
	d := geometryDevice(t, f)
	geometry, err := d.Geometry(context.Background())
	want := []WrapEndpoint{{Wrap: 3, Object: 1 << 40}, {Wrap: 8, Object: 1 << 40}, {Partition: 1, Wrap: 3, Object: 77}}
	if err != nil || !geometry.Valid() || !geometry.AllWraps || geometry.Fresh != nil || geometry.Source != "A3/1F/45" || geometry.ObservedAt.IsZero() || !reflect.DeepEqual(geometry.Endpoints, want) {
		t.Fatalf("geometry=%+v error=%v", geometry, err)
	}
	geometry.Endpoints[0].Object = 9
	again, err := d.Geometry(context.Background())
	if err != nil || !reflect.DeepEqual(again.Endpoints, want) || f.wrapQueries != 1 {
		t.Fatalf("snapshot=%+v error=%v queries=%d", again, err, f.wrapQueries)
	}
	report := d.Report()
	observedFeature := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
	if observedFeature.GetGeometry().Fresh != nil || observedFeature.GetGeometry().GetReportedWraps() != 3 || !observedFeature.GetGeometry().GetAllWraps() || observedFeature.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED || report.GetTape().RecommendedWriteOptions != nil {
		t.Fatalf("feature=%v", observedFeature)
	}
	order := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER)
	if order.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN || order.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED || order.Reason != entity.DeviceReason_DEVICE_REASON_NOT_READY {
		t.Fatalf("order=%v", order)
	}
	if err := d.Cleanup(context.Background()); err != nil || geometry.Valid() || again.Valid() {
		t.Fatalf("cleanup=%v validity=%t/%t", err, geometry.Valid(), again.Valid())
	}
}

func TestGeometryUnknownFamilyAndUnprovenFormsNeverIssueVendorQuery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vendor      string
		setup       func(*geometryFixture)
		wantForms   int
		wantSupport entity.DeviceFeatureSupport
	}{
		{"unknown family", "OTHER", nil, 0, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"other HP product", "HP", func(f *geometryFixture) { f.model = "DAT160" }, 0, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"vendor reporting outside HPE", "IBM", func(f *geometryFixture) { f.form = vendorWrapReportData() }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"unknown reporting", "HP", func(f *geometryFixture) { f.unknown = true }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"clean unsupported", "HPE", func(f *geometryFixture) { f.unsupported = true }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED},
		{"wrong literal action", "IBM", func(f *geometryFixture) { f.wrongAction = true }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"wrong HPE literal action", "HP", func(f *geometryFixture) { f.wrongAction = true }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN},
		{"HPE missing qualifier mask", "HPE", func(f *geometryFixture) { f.form = vendorWrapReportData(); f.form[1] = 3 }, 1, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGeometryFixture()
			f.vendor = tc.vendor
			if tc.setup != nil {
				tc.setup(f)
			}
			d := geometryDevice(t, f)
			geometry, err := d.Geometry(context.Background())
			feature := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
			if err != nil || geometry != nil || f.wrapQueries != 0 || f.formQueries != tc.wantForms || feature.Support != tc.wantSupport {
				t.Fatalf("map=%v error=%v queries=%d forms=%d feature=%v", geometry, err, f.wrapQueries, f.formQueries, feature)
			}
			if err := d.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeometryFirstOnlyHasNoInventedEndpointAssociation(t *testing.T) {
	f := newGeometryFixture()
	f.shortOnly = true
	d := geometryDevice(t, f)
	geometry, err := d.Geometry(context.Background())
	feature := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
	if err != nil || geometry == nil || geometry.AllWraps || len(geometry.Endpoints) != 0 || geometry.Fresh != nil || feature.GetGeometry().GetAllWraps() || feature.GetGeometry().GetReportedWraps() != 1 || feature.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL {
		t.Fatalf("map=%+v error=%v feature=%v", geometry, err, feature)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGeometryRefreshRefusesBeforeQueriesOrMutation(t *testing.T) {
	// An explicitly unsupported reload form leaves baseline inspection and access unchanged.
	f := newGeometryFixture()
	f.deviceFixture.unsupported = 0x1b
	d := geometryDevice(t, f)
	forms, queries, security := f.formQueries, f.wrapQueries, f.security
	if err := d.RefreshPositionMap(context.Background()); !errors.Is(err, ErrUnsupported) || f.formQueries != forms || f.wrapQueries != queries || f.security != security || f.selects != 0 || f.formats != 0 || f.motions != 0 || f.writes != 0 {
		t.Fatalf("refresh=%v forms=%d queries=%d mode=%d motion=%d write=%d", err, f.formQueries, f.wrapQueries, f.selects, f.motions, f.writes)
	}

	// Unsupported refresh retains the ordinary cleanup boundary.
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func vendorWrapReportData() []byte {
	// The vendor-specific report supplies no standard field-mask evidence for qualifier 45h or RA.
	return []byte{0, 5, 0, 12, 0xa3, 0x1f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

func TestHPEReportedFormsObserveEndpointShapeWithoutQualification(t *testing.T) {
	for _, vendor := range []string{"HP", "HPE"} {
		for _, tc := range []struct {
			name                string
			short, vendor, ctdp bool
		}{
			{name: "standard all"}, {name: "standard short", short: true},
			{name: "vendor all", vendor: true}, {name: "vendor all with timeouts", vendor: true, ctdp: true},
		} {
			t.Run(vendor+"/"+tc.name, func(t *testing.T) {
				// HPE's independently documented layout preserves six-byte objects and full pair identities.
				f := newGeometryFixture()
				f.vendor, f.shortOnly = vendor, tc.short
				if vendor == "HPE" {
					f.model = "Ultrium 9-SCSI"
				}
				f.data = []byte{0, 14, 0, 0, 0xff, 0xfe, 0xff, 0xff, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
				if tc.vendor {
					f.form = vendorWrapReportData()
					if tc.ctdp {
						f.form[1] |= 0x80
						f.form = append(f.form, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
					}
					if support, err := commandFormSupport(f.form, wrapCommand(true)); err != nil || support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN {
						t.Fatalf("generic vendor report support=%v error=%v", support, err)
					}
				}

				// One query establishes only observed form/count; neither freshness nor AUTO is inferred.
				d := geometryDevice(t, f)
				observed, err := d.Geometry(context.Background())
				reported := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
				limits := reported.GetGeometry()
				if err != nil || !observed.Valid() || f.formQueries != 1 || f.wrapQueries != 1 || observed.AllWraps == tc.short || observed.Fresh != nil ||
					reported.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY || reported.Evidence != entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_OBSERVED ||
					reported.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED || limits.GetReportedWraps() != 1 || limits.Fresh != nil {
					t.Fatalf("map=%v error=%v queries=%d feature=%v", observed, err, f.wrapQueries, reported)
				}
				want := []WrapEndpoint{{Partition: 65535, Wrap: 65534, Object: 1<<48 - 1}}
				wantSupport := entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
				if tc.short {
					want, wantSupport = nil, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL
				}
				if !reflect.DeepEqual(observed.Endpoints, want) || reported.Support != wantSupport ||
					limits.GetAllWraps() == tc.short || limits.GetSource() != "A3/1F/45" || observed.Source != "A3/1F/45" {
					t.Fatalf("map=%+v feature=%v", observed, reported)
				}
				order := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER)
				if order.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN || d.Report().GetTape().RecommendedWriteOptions != nil {
					t.Fatalf("unqualified ordering=%v", order)
				}

				// Closure invalidates every family snapshot without another device query or mutation.
				if err := d.Cleanup(context.Background()); err != nil || observed.Valid() || f.wrapQueries != 1 || f.motions != 0 || f.selects != 0 || f.writes != 0 {
					t.Fatalf("cleanup=%v valid=%t queries=%d", err, observed.Valid(), f.wrapQueries)
				}
			})
		}
	}
}

func TestHPEVendorReportRequiresCompleteExactEnvelopeBeforeProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"missing header", func(data []byte) []byte { return data[:1] }},
		{"truncated usage", func(data []byte) []byte { return data[:15] }},
		{"reserved header", func(data []byte) []byte { data[0] = 1; return data }},
		{"reserved support bits", func(data []byte) []byte { data[1] |= 0x08; return data }},
		{"short CDB", func(data []byte) []byte { data[3] = 11; return data }},
		{"long CDB", func(data []byte) []byte { data[3] = 13; return data }},
		{"wrong opcode", func(data []byte) []byte { data[4] = 0xa4; return data }},
		{"wrong literal action", func(data []byte) []byte { data[5] = 0xff; return data }},
		{"truncated timeout descriptor", func(data []byte) []byte { data[1] |= 0x80; return data }},
		{"wrong timeout descriptor length", func(data []byte) []byte {
			data[1] |= 0x80
			return append(data, 0, 11, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A vendor flag cannot turn a malformed capability report into permission to issue a CDB.
			f := newGeometryFixture()
			f.vendor, f.form = "HP", tc.change(vendorWrapReportData())
			d := geometryDevice(t, f)
			reported := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
			if f.wrapQueries != 0 || reported.Reason != entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE || reported.GetGeometry() != nil || d.Report().Reason != 0 {
				t.Fatalf("queries=%d feature=%v report=%v", f.wrapQueries, reported, d.Report().Reason)
			}

			// Optional discovery failure preserves the ordinary device lifecycle.
			if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 {
				t.Fatalf("cleanup=%v closes=%d", err, f.closed)
			}
		})
	}
}

func TestHPEVendorObservationFailureNeverIssuesAnotherForm(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  *Response
		data      []byte
		cancel    bool
		uncertain bool
		blocked   bool
	}{
		{name: "clean rejected all", response: &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}},
		{name: "malformed all", data: []byte{0, 3, 0, 0, 0}},
		{name: "medium loss", response: &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, blocked: true},
		{name: "not ready", response: &Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 4, 1, false, 0, 0)}, blocked: true},
		{name: "deferred rejection", response: &Response{Completed: true, Status: 2, Sense: fixedSense(0x71, 5, 0x24, 0, false, 0, 0)}, uncertain: true},
		{name: "host failure", response: &Response{Completed: true, HostStatus: 1}, uncertain: true},
		{name: "canceled after issue", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Native completion, readiness and cancellation remain distinct from a clean optional rejection.
			f := newGeometryFixture()
			f.vendor, f.form, f.response = "HPE", vendorWrapReportData(), tc.response
			if tc.data != nil {
				f.data = tc.data
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				f.onWrap = cancel
			}
			d, err := NewDevice(ctx, Identity{}, newSerializedTransport(f.execute, func() error { f.closed++; return nil }))
			if f.wrapQueries != 1 || tc.uncertain != errors.Is(err, ErrUncertain) || tc.cancel != errors.Is(err, context.Canceled) {
				t.Fatalf("device=%v error=%v queries=%d", d, err, f.wrapQueries)
			}
			if tc.uncertain || tc.cancel {
				if d != nil || f.afterWrapQueries != 0 || f.closed != 1 {
					t.Fatalf("device=%v later queries=%d closes=%d", d, f.afterWrapQueries, f.closed)
				}
				return
			}

			// A rejected/malformed observation supplies no map; only readiness loss blocks baseline controls.
			if err != nil || d == nil {
				t.Fatalf("device=%v error=%v", d, err)
			}
			observed, err := d.Geometry(context.Background())
			reported := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
			if err != nil || observed != nil || reported.GetGeometry() != nil ||
				reported.Support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED ||
				reported.Support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED ||
				reported.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED {
				t.Fatalf("map=%v error=%v feature=%v", observed, err, reported)
			}
			profile := encryptionTestProfile()
			profile.EncryptionAlgorithm = nil
			_, err = d.Configure(context.Background(), profile, Read)
			if (err != nil) != tc.blocked || f.wrapQueries != 1 || tc.blocked && (f.selects != 0 || f.motions != 0 || f.writes != 0) {
				t.Fatalf("configure=%v queries=%d mode=%d motion=%d writes=%d", err, f.wrapQueries, f.selects, f.motions, f.writes)
			}
			if err := d.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHPEVendorReportWithoutReadyMediumDoesNotObserveAForm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		noMedium bool
		reason   entity.DeviceReason
	}{
		{"no medium", true, entity.DeviceReason_DEVICE_REASON_NO_MEDIUM},
		{"not ready", false, entity.DeviceReason_DEVICE_REASON_NOT_READY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A vendor report does not supply cartridge-dependent query acceptance or endpoint facts.
			f := newGeometryFixture()
			f.vendor, f.form = "HP", vendorWrapReportData()
			f.noMedium, f.notReady = tc.noMedium, !tc.noMedium
			d := geometryDevice(t, f)
			observed, err := d.Geometry(context.Background())
			reported := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
			if err != nil || observed != nil || f.wrapQueries != 0 || reported.GetGeometry() != nil ||
				reported.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN || reported.Reason != tc.reason {
				t.Fatalf("map=%v error=%v queries=%d feature=%v", observed, err, f.wrapQueries, reported)
			}

			// Passive discovery and closure never load, position or configure the cartridge.
			if err := d.Cleanup(context.Background()); err != nil || f.motions != 0 || f.selects != 0 || f.writes != 0 {
				t.Fatalf("cleanup=%v motion=%d mode=%d writes=%d", err, f.motions, f.selects, f.writes)
			}
		})
	}
}
