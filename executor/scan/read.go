package scan

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/media"
)

type contentStream struct {
	runner      *runner
	config      *Config
	scope       *Scope
	session     mediapkg.ReadSession
	page        []*Entry
	index       int
	cursor      int64
	cursorOrder []byte
	cursorPath  string
	lock        sync.Mutex
	failure     error
	exhausted   bool
	processed   int64
	readBytes   int64
}

func (r *runner) readContent(ctx context.Context, config *Config, scope *Scope, session mediapkg.ReadSession) error {
	// Cache-only misses remain unknown; they must never reach ACP's automatic hashing fallback.
	if config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_KNOWN_ONLY {
		return nil
	}
	checking := config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES
	if checking {
		r.setPhase(entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA)
	}
	for {
		stream := &contentStream{runner: r, config: config, scope: scope, session: session}
		options := []acp.Option{acp.WithHash(true), acp.WithLogger(r.logger), acp.WithEventHandler(stream.eventHandler(ctx))}
		if !checking && (session == nil || session.Capabilities().Read != mediapkg.AccessSequential) {
			options = append(options, acp.WithSignatureCache(true), acp.ForceRehash(true))
		}
		if session != nil {
			options = append(options, acp.SetFromDevice(mediapkg.DeviceOptions(session.Capabilities().Read)...))
		}
		err := acp.RunStream(ctx, stream, stream, options...)
		r.progress.UpdateSessionCurrent(stream.readBytes, stream.processed)
		r.progress.CommitSession()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stream.failure != nil {
			return errors.Join(err, stream.failure)
		}
		if err == nil || stream.exhausted {
			return err
		}
		if !checking || stream.processed == 0 {
			return err
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
}

func (s *contentStream) Next(ctx context.Context) (*acp.StreamRequest, error) {
	for {
		// Page by deterministic physical read order, preserving Tape locality without loading the manifest.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.index == len(s.page) {
			query := s.runner.scopeQuery(ctx, s.scope).Where("needs_hash = ? AND published = ?", true, false)
			order := "id"
			if s.session != nil && s.session.Capabilities().Read == mediapkg.AccessSequential {
				order = "storage_order, path, id"
				if s.cursor > 0 {
					query = query.Where("storage_order > ? OR (storage_order = ? AND path > ?) OR (storage_order = ? AND path = ? AND id > ?)", s.cursorOrder, s.cursorOrder, s.cursorPath, s.cursorOrder, s.cursorPath, s.cursor)
				}
			} else {
				query = query.Where("id > ?", s.cursor)
			}
			if err := query.Order(order).Limit(batchSize).Find(&s.page).Error; err != nil {
				return nil, err
			}
			if len(s.page) == 0 {
				s.exhausted = true
				return nil, io.EOF
			}
			s.index = 0
		}
		row := s.page[s.index]
		s.index++
		s.cursor = row.ID
		s.cursorOrder = row.StorageOrder
		s.cursorPath = row.Path
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			continue
		}

		// Revalidate an immutable entry reference immediately before opening its bytes.
		filename := row.SourcePath
		var err error
		if s.session != nil {
			filename, err = s.session.SourcePath(row.Path)
		}
		if s.config.IndexedInput && row.Expected.GetFileId() > 0 {
			filename, err = s.runner.resolveIndexedSource(ctx, row.Expected)
		}
		if err == nil {
			var info os.FileInfo
			info, err = os.Lstat(filename)
			if err == nil && !info.Mode().IsRegular() {
				err = fmt.Errorf("Scan source is not an ordinary file: %q", row.Path)
			}
			if err == nil && s.config.Spec.ResultPolicy != entity.ScanResultPolicy_VERIFY_COPIES {
				if info.Size() != row.Size || uint32(info.Mode()) != row.Mode || info.ModTime().UnixNano() != row.MtimeNs {
					err = fmt.Errorf("Scan source changed before reading: %q", row.Path)
				}
			}
		}
		if err != nil {
			if s.config.Spec.ResultPolicy != entity.ScanResultPolicy_VERIFY_COPIES || isDeviceReadError(err) {
				return nil, err
			}
			if err := s.recordReadError(ctx, row, err); err != nil {
				return nil, err
			}
			continue
		}
		if err := s.runner.db.WithContext(ctx).Model(row).Update("source_path", filename).Error; err != nil {
			return nil, err
		}
		return &acp.StreamRequest{ID: row.ID, Source: filename}, nil
	}
}

