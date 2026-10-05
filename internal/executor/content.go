package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
)

// HashObservedContent obtains content facts through ACP, outside any Library transaction.
func HashObservedContent(ctx context.Context, filename string, observed os.FileInfo) (*entity.ExpectedFile, error) {
	return hashObservedContent(ctx, filename, observed, false, false)
}

// HashOriginalContent applies the registered Location's read mode to a current original.
func HashOriginalContent(ctx context.Context, filename string, observed os.FileInfo, source *library.Location) (*entity.ExpectedFile, error) {
	return hashObservedContent(ctx, filename, observed, false, source.Config.GetUseMmap())
}

// VerifyObservedContent reads actual bytes when satisfying a Restore from an existing output.
func VerifyObservedContent(ctx context.Context, filename string, observed os.FileInfo) (*entity.ExpectedFile, error) {
	return hashObservedContent(ctx, filename, observed, true, false)
}

func hashObservedContent(ctx context.Context, filename string, observed os.FileInfo, force, mapped bool) (*entity.ExpectedFile, error) {
	if !observed.Mode().IsRegular() {
		return nil, fmt.Errorf("content input is not an ordinary file")
	}

	// One caller-owned item carries the content facts back through the results callback.
	source := &observedHash{filename: filename}
	policy := acp.HashCachedOrReadRefresh
	if force {
		policy = acp.HashReadRefresh
	}
	options := []acp.Option{acp.WithHashPolicy(policy)}
	if mapped {
		options = append(options, acp.SetFromDevice(acp.WithReadMode(acp.ReadMapped)))
	}
	engine, err := acp.NewStream(ctx, source.accept, options...)
	if err != nil {
		return nil, err
	}
	submitErr := engine.Submit(source)
	closeErr := engine.Close()
	waitErr := engine.Wait()

	// The item's own validation failure is the finding; the run merely stopped because of it.
	if err := source.failure(); err != nil {
		return nil, err
	}
	if err := errors.Join(submitErr, waitErr, closeErr); err != nil {
		return nil, err
	}
	result := source.expected()
	if result == nil {
		return nil, fmt.Errorf("ACP returned no content facts")
	}
	return result, nil
}

// observedHash is one observed file as an ACP item. It is pure data for the pipeline: everything
// it learns about its own outcome arrives through the results callback.
type observedHash struct {
	filename string

	lock   sync.Mutex
	result *entity.ExpectedFile
	err    error
}

// Source returns the exact file ACP reads.
func (s *observedHash) Source() string { return s.filename }

// Targets is empty: this item is hashed without being written anywhere.
func (s *observedHash) Targets() []string { return nil }

// accept records the content facts ACP actually read. The callback performs no persistence, so it
// never returns a cancellation error.
func (s *observedHash) accept(results []acp.Result) error {
	for _, result := range results {
		item, ok := result.Job.(*observedHash)
		if !ok {
			continue
		}
		item.complete(result)
	}
	return nil
}

// complete records the item's outcome, which its caller reads after the run ends.
func (s *observedHash) complete(result acp.Result) {
	if result.Err != nil {
		s.fail(result.Err)
		return
	}
	if len(result.SHA256) != 32 {
		s.fail(fmt.Errorf("ACP content hash is missing, source=%q", s.filename))
		return
	}
	signature, err := library.NewFileSignature(result.SHA256, result.Size)
	if err != nil {
		s.fail(err)
		return
	}
	mtime, err := dataformat.Nanoseconds(result.ModTime)
	if err != nil {
		s.fail(err)
		return
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	s.result = &entity.ExpectedFile{Signature: signature, Sha256: result.SHA256, SizeBytes: result.Size,
		Mode: uint32(result.Mode), MtimeNs: mtime}
}

func (s *observedHash) fail(err error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *observedHash) failure() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.err
}

func (s *observedHash) expected() *entity.ExpectedFile {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.result
}

