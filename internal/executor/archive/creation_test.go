package archive

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

func TestArchiveGetCreationPreservesEffectiveInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		preview *entity.ScanJobSpec
		policy  entity.PreviewPolicy
		force   bool
	}{
		{name: "disabled", policy: entity.PreviewPolicy_PREVIEW_POLICY_NONE},
		{name: "missing only", preview: &entity.ScanJobSpec{
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
			PreviewPolicy:   entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY,
		}, policy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY},
		{name: "force rehash", preview: &entity.ScanJobSpec{
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
			PreviewPolicy:   entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL,
		}, policy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL, force: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Referenced Library/Location objects deliberately do not exist in the read fixture.
			spec := &entity.ArchiveJobSpec{Selections: []*entity.FileSelection{
				{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: 51}},
					Scope: entity.FileScope_FILE_SCOPE_ALL},
				{Target: &entity.FileSelection_Location{
					Location: &entity.LocationSelection{LocationId: 93, Path: "selected/file"},
				}},
			}}
			service, directory := creationFixture(t, &Config{
				ID: 1, Spec: spec, Preview: test.preview, PreviewJobID: 27, PreviewError: "previous Preview failure",
			})
			db, err := service.exe.NewStateDB(context.Background(), 7)
			require.NoError(t, err)
			require.NoError(t, db.Create(&Item{
				ID: 1, Status: entity.CopyStatus_COPY_STATUS_STAGED, TargetPath: "retained", MediaPath: "media/retained",
				Data:   &entity.ArchiveManifestFile{SourcePath: "/unavailable/file"},
				Result: &entity.ArchiveCopyResult{SizeBytes: 7, Sha256: make([]byte, 32)},
			}).Error)
			connection, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			before, err := os.ReadFile(filepath.Join(directory, "state.db"))
			require.NoError(t, err)

			// Repeated opens return fresh request values; editing a response cannot modify retained input.
			for range 3 {
				reply, err := service.GetCreation(context.Background(), &entity.GetArchiveJobCreationRequest{Id: 7})
				require.NoError(t, err)
				require.Empty(t, reply.UnavailableReason)
				require.EqualValues(t, -23, reply.Request.Priority)
				require.True(t, proto.Equal(spec, reply.Request.Spec))
				require.Equal(t, test.policy, reply.Request.PreviewPolicy)
				require.Equal(t, test.force, reply.Request.ForceRehash)
				reply.Request.Spec.Selections[1].GetLocation().Path = "edited"
			}

			// Runner initialization would reset STAGED; the entire database and log must remain unchanged.
			after, err := os.ReadFile(filepath.Join(directory, "state.db"))
			require.NoError(t, err)
			require.Equal(t, before, after)
			log, err := os.ReadFile(filepath.Join(directory, "job.log"))
			require.NoError(t, err)
			require.Equal(t, "retained evidence\n", string(log))
			require.Empty(t, service.exe.RunningJobIDs())
		})
	}
}

func TestArchiveGetCreationExplainsMissingOriginalInput(t *testing.T) {
	for _, config := range []*Config{nil, {ID: 1, Spec: &entity.ArchiveJobSpec{}}} {
		// Migrated Jobs have a manifest but no original public selections or Preview options.
		service, _ := creationFixture(t, config)
		reply, err := service.GetCreation(context.Background(), &entity.GetArchiveJobCreationRequest{Id: 7})
		require.NoError(t, err)
		require.Contains(t, reply.UnavailableReason, "selections")
		require.EqualValues(t, -23, reply.Request.Priority)
		require.Empty(t, reply.Request.GetSpec().GetSelections())
		require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_UNSPECIFIED, reply.Request.PreviewPolicy)

		// Invalid IDs and missing Jobs fail without substituting another bundle.
		_, err = service.GetCreation(context.Background(), nil)
		require.Error(t, err)
		_, err = service.GetCreation(context.Background(), &entity.GetArchiveJobCreationRequest{Id: 8})
		require.ErrorIs(t, err, executor.ErrJobNotFound)
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
		"id": 7, "executor_id": "local", "catalog_kind": entity.JobKind_JOB_KIND_ARCHIVE,
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
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}, &Config{}, &Item{}))
	require.NoError(t, db.Create(&executor.JobRecord{
		ID: 1, Kind: entity.JobKind_JOB_KIND_ARCHIVE, Priority: -23,
		Status: entity.JobStatus_JOB_STATUS_FAILED, Checkpoint: entity.JobStatus_JOB_STATUS_READY,
		Error: "retained failure",
	}).Error)
	if config != nil {
		require.NoError(t, db.Create(config).Error)
	}
	return &service{exe: exe}, directory
}
