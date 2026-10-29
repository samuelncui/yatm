package tape

import (
	"context"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestNavigationRequiresPreparedEnvelopeAndApplicableEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       entity.DeviceFeatureKind
		validation entity.DeviceFeatureValidation
		support    entity.DeviceFeatureSupport
		reason     entity.DeviceReason
		eligible   bool
	}{
		{
			name:       "unvalidated",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     0,
			eligible:   true,
		},
		{
			name:       "validated",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_VALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     0,
			eligible:   true,
		},
		{
			name:       "unknown current position",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     entity.DeviceReason_DEVICE_REASON_NOT_READY,
			eligible:   true,
		},
		{
			name:       "failed position",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     0,
			eligible:   false,
		},
		{
			name:       "failed locate",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     0,
			eligible:   false,
		},
		{
			name:       "unknown support",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN,
			reason:     0,
			eligible:   false,
		},
		{
			name:       "unsupported",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED,
			reason:     entity.DeviceReason_DEVICE_REASON_UNSUPPORTED,
			eligible:   false,
		},
		{
			name:       "rejected position",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     entity.DeviceReason_DEVICE_REASON_UNSUPPORTED,
			eligible:   false,
		},
		{
			name:       "malformed position",
			kind:       entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION,
			validation: entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			support:    entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
			reason:     entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE,
			eligible:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Passive command reports alone cannot bypass the configured standard record/partition envelope.
			fixture := newDeviceFixture()
			device := fixtureDevice(t, fixture)
			defer device.transport.Close()
			if navigator, err := device.Navigation(context.Background()); err != nil || navigator != nil {
				t.Fatalf("unprepared navigator=%v err=%v", navigator, err)
			}
			if _, err := device.Configure(context.Background(), &entity.TarMediaProfile{
				RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512,
			}, Read); err != nil {
				t.Fatal(err)
			}

			// Current position readiness is refreshed by the consumer; failed or contradictory support is not.
			f := feature(device.report, tc.kind)
			f.Validation, f.Support, f.Reason = tc.validation, tc.support, tc.reason
			f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN
			navigator, err := device.Navigation(context.Background())
			if err != nil || (navigator != nil) != tc.eligible || fixture.motions != 0 {
				t.Fatalf("navigator=%v err=%v motions=%d", navigator, err, fixture.motions)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := device.Navigation(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
