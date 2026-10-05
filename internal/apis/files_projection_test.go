package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

func TestBasicLibraryEntryKind(t *testing.T) {
	for _, test := range []struct {
		kind entity.FileKind
		want entity.EntryKind
	}{
		{entity.FileKind_FILE_KIND_REGULAR, entity.EntryKind_ENTRY_KIND_FILE},
		{entity.FileKind_FILE_KIND_DIRECTORY, entity.EntryKind_ENTRY_KIND_DIRECTORY},
		{entity.FileKind_FILE_KIND_UNSPECIFIED, entity.EntryKind_ENTRY_KIND_OTHER},
	} {
		require.Equal(t, test.want, basicLibraryEntry(&library.File{Kind: test.kind}).Kind)
	}
}

func TestFilesStatusMatrix(t *testing.T) {
	// Independent original, coverage and history facts determine the compact display projection.
	known := &library.FileLocation{Signature: []byte("known")}
	unknown := &library.FileLocation{}
	tests := []struct {
		name     string
		facts    library.FileReadFacts
		original entity.OriginalAvailability
		valid    bool
		archive  entity.FilesArchive
		coverage entity.FilesCoverage
		issues   []entity.FilesIssue
	}{
		{"covered", library.FileReadFacts{Original: known, CurrentArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_COVERED, nil},
		{"local only", library.FileReadFacts{Original: known}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, nil},
		{"changed with history", library.FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, nil},
		{"unknown with history", library.FileReadFacts{Original: unknown, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"unknown without history", library.FileReadFacts{Original: unknown}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"missing saved", library.FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"unlinked saved", library.FileReadFacts{HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"missing latest", library.FileReadFacts{Original: known, HasArchive: true, LatestUnavailable: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_LATEST_VERSION_UNAVAILABLE}},
		{"missing all copies", library.FileReadFacts{Original: known, HasVersions: true, LatestUnavailable: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_ALL_COPIES_UNAVAILABLE, entity.FilesIssue_FILES_ISSUE_LATEST_VERSION_UNAVAILABLE}},
		{"missing no history", library.FileReadFacts{Original: known}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"local bad backups", library.FileReadFacts{Original: known, HasVersions: true, HasBadCopies: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_ALL_COPIES_UNAVAILABLE}},
		{"unavailable saved", library.FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"unchecked saved", library.FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNCHECKED, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"partial bad", library.FileReadFacts{Original: known, HasArchive: true, HasBadCopies: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_PARTIAL_COPIES_UNAVAILABLE}},
		{"invalid observation does not claim coverage", library.FileReadFacts{Original: known, CurrentArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, false, entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := filesStatus(&test.facts, &executor.FileReadObservation{Availability: test.original, Valid: test.valid})
			require.Equal(t, test.original, actual.Original)
			require.Equal(t, test.archive, actual.Archive)
			require.Equal(t, test.coverage, actual.Current)
			require.Equal(t, test.issues, actual.Issues)
		})
	}
}

type filesQueryLog struct {
	logger.Interface
	queries []string
}

func (l *filesQueryLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	l.queries = append(l.queries, query)
}

func TestLocationListQueryCostIsPageBatched(t *testing.T) {
	// Capture the actual API database, including Executor metadata reads, over a real live directory.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: physical}
	require.NoError(t, lib.CreateLocation(ctx, location))
	for index := 0; index < 50; index++ {
		name := fmt.Sprintf("file-%02d", index)
		require.NoError(t, os.WriteFile(filepath.Join(physical, name), []byte("content"), 0644))
		info, err := os.Stat(filepath.Join(physical, name))
		require.NoError(t, err)
		file := &library.File{Name: name}
		require.NoError(t, lib.SaveFile(ctx, file))
		require.NoError(t, db.Create(&library.FileLocation{FileID: file.ID, LocationID: location.ID, Path: name, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano()}).Error)
	}
	exe := executor.New(db, lib, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	service := &filesService{api: New(lib, exe)}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID}}}
	trace := &filesQueryLog{Interface: logger.Discard}
	db.Logger = trace

	// One complete listing enumerates the directory once and projects one batch at a time:
	// batch size changes how the work is split, never into a query per row, and the first
	// batch leaves before the rest of the directory has been read.
	include := []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS, entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION}
	trace.queries = nil
	firstBatchQueries := 0
	server := &filesListRecorder{ctx: ctx, afterSend: func(batch int) {
		if batch == 0 {
			firstBatchQueries = len(trace.queries)
		}
	}}
	require.NoError(t, service.List(&entity.ListFilesRequest{Directory: directory, BatchSize: 1, Include: include}, server))
	require.Len(t, server.sizes, 50, "one row per batch")
	require.Equal(t, int64(50), server.first.GetTotalEntryCount(), "the first batch states the directory's total")
	require.NotNil(t, server.first.Directory, "the first batch states the directory identity")
	require.NotEmpty(t, server.first.Breadcrumbs, "the first batch states the directory's breadcrumbs")
	singleQueries := len(trace.queries)
	require.Less(t, firstBatchQueries*4, singleQueries, "the first batch must not wait for the whole directory")
	trace.queries = nil
	whole, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory, BatchSize: 50, Include: include})
	require.NoError(t, err)
	require.Len(t, whole.Entries, 50)
	batchedQueries := len(trace.queries)
	require.Less(t, batchedQueries, singleQueries, "one batch reads the metadata once, not once per row")
	require.NotContains(t, strings.ToUpper(strings.Join(trace.queries, "\n")), "COUNT(")
	t.Logf("Location list of 50 rows: one batch %d SQL statements, one row per batch %d, first batch after %d", batchedQueries, singleQueries, firstBatchQueries)

	// Basic ls bypasses associations and coverage completely, even for associated entries.
	trace.queries = nil
	bare, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory, BatchSize: 50})
	require.NoError(t, err)
	for _, row := range bare.Entries {
		require.Nil(t, row.Status)
		require.Nil(t, row.AssociatedFileId)
	}
	for _, excluded := range []string{"file_locations", "file_versions", "file_tracking_keys", "FROM positions"} {
		require.NotContains(t, strings.Join(trace.queries, "\n"), excluded)
	}
}

