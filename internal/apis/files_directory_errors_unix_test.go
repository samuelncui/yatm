//go:build darwin || linux || freebsd

package apis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFilesListNonUTF8ChildrenAndStrictConsumers(t *testing.T) {
	// Real invalid-byte children coexist with a literal name identical to an error's display text.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	bad := "bad-\xff"
	for _, name := range []string{bad, strconv.Quote(bad), "good"} {
		err := os.WriteFile(filepath.Join(physical, name), []byte("content"), 0644)
		if errors.Is(err, syscall.EILSEQ) {
			t.Skip("host filesystem rejects invalid UTF-8 names; injected observation/projection tests cover this case")
		}
		require.NoError(t, err)
	}
	require.NoError(t, os.Mkdir(filepath.Join(physical, "folder-\xff"), 0755))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	service := &filesService{api: api}
	ref := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}

	// Minimal and fully projected List both retain exact totals and safe, non-actionable failures.
	for _, include := range [][]entity.FilesInclude{nil, {
		entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
	}} {
		reply, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: ref, BatchSize: 2, Include: include})
		require.NoError(t, err)
		require.EqualValues(t, 4, reply.GetTotalEntryCount())
		require.Len(t, reply.Entries, 4)
		failed, usable, sameDisplay := 0, 0, 0
		for _, entry := range reply.Entries {
			if entry.Name == strconv.Quote(bad) {
				sameDisplay++
			}
			if entry.Error == "" {
				usable++
				require.NotNil(t, entry.Reference)
				require.Equal(t, entry.Name, entry.Reference.GetLocation().Path)
				continue
			}
			failed++
			require.Contains(t, entry.Error, "UTF-8")
			require.Nil(t, entry.Reference)
			require.Nil(t, entry.SizeBytes)
			require.Nil(t, entry.MtimeNs)
			require.Nil(t, entry.AssociatedFileId)
			require.Nil(t, entry.Status)
			require.Empty(t, entry.Operations)
			if entry.Name == strconv.Quote("folder-\xff") {
				require.Equal(t, entity.EntryKind_ENTRY_KIND_DIRECTORY, entry.Kind)
			}
		}
		require.Equal(t, 2, failed)
		require.Equal(t, 2, usable)
		require.Equal(t, 2, sameDisplay)
		_, err = proto.Marshal(reply)
		require.NoError(t, err)
	}

	// Search cannot decide predicates by silently dropping a child whose facts are unavailable.
	search, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref, Query: "size:>0"})
	require.ErrorContains(t, err, "UTF-8")
	require.Nil(t, search)

	// Measure retains its explicit incomplete summary and never claims a completed total.
	stream := &measureTestStream{ctx: ctx}
	require.NoError(t, service.Measure(&entity.MeasureFilesRequest{Directory: ref, Query: "size:>0"}, stream))
	require.NotEmpty(t, stream.updates)
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.NotNil(t, summary)
	require.False(t, summary.Complete)
	require.Contains(t, summary.Error, "UTF-8")
}

func TestDirectoryChildErrorsFailSearchAndMeasurement(t *testing.T) {
	// Read permission allows enumeration while missing directory search permission prevents child stat.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the child-stat permission fixture")
	}
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "unreadable"), []byte("content"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chmod(physical, 0755)) })
	require.NoError(t, os.Chmod(physical, 0400))
	service := &filesService{api: api}
	ref := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}

	// List exposes the unreadable child, while a predicate query cannot silently omit it.
	reply, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: ref})
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.GetTotalEntryCount())
	require.Len(t, reply.Entries, 1)
	require.Contains(t, reply.Entries[0].Error, "permission denied")
	require.Equal(t, "unreadable", reply.Entries[0].Name)
	require.Nil(t, reply.Entries[0].Reference)
	search, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref, Query: "size:>0"})
	require.ErrorContains(t, err, "permission denied")
	require.Nil(t, search)

	// The existing measurement failure channel reports an incomplete total with its cause.
	stream := &measureTestStream{ctx: ctx}
	require.NoError(t, service.Measure(&entity.MeasureFilesRequest{Directory: ref, Query: "size:>0"}, stream))
	require.NotEmpty(t, stream.updates)
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.NotNil(t, summary)
	require.False(t, summary.Complete)
	require.Contains(t, summary.Error, "permission denied")
}

func TestFilesListTimestampOverflowAndStrictConsumers(t *testing.T) {
	// Read a real out-of-range file timestamp alongside valid files, including a known epoch zero.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	for _, file := range []struct {
		name  string
		mtime time.Time
	}{
		{"a-before", time.Unix(-1, 123456789)},
		{"b-invalid", time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"z-epoch", time.Unix(0, 0)},
	} {
		full := filepath.Join(physical, file.name)
		require.NoError(t, os.WriteFile(full, []byte("content"), 0644))
		// os.Chtimes converts through UnixNano and would wrap the very timestamp under test.
		stamp := syscall.Timespec{Sec: file.mtime.Unix(), Nsec: int64(file.mtime.Nanosecond())}
		require.NoError(t, syscall.UtimesNano(full, []syscall.Timespec{stamp, stamp}))
	}
	info, err := os.Stat(filepath.Join(physical, "b-invalid"))
	require.NoError(t, err)
	if _, err := dataformat.Nanoseconds(info.ModTime()); err == nil {
		t.Skipf("host filesystem cannot retain the year-2500 fixture; observed %s", info.ModTime().UTC().Format(time.RFC3339Nano))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{
		Location: &entity.Location{Name: "Originals", RootPath: physical},
	})
	require.NoError(t, err)
	service := &filesService{api: api}
	ref := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{
		Location: &entity.LocationEntryRef{LocationId: created.Location.Id},
	}}

	// List keeps every row without a fabricated timestamp or actionable reference on failure.
	reply, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: ref, BatchSize: 1,
		Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_OPERATIONS}})
	require.NoError(t, err)
	require.EqualValues(t, 3, reply.GetTotalEntryCount())
	require.Len(t, reply.Entries, 3)
	bad := reply.Entries[1]
	require.Equal(t, "b-invalid", bad.Name)
	require.Contains(t, bad.Error, "outside the signed Unix nanosecond range")
	require.Nil(t, bad.Reference)
	require.Nil(t, bad.MtimeNs)
	require.Nil(t, bad.SizeBytes)
	require.Empty(t, bad.Operations)
	for _, index := range []int{0, 2} {
		entry := reply.Entries[index]
		info, err := os.Stat(filepath.Join(physical, entry.Path))
		require.NoError(t, err)
		mtime, err := dataformat.Nanoseconds(info.ModTime())
		require.NoError(t, err)
		require.Equal(t, &mtime, entry.MtimeNs)
		require.NotNil(t, entry.Reference)
		require.Empty(t, entry.Error)
	}
	require.Equal(t, proto.Int64(0), reply.Entries[2].MtimeNs)

	// Search and Measure keep their established failure channels instead of silently omitting the child.
	search, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref, Query: "size:>0"})
	require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
	require.Nil(t, search)
	stream := &measureTestStream{ctx: ctx}
	require.NoError(t, service.Measure(&entity.MeasureFilesRequest{Directory: ref, Query: "size:>0"}, stream))
	require.NotEmpty(t, stream.updates)
	summary := stream.updates[len(stream.updates)-1].GetSummary()
	require.NotNil(t, summary)
	require.False(t, summary.Complete)
	require.Contains(t, summary.Error, "outside the signed Unix nanosecond range")
}
