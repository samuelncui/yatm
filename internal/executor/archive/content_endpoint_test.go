package archive

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

type endpointWriteSession struct {
	prefix    bool
	opened    map[string]acp.ContentMetadata
	bytes     map[string]*bytes.Buffer
	completed []*mediapkg.CompletedFile
}

func (*endpointWriteSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Write: mediapkg.AccessSequential}
}

func (*endpointWriteSession) Media() *mediapkg.Descriptor {
	return &mediapkg.Descriptor{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001"}
}

func (s *endpointWriteSession) Target(path string, _ int64) (mediapkg.TargetRef, error) {
	return mediapkg.TargetRef{Content: &archiveEndpointTarget{session: s, path: path}}, nil
}

func (s *endpointWriteSession) Finalize(context.Context, error) (*mediapkg.WriteResult, error) {
	return &mediapkg.WriteResult{Media: s.Media(), PathPrefix: "physical", VerifiedPrefix: s.prefix,
		Inventory: func(ctx context.Context, yield func(*mediapkg.CompletedFile) error) error {
			for _, file := range s.completed {
				if err := yield(file); err != nil {
					return err
				}
			}
			return nil
		}}, nil
}

type archiveEndpointTarget struct {
	session *endpointWriteSession
	path    string
}

func (e *archiveEndpointTarget) Open(_ context.Context, metadata acp.ContentMetadata) (acp.ContentWriter, error) {
	e.session.opened[e.path] = metadata
	e.session.bytes[e.path] = new(bytes.Buffer)
	return &archiveEndpointWriter{archiveEndpointTarget: e}, nil
}

type archiveEndpointWriter struct{ *archiveEndpointTarget }

func (w *archiveEndpointWriter) Write(data []byte) (int, error) {
	return w.session.bytes[w.path].Write(data)
}

func (w *archiveEndpointWriter) Finish(cause error) error {
	if cause == nil {
		w.session.completed = append(w.session.completed, reconciliationIndexEntry("physical/"+w.path, int64(w.session.bytes[w.path].Len())))
	}
	return nil
}

func TestArchiveEndpointUsesActualFactsAndFinalInventory(t *testing.T) {
	for _, prefix := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact inventory", true: "LTFS prefix"}[prefix], func(t *testing.T) {
			// Freeze a missing first source and two native sources whose actual size differs from the manifest.
			root := t.TempDir()
			stamp := time.Unix(1700000000, 0)
			var manifest []*Item
			for index, name := range []string{"a-missing.txt", "b-present.txt", "c-present.txt"} {
				source := filepath.Join(root, name)
				if index != 0 {
					require.NoError(t, os.WriteFile(source, []byte("actual "+name), 0640))
					require.NoError(t, os.Chtimes(source, stamp, stamp))
				}
				manifest = append(manifest, &Item{ID: int64(index + 1), Status: entity.CopyStatus_COPY_STATUS_PENDING,
					Size: 999, TargetPath: name, Data: &entity.ArchiveManifestFile{SourcePath: source, Expected: &entity.ExpectedFile{SizeBytes: 999}}})
			}
			runner := newItemTestRunner(t, manifest...)
			reporter := startTestReporter(t, runner)
			session := &endpointWriteSession{prefix: prefix, opened: make(map[string]acp.ContentMetadata), bytes: make(map[string]*bytes.Buffer)}

			// The real ACP lane feeds native sources into endpoint writers and the Archive result writer.
			stream, err := acp.NewStream(context.Background(), reporter.onResults, acp.WithHashPolicy(acp.HashRead),
				acp.SetToDevice(acp.LinearDevice(true)))
			require.NoError(t, err)
			source := &copySource{runner: runner, session: session, pageSize: 2}
			copyErr := source.consume(context.Background(), stream)
			require.NoError(t, reporter.Close())
			require.NoError(t, reporter.err())
			result, err := session.Finalize(context.Background(), copyErr)
			require.NoError(t, err)
			require.NoError(t, runner.reconcileWriteResult(context.Background(), result))

			// Failed source Open creates no member; successful endpoint results use frozen names, actual facts and one prefix.
			require.NotContains(t, session.opened, "a-missing.txt")
			require.Len(t, session.opened, 2)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, readItemStatus(t, runner, 1))
			for _, item := range manifest[1:] {
				actual := []byte("actual " + item.TargetPath)
				metadata := session.opened[item.TargetPath]
				require.EqualValues(t, len(actual), metadata.Info.Size())
				require.Equal(t, fs.FileMode(0640), metadata.Info.Mode().Perm())
				require.Equal(t, stamp, metadata.Info.ModTime())
				require.Equal(t, actual, session.bytes[item.TargetPath].Bytes())
				stored := runnerItem(t, runner, item.ID)
				if prefix {
					require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
					require.Empty(t, stored.MediaPath)
					continue
				}
				require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, stored.Status)
				require.Equal(t, "physical/"+item.TargetPath, stored.MediaPath)
				require.EqualValues(t, len(actual), stored.Result.SizeBytes)
				require.Equal(t, sha256Sum(actual), stored.Result.Sha256)
				require.NotNil(t, stored.Result.Storage)
			}
		})
	}
}
