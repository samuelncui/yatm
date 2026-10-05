package legacy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func archivedVersionFixture(t testing.TB, count int) *gorm.DB {
	// Use on-disk SQLite so migration benchmarks include real commit costs.
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "versions.db"))
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, db.AutoMigrate(&stagedLibraryFile{}, &stagedLibraryPosition{}, &stagedLibraryVersion{}))

	// Put each File's second physical copy in a later page; it must not create another version.
	files := make([]*stagedLibraryFile, 0, count)
	copies := make([]*stagedLibraryPosition, 0, 2*count)
	for index := 1; index <= count; index++ {
		name := fmt.Sprintf("file-%06d", index)
		hash := sha256.Sum256([]byte(name))
		signature, err := library.NewFileSignature(hash[:], int64(index))
		require.NoError(t, err)
		files = append(files, &stagedLibraryFile{
			ID: int64(index), Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR,
			Mode: 0o640, MtimeNS: 1234567890123456789, Hash: hash[:], Size: int64(index), Signature: signature,
		})
		for medium := 1; medium <= 2; medium++ {
			copies = append(copies, &stagedLibraryPosition{
				ID: int64((medium-1)*count + index), FileID: int64(index), MediaID: int64(medium), Path: name,
				Mode: 0o644, MtimeNS: 1234567890000000000, Hash: hash[:], Size: int64(index),
			})
		}
	}
	require.NoError(t, db.CreateInBatches(files, 256).Error)
	require.NoError(t, db.CreateInBatches(copies, 256).Error)
	return db
}

func TestPrepareArchivedVersionsAcrossPages(t *testing.T) {
	// Include matching and older content, plus an unowned physical copy.
	db := archivedVersionFixture(t, 257)
	hash := sha256.Sum256([]byte("older content"))
	require.NoError(t, db.Create([]*stagedLibraryPosition{
		{ID: 600, FileID: 1, MediaID: 3, Path: "old", Size: 13, Hash: hash[:], Mode: 0o600, MtimeNS: 123},
		{ID: 601, MediaID: 3, Path: "unowned", Size: 13, Hash: hash[:]},
	}).Error)
	var before []*stagedLibraryFile
	require.NoError(t, db.Order("id").Find(&before).Error)

	// Matching copies use File metadata; historical content keeps the copy's metadata.
	report := &Report{}
	require.NoError(t, prepareArchivedVersions(context.Background(), db, report))
	require.Empty(t, report.Warnings)
	var versions []*stagedLibraryVersion
	require.NoError(t, db.Order("id").Find(&versions).Error)
	require.Len(t, versions, 258)
	for index, version := range versions[:257] {
		require.Equal(t, before[index].ID, version.FileID)
		require.Equal(t, before[index].Signature, version.Signature)
		require.Equal(t, before[index].Mode, version.Mode)
		require.Equal(t, before[index].MtimeNS, version.MtimeNS)
	}
	require.Equal(t, int64(1), versions[257].FileID)
	require.Equal(t, uint32(0o600), versions[257].Mode)
	require.Equal(t, int64(123), versions[257].MtimeNS)

	// Every physical copy retains its evidence and gets a signature without altering File facts.
	var copies []*stagedLibraryPosition
	require.NoError(t, db.Order("id").Find(&copies).Error)
	require.Len(t, copies, 516)
	for _, copy := range copies {
		signature, err := library.NewFileSignature(copy.Hash, copy.Size)
		require.NoError(t, err)
		require.Equal(t, signature, copy.Signature)
	}
	var after []*stagedLibraryFile
	require.NoError(t, db.Order("id").Find(&after).Error)
	require.Equal(t, before, after)
}

func TestPrepareArchivedVersionsRejectsConflictingCopyFacts(t *testing.T) {
	for _, copies := range []int{2, 257} {
		t.Run(fmt.Sprint(copies), func(t *testing.T) {
			// A legacy opaque signature cannot reconcile physically different hashes.
			db := archivedVersionFixture(t, 1)
			require.NoError(t, db.Model(&stagedLibraryFile{}).Where("id = ?", 1).Update("hash", nil).Error)
			if copies > 2 {
				rows := make([]*stagedLibraryPosition, 0, copies-2)
				for id := 3; id <= copies; id++ {
					rows = append(rows, &stagedLibraryPosition{
						ID: int64(id), FileID: 1, MediaID: int64(id), Path: "file-000001", Size: 1,
					})
				}
				require.NoError(t, db.CreateInBatches(rows, 256).Error)
				require.NoError(t, db.Model(&stagedLibraryPosition{}).Where("id > ?", 2).
					Update("hash", gorm.Expr("(SELECT hash FROM positions_staging WHERE id = 1)")).Error)
			}
			hash := sha256.Sum256([]byte("conflicting content"))
			require.NoError(t, db.Model(&stagedLibraryPosition{}).Where("id = ?", copies).Update("hash", hash[:]).Error)

			// The same rejection applies within one page and across a page boundary.
			err := prepareArchivedVersions(context.Background(), db, &Report{})
			require.ErrorContains(t, err, "legacy archived facts disagree, File 1")
		})
	}
}

func BenchmarkPrepareArchivedVersions(b *testing.B) {
	for index := 0; index < b.N; index++ {
		// Each operation consumes fresh staging tables; setup and result checks are not timed.
		b.StopTimer()
		db := archivedVersionFixture(b, 10000)
		report := &Report{}
		b.StartTimer()
		err := prepareArchivedVersions(context.Background(), db, report)
		b.StopTimer()

		// Count saved versions to prevent a faster incomplete migration from passing.
		require.NoError(b, err)
		require.Empty(b, report.Warnings)
		var count int64
		require.NoError(b, db.Model(&stagedLibraryVersion{}).Count(&count).Error)
		require.Equal(b, int64(10000), count)
	}
}
