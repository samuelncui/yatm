package restore

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

type endpointReadSession struct {
	mediapkg.ReadSession
	source *restoreContentSource
}

func (*endpointReadSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Read: mediapkg.AccessSequential}
}

func (s *endpointReadSession) Source(mediapkg.ReadRequest) (mediapkg.SourceRef, error) {
	return mediapkg.SourceRef{Content: s.source}, nil
}

type restoreContentSource struct {
	content         []byte
	info            fs.FileInfo
	openErr         error
	statErr         error
	attrs           map[string][]byte
	attrsErr        error
	closeErr        error
	openFileOnError bool
	opens           atomic.Int64
	closes          atomic.Int64
}

func (s *restoreContentSource) Open(context.Context) (fs.File, error) {
	s.opens.Add(1)
	if s.openErr != nil && !s.openFileOnError {
		return nil, s.openErr
	}
	return &restoreContentReader{Reader: bytes.NewReader(s.content), source: s}, s.openErr
}

type restoreContentReader struct {
	*bytes.Reader
	source *restoreContentSource
}

func (r *restoreContentReader) Stat() (fs.FileInfo, error) { return r.source.info, r.source.statErr }

func (r *restoreContentReader) Xattrs() (map[string][]byte, error) {
	return r.source.attrs, r.source.attrsErr
}

func (r *restoreContentReader) Close() error {
	r.source.closes.Add(1)
	return r.source.closeErr
}

func TestRestoreEndpointFailureCannotPublishOrAdoptOutput(t *testing.T) {
	for _, boundary := range []string{"open", "open with file", "stat", "xattrs", "close", "stopped lane"} {
		t.Run(boundary, func(t *testing.T) {
			// Freeze a real Restore candidate, while its bytes come from a content endpoint.
			ctx := context.Background()
			content := []byte("saved content")
			runner, _, candidates := setupVolumeRestore(t, "endpoint-"+boundary, map[string][]byte{"file.txt": content})
			candidate := candidates[0]
			metadataPath := filepath.Join(t.TempDir(), "metadata")
			require.NoError(t, os.WriteFile(metadataPath, content, 0o640))
			info, err := os.Stat(metadataPath)
			require.NoError(t, err)
			failure := errors.New("invalid member " + boundary)
			source := &restoreContentSource{content: content, info: info}
			switch boundary {
			case "stopped lane":
				failure = errors.Join(mediapkg.ErrReadUnavailable, os.ErrPermission)
				source.openErr = failure
			case "open", "open with file":
				source.openErr = failure
				source.openFileOnError = boundary == "open with file"
			case "stat":
				source.statErr = failure
			case "xattrs":
				source.attrsErr = failure
			default:
				source.closeErr = failure
			}
			session := &endpointReadSession{source: source}
			transfer := func() error {
				// Drive the same pager, ACP stream and result writer as a Media attempt.
				buffer := startTestBuffer(t, runner)
				items := &copyItems{runner: runner, mediaID: candidate.MediaID, session: session, batch: 1}
				engine, err := acp.NewStream(ctx, buffer.onResults, acp.WithHashPolicy(acp.HashReadRefresh),
					acp.Overwrite(false), acp.SetFromDevice(mediapkg.DeviceOptions(session.Capabilities().Read)...))
				require.NoError(t, err)
				return errors.Join(items.consume(ctx, engine), buffer.Close())
			}

			// Matching payload bytes are insufficient: failed member closure must leave no output.
			require.ErrorIs(t, transfer(), failure)
			_, err = os.Stat(runner.restoreTarget(candidate.TargetPath))
			require.ErrorIs(t, err, os.ErrNotExist)
			var output File
			require.NoError(t, runner.db.First(&output, candidate.ItemID).Error)
			require.False(t, output.Ready)
			require.False(t, output.Completed)
			var stored Copy
			require.NoError(t, runner.db.First(&stored, candidate.ID).Error)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
			require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
			require.EqualValues(t, 1, source.opens.Load())
			if boundary != "open" && boundary != "stopped lane" {
				require.EqualValues(t, 1, source.closes.Load())
			}

			// The next explicit attempt transfers again instead of adopting a leaked final path.
			source.openErr, source.statErr, source.attrsErr, source.closeErr = nil, nil, nil, nil
			require.NoError(t, transfer())
			require.EqualValues(t, 2, source.opens.Load())
			actual, err := os.ReadFile(runner.restoreTarget(candidate.TargetPath))
			require.NoError(t, err)
			require.Equal(t, content, actual)
			require.NoError(t, runner.db.First(&output, candidate.ItemID).Error)
			require.True(t, output.Ready)
			require.False(t, output.Completed, "Media finalization still owns Library publication")
		})
	}
}