func TestFilesListIncludeAndRecordedLibrarySearch(t *testing.T) {
	// Annotate an actual entry, then make its retained association stale without altering Library identity.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "live.txt"), []byte("live"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}
	page, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Nil(t, page.Entries[0].SizeBytes)
	require.Nil(t, page.Entries[0].Status)
	require.Nil(t, page.Directory)
	updated, err := service.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{References: []*entity.FileOperationRef{page.Entries[0].Reference}, AddTags: []string{"example"}})
	require.NoError(t, err)
	fileID := updated.Entries[0].Entry.GetAssociatedFileId()
	require.Positive(t, fileID)
	// Admission without hashing records an association but no content identity, so the detail
	// publishes none: a Preview read has nothing to name.
	require.Empty(t, updated.Entries[0].GetContentSignature())

	// Navigation and status arrive in the list response rather than subsequent row hydration.
	page, err = listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS, entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION}})
	require.NoError(t, err)
	require.Equal(t, int64(4), page.Entries[0].GetSizeBytes())
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, page.Entries[0].Status.Original)
	require.Equal(t, entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, page.Entries[0].Status.Archive)
	require.Equal(t, "Originals", page.Directory.Name)
	require.Len(t, page.Breadcrumbs, 1)

	// Live detail can change while Library predicates retain the last published original facts.
	stale := page.Entries[0].Reference
	require.NoError(t, os.WriteFile(filepath.Join(physical, "live.txt"), []byte("replacement content"), 0644))
	refreshed, err := service.Get(ctx, &entity.GetFileRequest{Reference: stale})
	require.NoError(t, err)
	require.Equal(t, int64(19), refreshed.Detail.Entry.GetSizeBytes(), "details report the object that is there now")
	require.Equal(t, "live.txt", refreshed.Detail.ContentReference.GetLocation().Path)
	require.Empty(t, refreshed.Detail.ContentSignature, "changed content publishes no identity to read")
	libraryRoot := &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: 0}}
	changed, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: libraryRoot, Recursive: true, Scope: entity.FileScope_FILE_SCOPE_ALL, Query: "type:file AND size:4", Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}})
	require.NoError(t, err)
	require.Len(t, changed.Entries, 1, "Library size filtering uses the recorded original")
	require.Equal(t, int64(4), changed.Entries[0].GetSizeBytes())
	require.Nil(t, changed.Entries[0].Status, "predicates do not request live status")

	// Missing originals retain their associations and remain searchable while detail reports absence.
	require.NoError(t, os.Remove(filepath.Join(physical, "live.txt")))
	originals, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: libraryRoot, Recursive: true, Scope: entity.FileScope_FILE_SCOPE_ALL, Query: "has:original"})
	require.NoError(t, err)
	require.Len(t, originals.Entries, 1, "has:original queries the recorded association")
	require.Equal(t, fileID, originals.Entries[0].Reference.GetFileId())
	detail, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: fileID}}})
	require.NoError(t, err)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, detail.Detail.Entry.Status.Original)
	require.NotNil(t, detail.Detail.Original)
	require.Nil(t, detail.Detail.ContentReference)
	require.Empty(t, detail.Detail.ContentSignature)
	require.Equal(t, []string{"example"}, detail.Detail.Organization.Tags)
}

func TestLibrarySearchWithoutMatchesHasNoContinuation(t *testing.T) {
	// Catalog filtering precedes pagination, so nonmatches do not create empty continuation pages.
	api, _, _ := setupLocationAPI(t)
	ctx := context.Background()
	parent, err := api.lib.MkdirAll(ctx, 0, "bounded", 0755)
	require.NoError(t, err)
	for _, name := range []string{"a", "b", "c"} {
		_, err := api.lib.MkdirAll(ctx, parent.ID, name, 0755)
		require.NoError(t, err)
	}

	// No directory has an original; the first query has already exhausted all matching rows.
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: parent.ID}}
	first, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Scope: entity.FileScope_FILE_SCOPE_ALL, Query: "has:original", Limit: 1})
	require.NoError(t, err)
	require.Empty(t, first.Entries)
	require.Empty(t, first.NextCursor)
}
