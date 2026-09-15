package library

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type preAnnotationFile struct {
	ID   int64 `gorm:"primaryKey;autoIncrement"`
	Name string
}

func (*preAnnotationFile) TableName() string { return "files" }

func TestLibraryRuntimeSchemaContainsOnlyDeclaredTables(t *testing.T) {
	// Read the concrete runtime schema after the additive migrations.
	db, _ := newTestLibrary(t)
	var tables []string
	require.NoError(t, db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	).Scan(&tables).Error)

	// Online catalog tables are Library-owned; execution manifests remain in Job bundles.
	require.Equal(t, []string{"catalog_metadata", "file_locations", "file_operation_results", "file_tags", "file_tracking_keys", "file_version_archives", "file_versions", "files", "library_settings", "location_migrations", "locations", "media", "positions", "restore_results"}, tables)
	require.True(t, db.Migrator().HasColumn(ModelFile, "note"))
	for _, index := range []string{"idx_files_search_name"} {
		require.True(t, db.Migrator().HasIndex(ModelFile, index), index)
	}
	require.True(t, db.Migrator().HasIndex(ModelFileTag, "idx_file_tags_tag_file"))
}

func TestLibraryAutoMigrateRejectsIncompatibleDraftWithoutReplacingFiles(t *testing.T) {
	db := openTestLibraryDB(t)
	require.NoError(t, db.AutoMigrate(new(preAnnotationFile)))
	require.NoError(t, db.Create(&preAnnotationFile{ID: 7, Name: "kept.txt"}).Error)

	require.ErrorIs(t, New(db).AutoMigrate(), dataformat.ErrUnsupportedCatalog)
	stored := new(preAnnotationFile)
	require.NoError(t, db.First(stored, 7).Error)
	require.Equal(t, "kept.txt", stored.Name)
	require.False(t, db.Migrator().HasTable(ModelFileTag))
}

func TestTrimPositionsKeepsVolumeMedia(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	volume, err := lib.CreateMedia(ctx, &Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "d2719f91-4f8f-4e55-b06e-6b3392b33197",
		Name: "offline", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack(),
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: volume.ID, Path: "kept.txt"}).Error)
	require.NoError(t, db.Create(&Position{MediaID: volume.ID + 100, Path: "orphan.txt"}).Error)

	require.NoError(t, lib.Trim(ctx, true, false))
	var paths []string
	require.NoError(t, db.Model(&Position{}).Order("path").Pluck("path", &paths).Error)
	require.Equal(t, []string{"kept.txt"}, paths)
}

func TestGetMediaByIdentityTreatsMissingMediaAsExpected(t *testing.T) {
	// Capture GORM diagnostics while querying an identity that has not been registered.
	db, _ := newTestLibrary(t)
	output := new(bytes.Buffer)
	db = db.Session(&gorm.Session{Logger: logger.New(
		log.New(output, "", 0), logger.Config{LogLevel: logger.Info},
	)})
	lib := New(db)

	// A normal miss returns nil without producing a record-not-found error log.
	media, err := lib.GetMediaByIdentity(context.Background(), entity.MediaKind_MEDIA_KIND_TAPE, "ABC001")
	require.NoError(t, err)
	require.Nil(t, media)
	require.False(t, strings.Contains(strings.ToLower(output.String()), "record not found"), output.String())
}
