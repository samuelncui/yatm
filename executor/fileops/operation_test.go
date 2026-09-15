package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type fixture struct {
	exe               *executor.Executor
	location          *library.Location
	libDB, executorDB *gorm.DB
	root, work        string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	// Real operations use only an isolated original directory and temporary databases.
	base := t.TempDir()
	root, work := filepath.Join(base, "originals"), filepath.Join(base, "work")
	require.NoError(t, os.Mkdir(root, 0755))
	executorDB, err := resource.OpenSQLite(filepath.Join(base, "executor.db"))
	require.NoError(t, err)
	libDB, err := resource.OpenSQLite(filepath.Join(base, "library.db"))
	require.NoError(t, err)
	for _, db := range []*gorm.DB{libDB, executorDB} {
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	}
	lib := library.New(libDB)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.New(executorDB, lib, nil, executor.Paths{Work: work, Access: []executor.AccessRange{{Root: root}}}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	root, err = exe.OnlineRoot(root)
	require.NoError(t, err)
	location := &library.Location{Name: "Documents", ExecutorID: "local", RootPath: root}
	require.NoError(t, lib.CreateOnlineSource(context.Background(), location))
	f := &fixture{exe: exe, location: location, libDB: libDB, executorDB: executorDB, root: root, work: work}
	t.Cleanup(func() {
		var count int64
		require.NoError(t, executorDB.Model(&executor.Job{}).Count(&count).Error)
		require.Zero(t, count, "ordinary file operations must never create catalog Jobs")
		entries, err := os.ReadDir(work)
		if os.IsNotExist(err) {
			return
		}
		require.NoError(t, err)
		require.Empty(t, entries, "ordinary file operations must not create Job bundles")
	})
	return f
}

func (f *fixture) ref(t *testing.T, name string) *entity.FileOperationRef {
	t.Helper()
	entry, err := f.exe.ObserveLocationEntry(context.Background(), f.location.ID, name)
	require.NoError(t, err)
	return &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: entry.Reference}}
}

func (f *fixture) write(t *testing.T, name, content string) {
	t.Helper()
	full := filepath.Join(f.root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))
}

func (f *fixture) original(t *testing.T, name string) *library.File {
	t.Helper()
	file := &library.File{Name: "Logical " + filepath.Base(name), Kind: entity.FileKind_FILE_KIND_REGULAR, Note: "keep this note"}
	require.NoError(t, f.exe.Lib().SaveFile(context.Background(), file))
	info, err := os.Stat(filepath.Join(f.root, name))
	require.NoError(t, err)
	require.NoError(t, f.libDB.Create(&library.FileLocation{FileID: file.ID, LocationID: f.location.ID, Path: name,
		Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), ObservedBindingToken: f.location.BindingToken}).Error)
	return file
}

type operationStream struct {
	grpc.ServerStream
	ctx     context.Context
	updates []*entity.FileOperationUpdate
	onSend  func(*entity.FileOperationUpdate) error
}

func (s *operationStream) Context() context.Context { return s.ctx }
func (s *operationStream) Send(update *entity.FileOperationUpdate) error {
	s.updates = append(s.updates, proto.Clone(update).(*entity.FileOperationUpdate))
	if s.onSend != nil {
		return s.onSend(update)
	}
	return nil
}

func (f *fixture) execute(t *testing.T, spec *entity.FileOperationSpec, prepared func()) *operationStream {
	t.Helper()
	stream := &operationStream{ctx: context.Background()}
	stream.onSend = func(update *entity.FileOperationUpdate) error {
		if update.Entry == nil && update.Summary != nil && !update.Summary.Completed && prepared != nil {
			prepared()
		}
		return nil
	}
	require.NoError(t, (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: spec, ConfirmDelete: true}, stream))
	require.True(t, stream.updates[len(stream.updates)-1].GetSummary().GetCompleted())
	// Streaming summaries account for each item exactly once, including deferred directory results.
	seen := make(map[int64]bool)
	var settled int64
	for _, update := range stream.updates {
		if update.Entry == nil {
			continue
		}
		require.False(t, seen[update.Entry.Id])
		seen[update.Entry.Id] = true
		if update.Entry.Outcome != entity.FileOperationOutcome_UNPROCESSED {
			settled++
		}
		require.Equal(t, settled, update.Summary.Succeeded+update.Summary.Failed+update.Summary.PublicationPending)
	}
	require.Equal(t, stream.summary().TotalItems, int64(len(seen)))
	return stream
}

func (s *operationStream) summary() *entity.FileOperationSummary {
	return s.updates[len(s.updates)-1].GetSummary()
}

