package scan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestScanGetCreationPreservesRetainedSpec(t *testing.T) {
	for _, spec := range []*entity.ScanJobSpec{
		{Selections: scanLocationSelections(93, "selected/file", "other"),
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
			ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
			CompareLibrary:  true, PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY,
			WriteSignatureCache: proto.Bool(false)},
		{Selections: []*entity.FileSelection{
			{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: 0}},
				Scope: entity.FileScope_FILE_SCOPE_ALL},
		}, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING,
			ResultPolicy:  entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
			PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL},
		{MediaId: 59, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
			ResultPolicy:  entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES,
			PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_NONE},
	} {
		// Sources and Media are intentionally unavailable; GetCreation reads the recorded choices only.
		service, directory := creationFixture(t, &Config{ID: 1, Spec: spec})
		before, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)

		// Repeated opens preserve every current Scan option without deriving roots from entries.
		for range 3 {
			reply, err := service.GetCreation(context.Background(), &entity.GetScanJobCreationRequest{Id: 7})
			require.NoError(t, err)
			require.Empty(t, reply.UnavailableReason)
			require.EqualValues(t, -23, reply.Request.Priority)
			require.True(t, proto.Equal(spec, reply.Request.Spec))
			reply.Request.Spec.MediaId = 999
			reply.Request.Spec.Selections = nil
		}

		// No enumeration, runner creation, migration or state reset is needed for a read.
		after, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		require.Equal(t, before, after)
		require.Empty(t, service.exe.RunningJobIDs())
	}
}

func TestScanGetCreationExplainsIndexedAndMissingSources(t *testing.T) {
	for _, config := range []*Config{
		nil,
		{ID: 1, Spec: &entity.ScanJobSpec{}},
		{ID: 1, IndexedInput: true, Spec: &entity.ScanJobSpec{
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
			ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
			PreviewPolicy:   entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL,
		}},
	} {
		// Companion entries are not a recoverable public selection; never broaden them to a Location root.
		service, directory := creationFixture(t, config)
		before, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		reply, err := service.GetCreation(context.Background(), &entity.GetScanJobCreationRequest{Id: 7})
		require.NoError(t, err)
		require.NotEmpty(t, reply.UnavailableReason)
		require.Empty(t, reply.Request.GetSpec().GetSelections())
		require.Zero(t, reply.Request.GetSpec().GetMediaId())
		if config != nil {
			require.True(t, proto.Equal(config.Spec, reply.Request.Spec))
		}
		if config != nil && config.IndexedInput {
			require.Contains(t, reply.UnavailableReason, "indexed Archive")
			// An Archive's companion Scan is not a submitted Scan: with no other, there is no latest one.
			latest, err := service.GetCreation(context.Background(), &entity.GetScanJobCreationRequest{Latest: true})
			require.NoError(t, err)
			require.Nil(t, latest.Request)
		}

		// Reporting incomplete input must not start the retained pipeline or rewrite its config.
		_, err = service.GetCreation(context.Background(), nil)
		require.Error(t, err)
		after, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}

func creationFixture(t *testing.T, config *Config) (*service, string) {
	t.Helper()
	service, directory, _ := creationCatalogFixture(t, config)
	return service, directory
}

func creationCatalogFixture(t *testing.T, config *Config) (*service, string, *gorm.DB) {
	t.Helper()
	// Seed a retained bundle without a Library, runner, source access or background work.
	root := t.TempDir()
	catalog, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	connection, err := catalog.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	exe := executor.New(catalog, nil, nil, executor.Paths{Work: root}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	directory := seedCreationJob(t, exe, catalog, 7, config)
	return &service{exe: exe}, directory, catalog
}

// seedCreationJob adds one retained Scan bundle with the given config and returns its directory.
func seedCreationJob(t *testing.T, exe *executor.Executor, catalog *gorm.DB, id int64, config *Config) string {
	t.Helper()
	require.NoError(t, catalog.Model(executor.ModelJob).Create(map[string]any{
		"id": id, "executor_id": "local", "catalog_kind": entity.JobKind_JOB_KIND_SCAN,
		"created_at_ns": 123, "updated_at_ns": 456, "revision": 3, "deleted_at_ns": 0,
	}).Error)
	directory, err := exe.EnsureJobWorkPath(context.Background(), id)
	require.NoError(t, err)
	metadata, err := json.Marshal(dataformat.NewBundle(id, 123*int64(time.Millisecond)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "job.json"), metadata, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "job.log"), []byte("retained evidence\n"), 0644))

	// Use the owning row definitions, including the same config shape used by legacy import.
	db, err := resource.OpenSQLite(filepath.Join(directory, "state.db"))
	require.NoError(t, err)
	stateConnection, err := db.DB()
	require.NoError(t, err)
	defer stateConnection.Close()
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}, &Config{}, &Entry{}))
	require.NoError(t, db.Create(&executor.JobRecord{
		ID: 1, Kind: entity.JobKind_JOB_KIND_SCAN, Priority: -23,
		Status: entity.JobStatus_JOB_STATUS_FAILED, Checkpoint: entity.JobStatus_JOB_STATUS_READY,
		Error: "retained failure",
	}).Error)
	if config != nil {
		require.NoError(t, db.Create(config).Error)
	}
	return directory
}

func TestScanGetCreationLatestSkipsABoundedRunOfCompanionScans(t *testing.T) {
	// Job 7 is the submitted Scan; newer companion Scans of Archives are skipped up to the bound.
	submitted := &entity.ScanJobSpec{MediaId: 5, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY}
	service, _, catalog := creationCatalogFixture(t, &Config{ID: 1, Spec: submitted})
	companion := func(id int64) {
		seedCreationJob(t, service.exe, catalog, id, &Config{ID: 1, IndexedInput: true, Spec: &entity.ScanJobSpec{}})
	}
	for id := int64(8); id < 8+latestScanCandidates; id++ {
		companion(id)
	}
	reply, err := service.GetCreation(context.Background(), &entity.GetScanJobCreationRequest{Latest: true})
	require.NoError(t, err)
	require.True(t, proto.Equal(submitted, reply.Request.GetSpec()), "the bound's worth of companions still reaches the submitted Scan")

	// One companion more than the bound ends the walk without a latest Scan.
	companion(8 + latestScanCandidates)
	reply, err = service.GetCreation(context.Background(), &entity.GetScanJobCreationRequest{Latest: true})
	require.NoError(t, err)
	require.Nil(t, reply.Request)
}
