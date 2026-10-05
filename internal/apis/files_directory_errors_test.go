package apis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLocationDirectoryErrorProjection(t *testing.T) {
	// Valid UTF-8 keeps its literal identity; invalid bytes alone need display escaping.
	for _, name := range []string{"bad-\xff", `bad-\xff`, "line\nname", `line\nname`} {
		for _, kind := range []struct {
			mode os.FileMode
			want entity.EntryKind
		}{
			{0, entity.EntryKind_ENTRY_KIND_UNSPECIFIED},
			{os.ModeDir, entity.EntryKind_ENTRY_KIND_DIRECTORY},
			{os.ModeSymlink, entity.EntryKind_ENTRY_KIND_LINK},
			{os.ModeNamedPipe, entity.EntryKind_ENTRY_KIND_OTHER},
		} {
			entry := locationDirectoryError(executor.LocationDirectoryEntry{
				Path: "parent/" + name, Type: kind.mode, Error: errors.New("cannot stat " + name),
			})
			if utf8.ValidString(name) {
				require.Equal(t, name, entry.Name)
				require.Equal(t, "parent/"+name, entry.Path)
			} else {
				require.Equal(t, strconv.Quote(name), entry.Name)
				require.Equal(t, strconv.Quote("parent/"+name), entry.Path)
			}
			require.Equal(t, kind.want, entry.Kind)
			require.True(t, utf8.ValidString(entry.Error))
			require.NotContains(t, entry.Error, "\n")
			require.Nil(t, entry.Reference)
			require.Nil(t, entry.SizeBytes)
			require.Nil(t, entry.MtimeNs)
			require.Nil(t, entry.AssociatedFileId)
			require.Nil(t, entry.Status)
			require.Empty(t, entry.Operations)
			_, err := proto.Marshal(entry)
			require.NoError(t, err)
		}
	}
}

func TestLocationDirectoryTimestampErrorKeepsUsableRows(t *testing.T) {
	// A physical observation reports its conversion error before producing any actionable reference.
	info, err := (fstest.MapFS{".invalid-time": {ModTime: time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)}}).Stat(".invalid-time")
	require.NoError(t, err)
	facts, failure := executor.InspectLocationFacts(info)
	require.ErrorContains(t, failure, "outside the signed Unix nanosecond range")
	require.Nil(t, facts)

	// The directory projection retains usable neighbors when the production observation rejects the child.
	rows := []executor.LocationDirectoryEntry{
		{Path: "before", Entry: &entity.LocationEntry{Path: "before", Reference: &entity.LocationEntryRef{
			LocationId: 1, Path: "before", Facts: &entity.LocationFileFacts{MtimeNs: -1},
		}}},
		{Path: ".invalid-time", Error: failure},
		{Path: "after", Entry: &entity.LocationEntry{Path: "after", Reference: &entity.LocationEntryRef{
			LocationId: 1, Path: "after", Facts: &entity.LocationFileFacts{MtimeNs: 0},
		}}},
	}
	entries, err := new(filesService).locationDirectoryEntries(context.Background(), rows,
		filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true}, nil, nil)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// Failed metadata stays absent, while a known epoch zero remains present on the later row.
	require.Equal(t, ".invalid-time", entries[1].Name)
	require.Contains(t, entries[1].Error, "outside the signed Unix nanosecond range")
	require.Nil(t, entries[1].Reference)
	require.Nil(t, entries[1].MtimeNs)
	require.Nil(t, entries[1].SizeBytes)
	require.Empty(t, entries[1].Operations)
	require.Equal(t, proto.Int64(-1), entries[0].MtimeNs)
	require.Equal(t, proto.Int64(0), entries[2].MtimeNs)
	require.NotNil(t, entries[0].Reference)
	require.NotNil(t, entries[2].Reference)
}

func TestFilesListRetainsUnreadableChildAndLaterRows(t *testing.T) {
	// Observe real files in separate batches, injecting a stat failure after enumeration is complete.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	for _, name := range []string{"a-readable", "b-unreadable", "z-readable"} {
		require.NoError(t, os.WriteFile(filepath.Join(physical, name), []byte("content"), 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}
	stream := &filesListRecorder{ctx: ctx, afterSend: func(batch int) {
		if batch == 0 {
			require.NoError(t, os.Remove(filepath.Join(physical, "b-unreadable")))
		}
	}}
	include := []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION}
	require.NoError(t, service.List(&entity.ListFilesRequest{Directory: directory, BatchSize: 1, Include: include}, stream))

	// The error row counts toward the complete directory and cannot expose any physical action.
	require.EqualValues(t, 3, stream.first.GetTotalEntryCount())
	require.Equal(t, []int{1, 1, 1}, stream.sizes)
	rows := stream.reply.Entries
	require.Equal(t, []string{"a-readable", "b-unreadable", "z-readable"}, []string{rows[0].Name, rows[1].Name, rows[2].Name})
	require.Contains(t, rows[1].Error, "b-unreadable")
	require.Nil(t, rows[1].Reference)
	require.Nil(t, rows[1].SizeBytes)
	require.Nil(t, rows[1].MtimeNs)
	require.Nil(t, rows[1].Status)
	require.Empty(t, rows[1].Operations)
	for _, index := range []int{0, 2} {
		require.Empty(t, rows[index].Error)
		require.EqualValues(t, 7, rows[index].GetSizeBytes())
		require.NotNil(t, rows[index].MtimeNs)
		info, err := os.Stat(filepath.Join(physical, rows[index].Path))
		require.NoError(t, err)
		mtime, err := dataformat.Nanoseconds(info.ModTime())
		require.NoError(t, err)
		require.Equal(t, mtime, rows[index].GetMtimeNs())
		require.Contains(t, rows[index].Operations, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE)
		detail, err := service.Get(ctx, &entity.GetFileRequest{Reference: rows[index].Reference})
		require.NoError(t, err)
		require.Equal(t, rows[index].Name, detail.Detail.Entry.Name)
	}
	_, err = proto.Marshal(stream.reply)
	require.NoError(t, err)
}

func TestFilesListDirectoryAndTransportFailures(t *testing.T) {
	// Parent and send failures remain request failures rather than synthetic child rows.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "file"), nil, 0600))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	service := &filesService{api: api}
	for _, parent := range []string{"missing", "file"} {
		stream := &filesListRecorder{ctx: ctx}
		ref := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: parent}}}
		require.Error(t, service.List(&entity.ListFilesRequest{Directory: ref}, stream))
		require.Empty(t, stream.sizes)
	}

	// A disconnected stream's cause takes precedence over otherwise usable directory contents.
	failure := errors.New("client disconnected")
	stream := &failedFilesListStream{filesListRecorder: filesListRecorder{ctx: ctx}, failure: failure}
	ref := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}
	require.ErrorIs(t, service.List(&entity.ListFilesRequest{Directory: ref}, stream), failure)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	cancelled := &filesListRecorder{ctx: ctx}
	require.Error(t, service.List(&entity.ListFilesRequest{Directory: ref}, cancelled))
	require.Empty(t, cancelled.sizes)
}

type failedFilesListStream struct {
	filesListRecorder
	failure error
}

func (s *failedFilesListStream) Send(*entity.ListFilesResponse) error { return s.failure }
