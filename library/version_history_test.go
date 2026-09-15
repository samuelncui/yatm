package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func saveVersionAt(t *testing.T, db *gorm.DB, fileID int64, content string, date int64) *FileVersion {
	t.Helper()
	// Use the archive publication boundary so history and immutable content share a transaction.
	hash := sha256.Sum256([]byte(content))
	var version *FileVersion
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		version, err = recordVersion(tx, &FileVersion{FileID: fileID, Signature: []byte("opaque:" + content),
			Hash: hash[:], Size: int64(len(content)), Mode: 0644, FirstArchivedAt: &date, LastArchivedAt: &date})
		return err
	}))
	return version
}

func TestRestoreVersionPolicyResolvesRepeatedContentHistory(t *testing.T) {
	// Returning to A more than once must retain its middle save, not just first/last endpoints.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := &File{Name: "draft.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))
	ids := make([]int64, 0, 5)
	for index, content := range []string{"A", "B", "A", "C", "A"} {
		ids = append(ids, saveVersionAt(t, db, file.ID, content, int64(index+1)*100).ID)
	}
	for _, test := range []struct {
		cutoff, date int64
		version      int
	}{
		{100, 100, 0}, {199, 100, 0}, {200, 200, 1}, {350, 300, 2}, {450, 400, 3}, {500, 500, 4},
	} {
		t.Run(fmt.Sprint(test.cutoff), func(t *testing.T) {
			result, err := lib.ResolveRestoreVersion(ctx, file.ID, &entity.RestoreVersionPolicy{BeforeAtMs: &test.cutoff})
			require.NoError(t, err)
			require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_MATCHED, result.Match)
			require.Equal(t, ids[test.version], result.Version.Id)
			require.Equal(t, test.date, result.GetArchivedAtMs())
		})
	}

	// Repeated metadata publication is idempotent and latest selection remains unchanged.
	saveVersionAt(t, db, file.ID, "A", 300)
	var count int64
	require.NoError(t, db.Model(&FileVersionArchive{}).Count(&count).Error)
	require.EqualValues(t, 5, count)
	latest, err := lib.ResolveRestoreVersion(ctx, file.ID, nil)
	require.NoError(t, err)
	require.Equal(t, ids[4], latest.Version.Id)
	require.EqualValues(t, 500, latest.GetArchivedAtMs())
}

func TestRestoreVersionPolicyDistinguishesNoMatchReasons(t *testing.T) {
	// The waitlist retains no-history, newer-only and undated Files as distinct unresolved cases.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := &File{Name: "draft.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))
	cutoff := int64(100)
	policy := &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}
	result, err := lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.NoError(t, err)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_NO_SAVED_VERSION, result.Match)
	saveVersionAt(t, db, file.ID, "later", 200)
	result, err = lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.NoError(t, err)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_AFTER_CUTOFF, result.Match)
	require.Nil(t, result.Version)

	// Unknown timestamps neither qualify nor prove that every saved version is newer.
	undated := &FileVersion{FileID: file.ID, Signature: []byte("undated")}
	require.NoError(t, db.Create(undated).Error)
	result, err = lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.NoError(t, err)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_DATE_UNKNOWN, result.Match)
	require.Nil(t, result.Version)
	later := int64(300)
	undated.LastArchivedAt = &later
	require.NoError(t, db.Save(undated).Error)
	result, err = lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.NoError(t, err)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_DATE_UNKNOWN, result.Match)
	cutoff = 0
	_, err = lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.NoError(t, err)
	cutoff = -1
	_, err = lib.ResolveRestoreVersion(ctx, file.ID, policy)
	require.ErrorContains(t, err, "negative")
}

func TestRestoreVersionPolicyUsesOnlyEvidencedEndpointsAndStableTies(t *testing.T) {
	// Old metadata proves endpoints only; no middle occurrence may be inferred from an interval.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := &File{Name: "old-history.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))
	first, last := int64(100), int64(500)
	require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("A"), FirstArchivedAt: &first, LastArchivedAt: &last}).Error)
	b := saveVersionAt(t, db, file.ID, "B", 200)
	cutoff := int64(350)
	result, err := lib.ResolveRestoreVersion(ctx, file.ID, &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff})
	require.NoError(t, err)
	require.Equal(t, b.ID, result.Version.Id)
	require.EqualValues(t, 200, result.GetArchivedAtMs())

	// Equal observed timestamps use descending version ID, consistently with latest selection.
	c := saveVersionAt(t, db, file.ID, "C", 200)
	result, err = lib.ResolveRestoreVersion(ctx, file.ID, &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff})
	require.NoError(t, err)
	require.Equal(t, c.ID, result.Version.Id)
}

