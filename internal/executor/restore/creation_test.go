package restore

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
)

func TestRestoreGetCreationPreservesSelectionsDestinationAndCutoff(t *testing.T) {
	for _, cutoff := range []*int64{nil, proto.Int64(0), proto.Int64(1790000000123)} {
		// Preserve both explicit versions and automatic roots, without resolving a newer version or destination.
		spec := &entity.RestoreJobSpec{
			FileVersionIds: []int64{71, 73},
			Selections:     []*entity.FileSelection{policyFileSelection(99)},
			Destination: &entity.RestoreDestination{
				LocationId: 91, Path: "selected/output", RootPath: "/unavailable", ExecutorId: "local",
			},
			AllowDamagedCopies: true, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtNs: cutoff},
			SkipUnmatchedVersions: cutoff != nil,
		}
		service, directory := creationFixture(t, &Config{ID: 1, Spec: spec, ManifestFrozen: true})
		before, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)

		// A returned request owns its decoded values; repeated reads preserve optional zero and the exact cutoff.
		for range 3 {
			reply, err := service.GetCreation(context.Background(), &entity.GetRestoreJobCreationRequest{Id: 7})
			require.NoError(t, err)
			require.Empty(t, reply.UnavailableReason)
			require.EqualValues(t, -23, reply.Request.Priority)
			require.True(t, proto.Equal(spec, reply.Request.Spec))
			reply.Request.Spec.VersionPolicy.BeforeAtNs = proto.Int64(1)
			reply.Request.Spec.FileVersionIds[0] = 999
			reply.Request.Spec.Destination.Path = "edited"
		}

		// The stored policy, manifest-frozen flag, failure and destination never change.
		after, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		require.Equal(t, before, after)
		require.Empty(t, service.exe.RunningJobIDs())
	}
}

func TestRestoreGetCreationExplainsMissingOriginalInput(t *testing.T) {
	for _, config := range []*Config{
		nil,
		{ID: 1, Spec: &entity.RestoreJobSpec{}, LegacyRoot: "/legacy/output", ManifestFrozen: true},
		{ID: 1, Spec: &entity.RestoreJobSpec{FileVersionIds: []int64{7}}},
	} {
		// Legacy roots remain execution evidence and cannot become guessed Location registrations.
		service, directory := creationFixture(t, config)
		before, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		reply, err := service.GetCreation(context.Background(), &entity.GetRestoreJobCreationRequest{Id: 7})
		require.NoError(t, err)
		require.Contains(t, reply.UnavailableReason, "destination")
		require.Nil(t, reply.Request.GetSpec().GetDestination())
		if config != nil {
			require.True(t, proto.Equal(config.Spec, reply.Request.Spec))
		}
		if config != nil && config.LegacyRoot != "" {
			require.Contains(t, reply.UnavailableReason, "v0.1.x")
			require.Contains(t, reply.UnavailableReason, "version choices")
		}

		// Missing input explanations and rejected IDs are reads, including for migrated bundles.
		_, err = service.GetCreation(context.Background(), nil)
		require.Error(t, err)
		after, err := os.ReadFile(filepath.Join(directory, "state.db"))
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}

func creationFixture(t *testing.T, config *Config) (*service, string) {
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
	require.NoError(t, catalog.Model(executor.ModelJob).Create(map[string]any{
		"id": 7, "executor_id": "local", "catalog_kind": entity.JobKind_JOB_KIND_RESTORE,
		"created_at_ns": 123, "updated_at_ns": 456, "revision": 3, "deleted_at_ns": 0,
	}).Error)
	directory, err := exe.EnsureJobWorkPath(context.Background(), 7)
	require.NoError(t, err)
	metadata, err := json.Marshal(dataformat.NewBundle(7, 123*int64(time.Millisecond)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "job.json"), metadata, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "job.log"), []byte("retained evidence\n"), 0644))

	// Use the owning row definitions, including the same config shape used by legacy import.
	db, err := resource.OpenSQLite(filepath.Join(directory, "state.db"))
	require.NoError(t, err)
	stateConnection, err := db.DB()
	require.NoError(t, err)
	defer stateConnection.Close()
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}, &Config{}, &File{}, &Copy{}))
	require.NoError(t, db.Create(&executor.JobRecord{
		ID: 1, Kind: entity.JobKind_JOB_KIND_RESTORE, Priority: -23,
		Status: entity.JobStatus_JOB_STATUS_FAILED, Checkpoint: entity.JobStatus_JOB_STATUS_READY,
		Error: "retained failure",
	}).Error)
	if config != nil {
		require.NoError(t, db.Create(config).Error)
	}
	return &service{exe: exe}, directory
}