func (s *contentStream) Write(ctx context.Context, result *acp.StreamResult) error {
	// All content consumers use ACP's actual successful read, never target rereads or hand-written hash loops.
	if result == nil || result.Job == nil {
		return fmt.Errorf("Scan ACP result is missing")
	}
	var row Entry
	if err := s.runner.db.WithContext(ctx).First(&row, result.ID).Error; err != nil {
		return err
	}
	job := result.Job
	if filepath.Clean(job.FullPath) != filepath.Clean(row.SourcePath) || job.SignatureCacheHit {
		return fmt.Errorf("Scan ACP result does not identify an actual selected read")
	}
	if job.Status != acp.JobStatusFinished || len(job.FailTargets) > 0 {
		return s.recordReadError(ctx, &row, fmt.Errorf("Scan could not read the complete file"))
	}
	hash, err := hex.DecodeString(job.SHA256)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("Scan ACP result has no SHA-256")
	}
	info, err := os.Lstat(row.SourcePath)
	if err != nil {
		return s.recordReadError(ctx, &row, err)
	}
	if info.Size() != job.Size || !info.ModTime().Equal(job.ModTime) || info.Mode() != job.Mode {
		return s.recordReadError(ctx, &row, fmt.Errorf("Scan source changed during read"))
	}

	// Expected-copy comparison retains the old baseline even when a complete read returns different bytes.
	if s.config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
		row.Finding = entity.ScanFinding_UNVERIFIABLE
		if expected := row.Expected; expected != nil && len(expected.Sha256) == 32 && expected.Size >= 0 {
			row.Finding = entity.ScanFinding_MATCH
			if expected.Size != job.Size || !bytes.Equal(expected.Sha256, hash) {
				row.Finding = entity.ScanFinding_MISMATCH
			}
		}
		row.ActualSize, row.ActualHash, row.CheckedAt = job.Size, hash, time.Now().UnixMilli()
		row.ReadMode, row.ReadMtimeNs = uint32(job.Mode), job.ModTime.UnixNano()
		row.NeedsHash = false
	} else {
		if row.Size != job.Size || row.Mode != uint32(job.Mode) || row.MtimeNs != job.ModTime.UnixNano() {
			return fmt.Errorf("Scan source changed while reading: %q", row.Path)
		}
		if s.config.IndexedInput && (row.Expected.Size != job.Size || !bytes.Equal(row.Expected.Sha256, hash)) {
			return fmt.Errorf("Scan source no longer contains expected content: %q", row.Path)
		}
		setHash(&row, hash)
	}
	if err := s.runner.db.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	s.lock.Lock()
	s.processed++
	s.readBytes += job.Size
	s.lock.Unlock()
	return nil
}

func (s *contentStream) recordReadError(ctx context.Context, row *Entry, cause error) error {
	if s.config.Spec.ResultPolicy != entity.ScanResultPolicy_VERIFY_COPIES {
		return cause
	}
	if isDeviceReadError(cause) {
		return cause
	}
	row.Finding = entity.ScanFinding_UNREADABLE
	if errors.Is(cause, os.ErrNotExist) {
		row.Finding = entity.ScanFinding_MISSING
	}
	row.Detail = cause.Error()
	row.CheckedAt = time.Now().UnixMilli()
	row.NeedsHash = false
	if err := s.runner.db.WithContext(ctx).Save(row).Error; err != nil {
		return err
	}
	s.lock.Lock()
	s.processed++
	s.lock.Unlock()
	return nil
}

func (s *contentStream) eventHandler(ctx context.Context) acp.EventHandler {
	return func(event acp.Event) {
		// Runtime progress remains observable for a single large file without publishing catalog revisions.
		if value, ok := event.(*acp.EventUpdateProgress); ok {
			s.lock.Lock()
			processed := s.processed
			s.lock.Unlock()
			s.runner.progress.UpdateSessionCurrent(value.Bytes, processed)
			return
		}
		report, ok := event.(*acp.EventReportError)
		if !ok || report.Error == nil || report.Error.Err == nil || report.Error.Src == "" || ctx.Err() != nil {
			return
		}
		if s.config.Spec.ResultPolicy != entity.ScanResultPolicy_VERIFY_COPIES {
			return
		}
		var row Entry
		err := s.runner.db.WithContext(ctx).Where("source_path = ? AND needs_hash = ?", report.Error.Src, true).First(&row).Error
		if err == nil {
			err = s.recordReadError(ctx, &row, report.Error.Err)
		}
		if err != nil {
			s.lock.Lock()
			s.failure = err
			s.lock.Unlock()
		}
	}
}

func (*contentStream) Flush(context.Context) error { return nil }
func isDeviceReadError(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO)
}

func reusableSignature(filename string) (acp.CachedSignature, bool, error) {
	// Malformed disposable xattrs are misses; real access failures must not look like unsigned success.
	signature, valid, err := acp.ReadCachedSignature(filename)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || isDeviceReadError(err) {
		return signature, false, err
	}
	return signature, valid && err == nil, nil
}
