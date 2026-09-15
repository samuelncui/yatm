package executor

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

// HashObservedContent obtains content facts through ACP, outside any Library transaction.
func HashObservedContent(ctx context.Context, filename string, observed os.FileInfo) (*entity.ExpectedFile, error) {
	return hashObservedContent(ctx, filename, observed, false)
}

// VerifyObservedContent reads actual bytes when satisfying a Restore from an existing output.
func VerifyObservedContent(ctx context.Context, filename string, observed os.FileInfo) (*entity.ExpectedFile, error) {
	return hashObservedContent(ctx, filename, observed, true)
}

func hashObservedContent(ctx context.Context, filename string, observed os.FileInfo, force bool) (*entity.ExpectedFile, error) {
	if !observed.Mode().IsRegular() {
		return nil, fmt.Errorf("content input is not an ordinary file")
	}
	stream := &observedHash{filename: filename, observed: observed}
	if err := acp.RunStream(ctx, stream, stream, acp.WithHash(true), acp.WithSignatureCache(true), acp.ForceRehash(force)); err != nil {
		return nil, err
	}
	if stream.result == nil {
		return nil, fmt.Errorf("ACP returned no content facts")
	}
	return stream.result, nil
}

type observedHash struct {
	filename string
	observed os.FileInfo
	issued   bool
	result   *entity.ExpectedFile
}

func (s *observedHash) Next(context.Context) (*acp.StreamRequest, error) {
	if s.issued {
		return nil, io.EOF
	}
	s.issued = true
	info, err := os.Lstat(s.filename)
	if err != nil {
		return nil, err
	}
	if !sameObservedFile(s.observed, info) {
		return nil, library.ErrOnlineConflict
	}
	return &acp.StreamRequest{ID: 1, Source: s.filename}, nil
}

func (s *observedHash) Write(_ context.Context, result *acp.StreamResult) error {
	if result == nil || result.ID != 1 || result.Job == nil {
		return fmt.Errorf("ACP content result is missing")
	}
	job := result.Job
	if job.Status != acp.JobStatusFinished || job.FullPath != s.filename || job.Size != s.observed.Size() || job.Mode != s.observed.Mode() || !job.ModTime.Equal(s.observed.ModTime()) {
		return library.ErrOnlineConflict
	}
	info, err := os.Lstat(s.filename)
	if err != nil {
		return err
	}
	if !sameObservedFile(s.observed, info) {
		return library.ErrOnlineConflict
	}
	hash, err := hex.DecodeString(job.SHA256)
	if err != nil {
		return err
	}
	signature, err := library.NewFileSignature(hash, job.Size)
	if err != nil {
		return err
	}
	s.result = &entity.ExpectedFile{Signature: signature, Sha256: hash, Size: job.Size, Mode: uint32(job.Mode), MtimeNs: job.ModTime.UnixNano()}
	return nil
}

func (*observedHash) Flush(context.Context) error { return nil }

func sameObservedFile(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

// CaptureOriginal freezes the one original selected by File identity, hashing only when needed.
func (e *Executor) CaptureOriginal(ctx context.Context, fileID int64) (string, *entity.ExpectedFile, error) {
	// Preparation observes the current original without requiring a prior Analyze Job.
	original, err := e.lib.GetOnlinePosition(ctx, fileID)
	if err != nil {
		return "", nil, err
	}
	live, err := e.ObserveLocationEntry(ctx, original.SourceID, original.Path)
	if err != nil {
		return "", nil, err
	}
	if live.Reference.BindingToken != original.ObservedBindingToken {
		return "", nil, library.ErrOnlineConflict
	}
	facts := live.Reference.Facts
	location, full, observed, err := e.ResolveLocationEntry(ctx, live.Reference)
	if err != nil {
		return "", nil, err
	}
	keys, err := ObserveTracking(location, full, observed)
	if err != nil {
		return "", nil, err
	}
	applicable, err := e.lib.MatchesObservation(ctx, location, &library.OnlinePosition{FileID: fileID, Path: original.Path,
		Size: facts.Size, Mode: facts.Mode, MtimeNS: facts.MtimeNs, TrackingKeys: keys})
	if err != nil {
		return "", nil, err
	}
	if !applicable {
		updated, err := e.AdmitLocationEntry(ctx, live.Reference)
		if err != nil {
			return "", nil, err
		}
		if updated.FileID != fileID {
			return "", nil, library.ErrOnlineConflict
		}
		original, err = e.lib.GetOnlinePosition(ctx, fileID)
		if err != nil {
			return "", nil, err
		}
	}

	// Freeze actual content and metadata only after refreshing its path-first association.
	expected := &entity.ExpectedFile{FileId: fileID, Signature: original.Signature, Sha256: original.Hash, Size: original.Size,
		Mode: original.Mode, MtimeNs: original.MtimeNS, OriginalLocationId: original.SourceID}
	opened, err := e.OpenOnlinePosition(ctx, original, expected)
	if err != nil {
		return "", nil, err
	}
	filename := opened.Name()
	info, err := opened.Stat()
	closeErr := opened.Close()
	if err != nil {
		return "", nil, err
	}
	if closeErr != nil {
		return "", nil, closeErr
	}
	if len(expected.Signature) != 0 && len(expected.Sha256) == 32 {
		return filename, expected, nil
	}
	known, err := HashObservedContent(ctx, filename, info)
	if err != nil {
		return "", nil, err
	}
	known.FileId = fileID
	known.OriginalLocationId = original.SourceID
	if len(expected.Signature) > 0 {
		known.Signature = expected.Signature
	}
	return filename, known, nil
}
