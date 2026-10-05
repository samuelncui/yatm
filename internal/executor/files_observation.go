package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

// maxFileObservationWorkers bounds one page's concurrent filesystem reads.
const maxFileObservationWorkers = 8

// FileReadObservation is an ephemeral metadata check; it does not publish catalog state.
type FileReadObservation struct {
	Availability entity.OriginalAvailability
	Valid        bool
	Entry        *entity.LocationEntry
	Location     *library.Location
}

// FileRowReader retains source and parent authorization for one read operation.
// Read calls are sequential; only the physical observations within a batch run concurrently.
type FileRowReader struct {
	executor *Executor
	sources  map[int64]fileReadSource
	guards   map[fileParentGuardKey]pathGuard
	allowed  map[fileParentGuardKey]func(string, bool) bool
}

// NewFileRowReader starts an operation-local authorization cache; it retains no row observations.
func (e *Executor) NewFileRowReader() *FileRowReader {
	return &FileRowReader{executor: e, sources: make(map[int64]fileReadSource),
		guards: make(map[fileParentGuardKey]pathGuard), allowed: make(map[fileParentGuardKey]func(string, bool) bool)}
}

// Read observes one batch while reusing the operation's source and parent checks.
func (r *FileRowReader) Read(ctx context.Context, facts map[int64]*library.FileReadFacts) (map[int64]*FileReadObservation, error) {
	return r.read(ctx, facts, nil, nil)
}

// ObserveFileRows shares Location setup and prefetched tracking facts across one page.
// Rows are grouped by source and parent directory, so the administrator path guard is
// proved once per directory instead of once per row. Native evidence uses the same stat.
func (e *Executor) ObserveFileRows(ctx context.Context, facts map[int64]*library.FileReadFacts) (map[int64]*FileReadObservation, error) {
	return e.NewFileRowReader().Read(ctx, facts)
}

// ObserveListedFileRows reuses metadata from this authorized directory read.
func (e *Executor) ObserveListedFileRows(ctx context.Context, facts map[int64]*library.FileReadFacts, location *library.Location, infos map[string]os.FileInfo) (map[int64]*FileReadObservation, error) {
	return e.NewFileRowReader().read(ctx, facts, location, infos)
}

func (r *FileRowReader) read(ctx context.Context, facts map[int64]*library.FileReadFacts, listed *library.Location, infos map[string]os.FileInfo) (map[int64]*FileReadObservation, error) {
	// Resolve each registered source once; source failures remain per-row unknown observations.
	e, sources := r.executor, r.sources
	for _, row := range facts {
		if row.Original == nil {
			continue
		}
		locationID := row.Original.LocationID
		if _, exists := sources[locationID]; exists {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := fileReadSource{}
		if listed != nil && listed.ID == locationID {
			entry.location, entry.root = listed, listed.RootPath
		} else {
			entry.location, entry.err = e.lib.GetLocation(ctx, locationID)
			if entry.err == nil {
				entry.root, entry.err = e.CheckLocation(entry.location)
			}
		}
		sources[locationID] = entry
	}

	// One page is bounded work: a small worker pool and a per-directory guard cache keep the
	// page's cost proportional to its rows instead of serializing one filesystem round per row.
	result := make(map[int64]*FileReadObservation, len(facts))
	guards := r.guards
	// Prepare each parent once before concurrent reads; scoped rule matchers stay on this goroutine.
	allowed := r.allowed
	leaves := make(map[int64]fileLeafAccess)
	var guardMu sync.Mutex
	for id, row := range facts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row.Original == nil {
			continue
		}
		source := sources[row.Original.LocationID]
		if source.err != nil {
			continue
		}
		original := row.Original
		parent := path.Dir(original.Path)
		if parent == "." {
			parent = ""
		}
		key := fileParentGuardKey{locationID: original.LocationID, parent: parent}
		if _, exists := guards[key]; !exists {
			if infos != nil && listed != nil && listed.ID == original.LocationID && infos[original.Path] != nil {
				guards[key] = pathGuard{}
			} else {
				_ = e.parentGuard(source.location, source.root, original.Path, guards, &guardMu)
			}
			allowed[key] = e.locationChildAccess(source.location, parent)
		}
		leaves[id] = fileLeafAccess{file: allowed[key](path.Base(original.Path), false), directory: allowed[key](path.Base(original.Path), true)}
	}

	// Bound physical observations while keeping all parent and leaf authorization results immutable.
	workers := min(runtime.GOMAXPROCS(0), maxFileObservationWorkers)
	var wait sync.WaitGroup
	slots := make(chan struct{}, workers)
	var mu sync.Mutex
	var failure error
	for id, row := range facts {
		if row.Original == nil {
			mu.Lock()
			result[id] = &FileReadObservation{Availability: entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED}
			mu.Unlock()
			continue
		}
		source := sources[row.Original.LocationID]
		wait.Add(1)
		slots <- struct{}{}
		go func(id int64, row *library.FileReadFacts, source fileReadSource) {
			defer wait.Done()
			defer func() { <-slots }()
			rowInfos := infos
			if listed == nil || listed.ID != row.Original.LocationID {
				rowInfos = nil
			}
			observation, err := e.observeFileRow(ctx, row, source, guards, leaves[id], rowInfos)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if failure == nil {
					failure = err
				}
				return
			}
			result[id] = observation
		}(id, row, source)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	return result, nil
}

