package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
)

// AdmitLocationEntries admits a small explicit selection of live ordinary files through the same
// observation, matching and publication rules as a Location selection, without a global scan.
func (e *Executor) AdmitLocationEntries(ctx context.Context, refs []*entity.LocationEntryRef) ([]*library.FileLocation, error) {
	// Reject invalid live batches before resolving any Location.
	if len(refs) == 0 || len(refs) > 1000 || refs[0] == nil {
		return nil, fmt.Errorf("admit between 1 and 1000 live files")
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref == nil || ref.LocationId != refs[0].LocationId {
			return nil, library.ErrLocationConflict
		}
		if library.IsLocationTrashPath(ref.Path) {
			return nil, fmt.Errorf("Trash content cannot be added to Library")
		}
		if seen[ref.Path] {
			return nil, fmt.Errorf("duplicate admission path %q", ref.Path)
		}
		seen[ref.Path] = true
	}

	// Observe each path once; gathering evidence performs no full-file reads.
	tracker := NewTracker()
	var location *library.Location
	items := make([]*observation.Item, 0, len(refs))
	for index, ref := range refs {
		current, _, info, err := e.ResolveLocationEntry(ctx, ref)
		if err != nil {
			return nil, err
		}
		location = current
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("only ordinary files have Library associations")
		}
		item, err := selectedFileObservation(tracker, location, ref, info)
		if err != nil {
			return nil, err
		}
		item.ID = int64(index + 1)
		items = append(items, item)
	}
	if err := e.enrichSelectedFiles(ctx, location, items); err != nil {
		return nil, err
	}
	if err := e.MatchLocationObservations(ctx, location, observation.NewSliceStore(items), nil); err != nil {
		return nil, err
	}

	// Publication creates unmatched Files and releases relocated associations atomically.
	ordered := append([]*observation.Item(nil), items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	if _, err := e.lib.PublishSelectedObservations(ctx, location.ID, func(_ context.Context, yield func(*library.ObservedEntry) error) error {
		for _, item := range ordered {
			if err := yield(item.Position()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(refs))
	for _, ref := range refs {
		paths = append(paths, ref.Path)
	}
	originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
	if err != nil {
		return nil, err
	}
	results := make([]*library.FileLocation, 0, len(refs))
	for _, ref := range refs {
		original := originals[ref.Path]
		if original == nil {
			return nil, library.ErrLocationConflict
		}
		results = append(results, original)
	}
	return results, nil
}

func cachedObservedContent(name string, size, mtimeNS int64) (hash, signature []byte, err error) {
	// Disposable cache errors and facts outside the existing observation remain misses.
	cached, valid, _ := acp.ReadCachedSignature(name)
	if !valid || cached.Size != size || cached.MtimeNS != mtimeNS {
		return nil, nil, nil
	}

	// Each observation owns its bytes; deriving a content identity still reports real errors.
	hash = append([]byte(nil), cached.SHA256[:]...)
	signature, err = library.NewFileSignature(hash, size)
	return hash, signature, err
}

func (e *Executor) absentAdmissionPath(location *library.Location, old *library.FileLocation) bool {
	// An inaccessible root or permissions failure is not evidence of a move.
	if old.LocationID != location.ID {
		return false
	}
	if _, _, err := e.CheckLocationPath(location, old.Path); !os.IsNotExist(err) {
		return false
	}
	_, _, err := e.CheckLocationPath(location, "")
	return err == nil
}

func preserveObservedSignature(observation *library.ObservedEntry, previous *library.FileLocation) {
	// A valid cache may reproduce content hashes, but must not recode a known opaque signature.
	if len(observation.Hash) != 0 && len(previous.Signature) != 0 && observation.Size == previous.Size && bytes.Equal(observation.Hash, previous.Hash) {
		observation.Signature = previous.Signature
	}
}
