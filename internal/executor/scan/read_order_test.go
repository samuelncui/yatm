package scan

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestScanRefreshRejectsEveryOtherMediaPolicyBeforeAccess(t *testing.T) {
	for _, policy := range []entity.ScanResultPolicy{
		entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
		entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY,
		entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES,
	} {
		// Frozen Volume facts reject a forged Tape refresh request before a device can be reserved or opened.
		exe, _, stored := setupVerifyExecutor(t)
		r := &runner{exe: exe}
		config := &Config{MediaKind: stored.Kind, MediaIdentity: stored.Identity, MediaProfile: stored.Profile,
			Spec: &entity.ScanJobSpec{MediaId: stored.ID, ResultPolicy: policy}}
		_, err := r.openSession(context.Background(), config, (&entity.ReadTapeTarget{
			Device: "/dev/not-configured", RefreshPositionMap: true,
		}).Pack())
		require.ErrorContains(t, err, "refresh requires tar VERIFY_COPIES")
	}

	// LTFS Verify remains on its user-owned adapter path and cannot request raw refresh.
	exe, _, _ := setupVerifyExecutor(t)
	stored, err := exe.Lib().CreateMedia(context.Background(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: "LTFS01", Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	r := &runner{exe: exe}
	config := &Config{MediaKind: stored.Kind, MediaIdentity: stored.Identity, MediaProfile: stored.Profile,
		Spec: &entity.ScanJobSpec{MediaId: stored.ID, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}
	_, err = r.openSession(context.Background(), config, (&entity.ReadTapeTarget{
		Device: "/dev/not-configured", RefreshPositionMap: true,
	}).Pack())
	require.ErrorContains(t, err, "refresh requires tar VERIFY_COPIES")

	// Even a valid tar profile cannot refresh during report-only processing or fresh inventory.
	stored, err = exe.Lib().CreateMedia(context.Background(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: "TAR001", Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
			InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096,
		}}).Pack()})
	require.NoError(t, err)
	for _, policy := range []entity.ScanResultPolicy{entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY, entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY} {
		config := &Config{MediaKind: stored.Kind, MediaIdentity: stored.Identity, MediaProfile: stored.Profile,
			Spec: &entity.ScanJobSpec{MediaId: stored.ID, ResultPolicy: policy}}
		_, err := r.openSession(context.Background(), config, (&entity.ReadTapeTarget{
			Device: "/dev/not-configured", RefreshPositionMap: true,
		}).Pack())
		require.ErrorContains(t, err, "refresh requires tar VERIFY_COPIES")
	}
}