// CaptureOriginal observes the one original selected by File identity, hashing only when needed.
func (e *Executor) CaptureOriginal(ctx context.Context, fileID int64) (string, *entity.ExpectedFile, error) {
	var filename string
	var expected *entity.ExpectedFile
	err := e.CaptureOriginals(ctx, []int64{fileID}, func(_ int64, name string, content *entity.ExpectedFile) error {
		filename, expected = name, content
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if expected == nil {
		return "", nil, library.ErrFileNotFound
	}
	return filename, expected, nil
}

// CaptureOriginals prepares bounded batches in input order, omitting Files without originals.
// All other observation, admission and content failures stop preparation.
func (e *Executor) CaptureOriginals(ctx context.Context, ids []int64, yield func(int64, string, *entity.ExpectedFile) error) error {
	for start := 0; start < len(ids); start += 256 {
		if err := e.captureOriginalBatch(ctx, ids[start:min(start+256, len(ids))], yield); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) captureOriginalBatch(ctx context.Context, ids []int64, yield func(int64, string, *entity.ExpectedFile) error) error {
	// Share catalog reads and source validation while preserving each leaf's physical observation.
	facts, err := e.lib.ReadFileFacts(ctx, ids, false, false)
	if err != nil {
		return err
	}
	locations := make(map[int64]*library.Location)
	parents := make(map[fileParentGuardKey]*LocationDirectoryReader)
	var order []int64
	groups := make(map[int64][]*library.ObservationAdmission)
	infos := make(map[int64]os.FileInfo)
	filenames := make(map[int64]string)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := facts[id]
		if row == nil || row.Original == nil || infos[id] != nil {
			continue
		}
		original := row.Original

		// Validate each source once in this batch; explicit originals do not apply user Ignore.
		location := locations[original.LocationID]
		if location == nil {
			location, err = e.lib.GetLocation(ctx, original.LocationID)
			if err != nil {
				return err
			}
			if _, err := e.CheckLocation(location); err != nil {
				return err
			}
			locations[location.ID] = location
			order = append(order, location.ID)
		}

		// Reuse the directory reader's parent guard and compiled rules for sibling leaves.
		if err := entity.ValidateRelativePath(original.Path); err != nil {
			return err
		}
		parent := path.Dir(original.Path)
		if parent == "." {
			parent = ""
		}
		key := fileParentGuardKey{locationID: location.ID, parent: parent}
		reader := parents[key]
		if reader == nil {
			reader, err = e.PrepareLocationDirectory(location, parent)
			if err != nil {
				return err
			}
			parents[key] = reader
		}
		full := filepath.Join(reader.full, path.Base(original.Path))
		observed, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if !reader.Allowed(path.Base(original.Path), observed.IsDir()) {
			return ErrAccessExcluded
		}
		if !observed.Mode().IsRegular() {
			return fmt.Errorf("original is not an ordinary file")
		}

		// Carry this same observation into shared identity admission and optional hashing.
		entry, err := locationEntry(location.ID, original.Path, observed)
		if err != nil {
			return err
		}
		item, err := selectedFileObservation(location, entry.Reference, observed)
		if err != nil {
			return err
		}
		position := item.Position()
		position.FileID = id
		preserveObservedSignature(position, original)
		groups[location.ID] = append(groups[location.ID], &library.ObservationAdmission{Observation: position})
		infos[id], filenames[id] = observed, full
	}

	// Admission owns unchanged-content matching and one transaction per observed Location batch.
	originals := make(map[int64]*library.FileLocation, len(ids))
	for _, id := range order {
		rows, err := e.lib.AdmitObservations(ctx, id, groups[id])
		if err != nil {
			return err
		}
		for _, original := range rows {
			originals[original.FileID] = original
		}
	}

	// Return content in selection order, hashing only originals without complete content facts.
	for _, id := range ids {
		original := originals[id]
		if original == nil {
			continue
		}
		expected := &entity.ExpectedFile{FileId: id, Signature: original.Signature, Sha256: original.Hash, SizeBytes: original.Size,
			Mode: original.Mode, MtimeNs: original.MtimeNS, OriginalLocationId: original.LocationID}
		if len(expected.Signature) == 0 || len(expected.Sha256) != 32 {
			known, err := HashOriginalContent(ctx, filenames[id], infos[id], locations[original.LocationID])
			if err != nil {
				return err
			}
			known.FileId, known.OriginalLocationId = id, original.LocationID
			if len(expected.Signature) > 0 {
				known.Signature = expected.Signature
			}
			expected = known
		}
		if err := yield(id, filenames[id], expected); err != nil {
			return err
		}
	}
	return nil
}
