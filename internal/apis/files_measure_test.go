package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type measureTestStream struct {
	grpc.ServerStream
	ctx     context.Context
	updates []*entity.MeasureFilesResponse
	onSend  func()
}

func setupMeasureAPI(t *testing.T) (*API, *gorm.DB) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.New(db, lib, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	return New(lib, exe), db
}

func (s *measureTestStream) Context() context.Context { return s.ctx }
func (s *measureTestStream) Send(update *entity.MeasureFilesResponse) error {
	s.updates = append(s.updates, update)
	if s.onSend != nil {
		s.onSend()
	}
	return nil
}

func TestMeasureLibraryAllPagesOverlapAndUnknown(t *testing.T) {
	// Populate more than one measurement page, keeping a saved-only file and unknown facts distinct.
	api, db := setupMeasureAPI(t)
	parent := &library.File{Name: "match-parent", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, api.lib.SaveFile(context.Background(), parent))
	child := &library.File{Name: "match-child", ParentID: parent.ID, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, api.lib.SaveFile(context.Background(), child))
	for index := 0; index < 260; index++ {
		file := &library.File{Name: fmt.Sprintf("match-%03d", index), ParentID: child.ID}
		require.NoError(t, api.lib.SaveFile(context.Background(), file))
		require.NoError(t, db.Create(&library.FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprint(index)), Size: 2}).Error)
	}
	unknown := &library.File{Name: "outside-filter", ParentID: child.ID}
	require.NoError(t, api.lib.SaveFile(context.Background(), unknown))
	stream := &measureTestStream{ctx: context.Background()}

	// Matching parent and descendants are each streamed, but their bytes are counted once globally.
	require.NoError(t, (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{
		Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: parent.ID}},
		Query:     "name:match-*", Recursive: true, Scope: entity.FileScope_FILE_SCOPE_ALL,
	}, stream))
	require.Len(t, stream.updates, 262)
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.EqualValues(t, 520, summary.KnownBytes)
	require.False(t, summary.Complete, "unmatched descendants of matching folders still contribute unknown facts")
	require.Equal(t, "size is unknown", summary.Error)
	for _, update := range stream.updates {
		if update.GetItem().GetReference().GetFileId() == child.ID {
			require.EqualValues(t, 520, update.GetItem().KnownBytes)
			require.False(t, update.GetItem().Complete)
		}
	}
}

func TestMeasureLibraryAttributesAreBatchedAndPure(t *testing.T) {
	// Measure uses original facts before saved versions and never reads physical originals or previews.
	api, db := setupMeasureAPI(t)
	for index := 0; index < 30; index++ {
		file := &library.File{Name: fmt.Sprintf("file-%02d", index)}
		require.NoError(t, api.lib.SaveFile(context.Background(), file))
		require.NoError(t, db.Create(&library.FileLocation{FileID: file.ID, LocationID: 100, Path: file.Name, Size: 3}).Error)
		require.NoError(t, db.Create(&library.FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprint(index)), Size: 100}).Error)
	}
	trace := &filesQueryLog{Interface: logger.Discard}
	db.Logger = trace
	stream := &measureTestStream{ctx: context.Background()}
	require.NoError(t, (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{
		Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}, Query: "name:file-*", Scope: entity.FileScope_FILE_SCOPE_ALL,
	}, stream))

	// Query count grows by page, not by row; reads do not enter status, copy or preview pipelines.
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.True(t, summary.Complete)
	require.EqualValues(t, 90, summary.KnownBytes)
	require.Less(t, len(trace.queries), 15)
	for _, query := range trace.queries {
		lower := strings.ToLower(query)
		require.NotContains(t, lower, "positions")
		require.NotContains(t, lower, "preview")
		require.NotContains(t, lower, "insert ")
		require.NotContains(t, lower, "update ")
	}
}