// fileReadSource is one resolved Location and its validated root.
type fileReadSource struct {
	location *library.Location
	err      error
	root     string
}

// pathGuard is one parent directory's administrator-boundary result.
type pathGuard struct {
	err error
}

type fileLeafAccess struct {
	file, directory bool
}

type fileParentGuardKey struct {
	locationID int64
	parent     string
}

// observeFileRow reports what one row's original currently is. The row's own facts decide
// the answer; native tracking evidence is derived from the same metadata observation.
func (e *Executor) observeFileRow(
	ctx context.Context,
	row *library.FileReadFacts,
	source fileReadSource,
	guards map[fileParentGuardKey]pathGuard,
	access fileLeafAccess,
	infos map[string]os.FileInfo,
) (*FileReadObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observation := &FileReadObservation{Availability: entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED}
	original := row.Original
	if original == nil {
		return observation, nil
	}
	observation.Location = source.location
	observation.Availability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE
	if source.err != nil {
		return observation, nil
	}

	// Source and parent failures affect only their rows; cancellation remains an operation error.
	parent := path.Dir(original.Path)
	if parent == "." {
		parent = ""
	}
	guard := guards[fileParentGuardKey{locationID: original.LocationID, parent: parent}]
	if !access.file && !access.directory {
		return observation, nil
	}
	if guard.err != nil {
		if errors.Is(guard.err, os.ErrNotExist) {
			observation.Availability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING
		}
		return observation, nil
	}
	full := filepath.Join(source.root, filepath.FromSlash(original.Path))
	info := infos[original.Path]
	var err error
	if info == nil {
		info, err = os.Lstat(full)
	}
	if errors.Is(err, os.ErrNotExist) {
		observation.Availability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING
		return observation, nil
	}
	if err != nil {
		return observation, nil
	}
	if info.IsDir() && !access.directory || !info.IsDir() && !access.file {
		return observation, nil
	}
	if !info.Mode().IsRegular() {
		observation.Availability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING
		return observation, nil
	}

	entry, err := locationEntry(source.location.ID, original.Path, info)
	if err != nil {
		return observation, nil
	}
	observation.Entry, observation.Availability = entry, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT
	if original.Size != info.Size() || original.Mode != uint32(info.Mode()) || original.MtimeNS != entry.Reference.Facts.MtimeNs {
		return observation, nil
	}

	// Native identity shares the metadata observation without opening the file.
	keys := ObserveTracking(source.location, info)
	if !sameReadTracking(row.Tracking, keys) {
		return observation, nil
	}
	observation.Valid = true
	return observation, nil
}

// parentGuard proves one row's containing directory once per page, near enough to the guard
// a single-path read applies to it: the same administrator boundary, the same rule that no
// ancestor is a symlink, and the same treatment of a directory that no longer resolves.
func (e *Executor) parentGuard(location *library.Location, root, relative string, guards map[fileParentGuardKey]pathGuard, mu *sync.Mutex) error {
	parent := path.Dir(relative)
	if parent == "." {
		parent = ""
	}
	key := fileParentGuardKey{locationID: location.ID, parent: parent}
	mu.Lock()
	defer mu.Unlock()
	guard, cached := guards[key]
	if !cached {
		// The root was resolved once for this page, so only the row path itself is new.
		_, info, err := e.checkPreparedLocationPath(location, root, parent)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("file parent %q is not a real directory: %w", parent, ErrAccessExcluded)
		}
		guard = pathGuard{err: err}
		guards[key] = guard
	}
	return guard.err
}

func sameReadTracking(stored, observed []*library.FileTrackingKey) bool {
	if len(stored) != len(observed) {
		return false
	}
	for _, key := range observed {
		matched := false
		for _, previous := range stored {
			if key.Kind == previous.Kind && key.Scope == previous.Scope && bytes.Equal(key.KeyValue, previous.KeyValue) && key.Details == previous.Details {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