func TestVersionHistoryRoundTripAndInvalidImportRollback(t *testing.T) {
	// One version spans several composite-key pages without duplicating content rows.
	ctx := context.Background()
	db, source := newTestLibrary(t)
	file := &File{Name: "repeated.txt"}
	require.NoError(t, source.SaveFile(ctx, file))
	for index := 1; index <= 2*batchSize+1; index++ {
		saveVersionAt(t, db, file.ID, "A", int64(index))
	}
	var expected []*FileVersionArchive
	require.NoError(t, db.Order("version_id, archived_at").Find(&expected).Error)
	var snapshot bytes.Buffer
	require.NoError(t, source.Export(ctx, &snapshot, []entity.LibraryEntityType{entity.LibraryEntityType_FILE}))
	targetDB, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(snapshot.Bytes())))
	var actual []*FileVersionArchive
	require.NoError(t, targetDB.Order("version_id, archived_at").Find(&actual).Error)
	require.Equal(t, expected, actual)

	// A dangling record after valid pages aborts the whole replacement, including prior history.
	invalid := bytes.TrimSuffix(snapshot.Bytes(), []byte("{\"type\":\"end\"}\n"))
	var broken bytes.Buffer
	broken.Write(invalid)
	encoder := json.NewEncoder(&broken)
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeFileVersionArchive, Data: &FileVersionArchive{VersionID: 99999, ArchivedAt: 300}}))
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeEnd}))
	require.ErrorContains(t, target.Import(ctx, &broken), "archive observation version")
	actual = nil
	require.NoError(t, targetDB.Order("version_id, archived_at").Find(&actual).Error)
	require.Equal(t, expected, actual)

	// A File-only replacement without saved content cannot leave history referencing reused IDs.
	var replacement bytes.Buffer
	encoder = json.NewEncoder(&replacement)
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeHeader, Format: jsonlFormat, Version: jsonlFormatVersion, Entities: []string{recordTypeFile}}))
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeFile, Data: &File{ID: file.ID, Name: "replacement.txt"}}))
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeEnd}))
	require.NoError(t, target.Import(ctx, &replacement))
	var count int64
	require.NoError(t, targetDB.Model(&FileVersionArchive{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestVersionHistoryArchiveRetryRollsBackUnpublishedDates(t *testing.T) {
	// Commit through the real inventory publication transaction, not a timestamp-only test write.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := &File{Name: "content.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))
	hash := sha256.Sum256([]byte("content"))
	input := func(_ context.Context, yield func(*MediaFile) error) error {
		return yield(&MediaFile{Path: "content.txt", Hash: hash[:], Size: 7,
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: []byte("opaque"), Sha256: hash[:], Size: 7, Mode: 0644}})
	}
	media, err := lib.CommitMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()}, input)
	require.NoError(t, err)
	var before []*FileVersionArchive
	require.NoError(t, db.Order("version_id, archived_at").Find(&before).Error)
	require.Len(t, before, 1)

	// The existing Position rejects a second publication; history rolls back with that transaction.
	_, err = lib.CommitMedia(ctx, media, input)
	require.Error(t, err)
	var after []*FileVersionArchive
	require.NoError(t, db.Order("version_id, archived_at").Find(&after).Error)
	require.Equal(t, before, after)
}

func TestVersionHistoryRestoreKeepsSourceDates(t *testing.T) {
	// Restoring an independent File carries known source dates, never the output's restore time.
	ctx := context.Background()
	db, lib, _, _, publication := restorePublicationFixture(t)
	publication.Reconnect = false
	result, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	version, err := lib.LatestFileVersion(ctx, result.ResultFileID)
	require.NoError(t, err)
	var observed []*FileVersionArchive
	require.NoError(t, db.Where("version_id = ?", version.ID).Find(&observed).Error)
	require.Equal(t, []*FileVersionArchive{{VersionID: version.ID, ArchivedAt: 1234}}, observed)
	require.NotEqual(t, result.RestoredAt, observed[0].ArchivedAt)
}