func TestMeasureLocationExcludesIgnoredContentsWithoutFollowingLinks(t *testing.T) {
	// Current-directory filters select roots; Location Ignore excludes entries while dot files remain.
	api, locations, root := setupLocationAPI(t)
	physical := filepath.Join(root, "source")
	require.NoError(t, os.MkdirAll(filepath.Join(physical, "match", "ignored"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "match", "ignored", "data"), []byte("12345"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "match", ".hidden"), []byte("123"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "other"), []byte("123456789"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(physical, "other"), filepath.Join(physical, "match", "link")))
	created, err := locations.Create(context.Background(), &entity.CreateLocationRequest{Location: &entity.Location{
		Name: "Source", RootPath: physical, Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "ignored/"}},
	}})
	require.NoError(t, err)
	stream := &measureTestStream{ctx: context.Background()}
	require.NoError(t, (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{Query: "name:match", Directory: &entity.FileOperationRef{
		Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}},
	}}, stream))

	// No File is admitted, ignored bytes stay out, and linked bytes do not inflate the logical sum.
	require.Len(t, stream.updates, 2)
	require.True(t, stream.updates[1].GetSummary().Complete)
	require.EqualValues(t, 3, stream.updates[1].GetSummary().KnownBytes)
	originals, err := api.lib.LocationOriginalsPage(context.Background(), created.Location.Id, "", 10)
	require.NoError(t, err)
	require.Empty(t, originals)

	// A measured root is Location content too, so an ignored root is an explicit error.
	err = (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{Directory: &entity.FileOperationRef{
		Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "match/ignored"}},
	}}, &measureTestStream{ctx: context.Background()})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestMeasureCancellationDoesNotPublishFinalSummary(t *testing.T) {
	// Cancel after a streamed result to ensure no completed summary can follow a canceled traversal.
	api, _ := setupMeasureAPI(t)
	file := &library.File{Name: "unknown"}
	require.NoError(t, api.lib.SaveFile(context.Background(), file))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &measureTestStream{ctx: ctx, onSend: cancel}
	err := (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{
		Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}, Scope: entity.FileScope_FILE_SCOPE_ALL,
	}, stream)
	require.ErrorIs(t, err, context.Canceled)
	for _, update := range stream.updates {
		require.Nil(t, update.GetSummary())
	}
}

func TestMeasureLocationConfigurationCostDoesNotGrowPerDirectory(t *testing.T) {
	// A request prepares registration boundaries once, even when traversing many directories.
	api, db := setupMeasureAPI(t)
	physical := filepath.Join(api.exe.Paths().Access[0].Root, "originals")
	for index := 0; index < 25; index++ {
		directory := filepath.Join(physical, fmt.Sprintf("folder-%02d", index))
		require.NoError(t, os.MkdirAll(directory, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "file"), []byte("123"), 0644))
	}
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: physical}
	require.NoError(t, api.lib.CreateLocation(context.Background(), location))
	trace := &filesQueryLog{Interface: logger.Discard}
	db.Logger = trace
	stream := &measureTestStream{ctx: context.Background()}
	require.NoError(t, (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{Directory: &entity.FileOperationRef{
		Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID}},
	}}, stream))

	// Physical enumeration is proportional to files, but database configuration reads stay request-bounded.
	configurationQueries := 0
	for _, query := range trace.queries {
		if strings.Contains(strings.ToLower(query), "locations") {
			configurationQueries++
		}
	}
	require.LessOrEqual(t, configurationQueries, 3, trace.queries)
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.True(t, summary.Complete)
	require.EqualValues(t, 75, summary.KnownBytes)
}

func TestMeasureLibraryDeepTreeAndEmptyRoots(t *testing.T) {
	// Preserve the old Data Usage deep-tree and empty-folder behavior through its new public entry point.
	api, db := setupMeasureAPI(t)
	root := &library.File{Name: "root", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	empty := &library.File{Name: "empty", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, api.lib.SaveFile(context.Background(), root))
	require.NoError(t, api.lib.SaveFile(context.Background(), empty))
	parent := root.ID
	for index := 0; index < 256; index++ {
		directory := &library.File{Name: "nested", ParentID: parent, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
		require.NoError(t, api.lib.SaveFile(context.Background(), directory))
		parent = directory.ID
	}
	file := &library.File{Name: "leaf", ParentID: parent}
	require.NoError(t, api.lib.SaveFile(context.Background(), file))
	require.NoError(t, db.Create(&library.FileVersion{FileID: file.ID, Signature: []byte("saved"), Size: 17}).Error)
	stream := &measureTestStream{ctx: context.Background()}
	require.NoError(t, (&filesService{api: api}).Measure(&entity.MeasureFilesRequest{
		Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}, Scope: entity.FileScope_FILE_SCOPE_ALL,
	}, stream))

	// Direct roots retain their totals without publishing every descendant as a selection root.
	require.Len(t, stream.updates, 3)
	require.EqualValues(t, empty.ID, stream.updates[0].GetItem().GetReference().GetFileId())
	require.Zero(t, stream.updates[0].GetItem().KnownBytes)
	require.True(t, stream.updates[0].GetItem().Complete)
	require.EqualValues(t, 17, stream.updates[1].GetItem().KnownBytes)
	require.EqualValues(t, 17, stream.updates[2].GetSummary().KnownBytes)
	require.True(t, stream.updates[2].GetSummary().Complete)
}