func TestRequestRenamePreservesOrganizationAndNestedOriginals(t *testing.T) {
	f := setup(t)
	f.write(t, "照片/one.txt", "one")
	f.write(t, "照片/nested/two.txt", "two")
	first, second := f.original(t, "照片/one.txt"), f.original(t, "照片/nested/two.txt")
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "照片")}, Destination: f.ref(t, ""), Name: "Renamed"}, nil)
	require.Equal(t, int64(1), stream.summary().Succeeded)
	require.NoFileExists(t, filepath.Join(f.root, "照片"))
	for _, expected := range []struct {
		file *library.File
		path string
	}{{first, "Renamed/one.txt"}, {second, "Renamed/nested/two.txt"}} {
		original, err := f.exe.Lib().GetFileLocation(context.Background(), expected.file.ID)
		require.NoError(t, err)
		require.Equal(t, expected.path, original.Path)
		var stored library.File
		require.NoError(t, f.libDB.First(&stored, expected.file.ID).Error)
		require.Equal(t, expected.file.Name, stored.Name)
		require.Equal(t, "keep this note", stored.Note)
	}
}

func TestRequestDeleteNeverRemovesNewUnselectedEntries(t *testing.T) {
	f := setup(t)
	f.write(t, "folder/old.txt", "old")
	file := f.original(t, "folder/old.txt")
	version := &library.FileVersion{FileID: file.ID, Signature: []byte("opaque-saved"), Size: 3}
	require.NoError(t, f.libDB.Create(version).Error)
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "folder")}}, func() { f.write(t, "folder/new.txt", "must stay") })
	require.Equal(t, int64(0), stream.summary().Succeeded)
	require.Equal(t, int64(1), stream.summary().Failed)
	require.FileExists(t, filepath.Join(f.root, "folder/old.txt"))
	data, err := os.ReadFile(filepath.Join(f.root, "folder/new.txt"))
	require.NoError(t, err)
	require.Equal(t, "must stay", string(data))
	original, err := f.exe.Lib().GetFileLocation(context.Background(), file.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	require.NoError(t, f.libDB.First(&library.FileVersion{}, version.ID).Error)
	require.NoError(t, f.libDB.First(&library.File{}, file.ID).Error)
}

func TestRequestRejectsChangedSourceAndNoOverwrite(t *testing.T) {
	f := setup(t)
	f.write(t, "old.txt", "before")
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "old.txt")}}, func() { f.write(t, "old.txt", "replacement content") })
	require.Equal(t, int64(1), stream.summary().Failed)
	require.FileExists(t, filepath.Join(f.root, "old.txt"))
	stream = f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "old.txt")}, Destination: f.ref(t, ""), Name: "new.txt"}, func() { f.write(t, "new.txt", "not owned") })
	require.Equal(t, int64(1), stream.summary().Failed)
	data, err := os.ReadFile(filepath.Join(f.root, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "not owned", string(data))
	require.Error(t, renameNoReplace(filepath.Join(f.root, "old.txt"), filepath.Join(f.root, "new.txt")))
}

func TestRequestPublicationFailurePreservesPhysicalMove(t *testing.T) {
	// A metadata receipt failure must preserve already moved bytes and report the exact stage.
	f := setup(t)
	f.write(t, "a.txt", "successful")
	f.write(t, "b.txt", "publication failure")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "target"), 0755))
	require.NoError(t, f.libDB.Callback().Create().Before("gorm:create").Register("reject-receipt", func(tx *gorm.DB) {
		result, ok := tx.Statement.Dest.(*library.FileOperationResult)
		if ok && result.SourcePath == "b.txt" {
			tx.AddError(errors.New("injected receipt failure"))
		}
	}))
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "a.txt"), f.ref(t, "b.txt")}, Destination: f.ref(t, "target")}, nil)
	require.Equal(t, int64(1), stream.summary().Succeeded)
	require.FileExists(t, filepath.Join(f.root, "target/a.txt"))
	require.Equal(t, int64(1), stream.summary().PublicationPending)
	require.FileExists(t, filepath.Join(f.root, "target/b.txt"))
	require.NoFileExists(t, filepath.Join(f.root, "b.txt"))
}

func TestRequestCancellationStopsBeforeNextMutation(t *testing.T) {
	f := setup(t)
	f.write(t, "a.txt", "a")
	f.write(t, "b.txt", "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &operationStream{ctx: ctx, onSend: func(update *entity.FileOperationUpdate) error {
		if update.Entry != nil {
			cancel()
		}
		return nil
	}}
	err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{ConfirmDelete: true, Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "a.txt"), f.ref(t, "b.txt")}}}, stream)
	require.ErrorIs(t, err, context.Canceled)
	require.NoFileExists(t, filepath.Join(f.root, "a.txt"))
	require.FileExists(t, filepath.Join(f.root, "b.txt"))
	require.Len(t, stream.updates, 2)
	require.Equal(t, entity.FileOperationOutcome_SUCCEEDED, stream.updates[1].Entry.Outcome)
}

