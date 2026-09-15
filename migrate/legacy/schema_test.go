package legacy

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/internal/dataformat"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestDetectSchemaAndRunningJobs(t *testing.T) {
	// Detect an empty database before either catalog format exists.
	db, err := resource.OpenSQLite(t.TempDir() + "/main.db")
	require.NoError(t, err)
	schema, err := DetectSchema(db)
	require.NoError(t, err)
	require.Equal(t, SchemaEmpty, schema)

	// Report only legacy Jobs that are actively executing a physical operation.
	require.NoError(t, db.AutoMigrate(&legacyJob{}))
	require.NoError(t, db.Create([]*legacyJob{
		{ID: 1, Status: legacypb.JobStatus_JOB_PENDING},
		{ID: 2, Status: legacypb.JobStatus_PROCESSING},
		{ID: 3, Status: legacypb.JobStatus_PROCESSING},
	}).Error)
	schema, err = DetectSchema(db)
	require.NoError(t, err)
	require.Equal(t, SchemaLegacy, schema)
	ids, err := RunningJobIDs(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 3}, ids)

	// A Draft projection column cannot impersonate the released Catalog marker.
	require.NoError(t, db.Migrator().DropTable(&legacyJob{}))
	require.NoError(t, db.AutoMigrate(&executor.Job{}))
	schema, err = DetectSchema(db)
	require.ErrorIs(t, err, dataformat.ErrUnsupportedCatalog)
	require.NoError(t, dataformat.MarkCatalog(db))
	schema, err = DetectSchema(db)
	require.NoError(t, err)
	require.Equal(t, SchemaCurrent, schema)
}