func TestRequestSafetyAdmissionAndBoundedManifest(t *testing.T) {
	f := setup(t)
	f.write(t, "nested/one", "one")
	server, stream := &service{exe: f.exe}, &operationStream{ctx: context.Background()}
	err := server.Execute(&entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "nested")}}}, stream)
	require.ErrorContains(t, err, "explicit confirmation")
	err = server.Execute(&entity.ExecuteFileOperationRequest{ConfirmDelete: true, Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "")}}}, stream)
	require.ErrorContains(t, err, "roots")
	nested := &library.Location{Name: "Nested", ExecutorID: f.location.ExecutorID, RootPath: filepath.Join(f.root, "nested")}
	require.NoError(t, f.exe.Lib().CreateOnlineSource(context.Background(), nested))
	err = server.Execute(&entity.ExecuteFileOperationRequest{ConfirmDelete: true, Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "nested")}}}, stream)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(f.root, "nested/one"))
	for i := 0; i < batchSize+3; i++ {
		f.write(t, fmt.Sprintf("many/%03d", i), "")
	}
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "many/empty"), 0755))
	stream = f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{f.ref(t, "many")}}, nil)
	require.Equal(t, int64(batchSize+5), stream.summary().Succeeded)
	require.Len(t, stream.updates, batchSize+7)
}

func TestRequestTemporaryManifestIsRemoved(t *testing.T) {
	f := setup(t)
	op, err := newOperation(context.Background(), f.exe)
	require.NoError(t, err)
	directory := op.directory
	require.FileExists(t, filepath.Join(directory, "manifest.db"))
	require.NoError(t, op.Close())
	require.NoDirExists(t, directory)
	require.NoError(t, op.Close())
}

func TestRequestRejectsReplacedDestination(t *testing.T) {
	f := setup(t)
	f.write(t, "source.txt", "content")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "target"), 0755))
	stream := &operationStream{ctx: context.Background(), onSend: func(update *entity.FileOperationUpdate) error {
		if update.Entry != nil || update.Summary.GetCompleted() {
			return nil
		}
		require.NoError(t, os.Rename(filepath.Join(f.root, "target"), filepath.Join(f.root, "previous-target")))
		require.NoError(t, os.Mkdir(filepath.Join(f.root, "target"), 0755))
		return nil
	}}
	err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{
		Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "source.txt")}, Destination: f.ref(t, "target"),
	}}, stream)
	require.NoError(t, err)
	require.EqualValues(t, 1, stream.summary().Failed)
	require.Contains(t, stream.updates[1].Entry.Error, "destination directory was replaced")
	require.FileExists(t, filepath.Join(f.root, "source.txt"))
	require.NoFileExists(t, filepath.Join(f.root, "target/source.txt"))
}

func TestMoveAncestorConflictNeverMergesChildrenIntoForeignDirectory(t *testing.T) {
	f := setup(t)
	f.write(t, "source/one.txt", "one")
	f.write(t, "source/nested/two.txt", "two")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "destination"), 0755))
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "source")}, Destination: f.ref(t, "destination")}, func() {
		f.write(t, "destination/source/sentinel.txt", "foreign content")
	})
	require.Equal(t, int64(1), stream.summary().Failed)
	require.Zero(t, stream.summary().Succeeded)
	entries, err := os.ReadDir(filepath.Join(f.root, "destination/source"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "sentinel.txt", entries[0].Name())
	data, err := os.ReadFile(filepath.Join(f.root, "destination/source/sentinel.txt"))
	require.NoError(t, err)
	require.Equal(t, "foreign content", string(data))
}

func TestDestinationReplacementBetweenItemsIsRejected(t *testing.T) {
	f := setup(t)
	f.write(t, "a.txt", "a")
	f.write(t, "b.txt", "b")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "destination"), 0755))
	stream := &operationStream{ctx: context.Background(), onSend: func(update *entity.FileOperationUpdate) error {
		if update.Entry == nil || update.Entry.SourcePath != "a.txt" {
			return nil
		}
		require.NoError(t, os.Rename(filepath.Join(f.root, "destination"), filepath.Join(f.root, "previous")))
		require.NoError(t, os.Mkdir(filepath.Join(f.root, "destination"), 0755))
		return nil
	}}
	err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{
		Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "a.txt"), f.ref(t, "b.txt")}, Destination: f.ref(t, "destination"),
	}}, stream)
	require.NoError(t, err)
	require.Equal(t, int64(1), stream.summary().Succeeded)
	require.Equal(t, int64(1), stream.summary().Failed)
	require.FileExists(t, filepath.Join(f.root, "previous/a.txt"))
	require.NoFileExists(t, filepath.Join(f.root, "destination/b.txt"))
	require.Contains(t, stream.updates[2].Entry.Error, "destination directory was replaced")
}

func TestActiveOperationManifestCannotBeBrowsedOrAdmitted(t *testing.T) {
	f := setup(t)
	op, err := newOperation(context.Background(), f.exe)
	require.NoError(t, err)
	defer op.Close()
	directory, err := os.MkdirTemp(f.root, filepath.Base(op.directory)+"-")
	require.NoError(t, err)
	defer os.RemoveAll(directory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "content"), []byte("unfinished"), 0600))
	_, err = f.exe.ObserveLocationEntry(context.Background(), f.location.ID, filepath.Base(directory)+"/content")
	require.Error(t, err)
}
