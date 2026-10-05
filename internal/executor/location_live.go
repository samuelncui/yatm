package executor

import (
	"container/heap"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
)

// InspectLocationFacts converts one stat observation without another filesystem read.
func InspectLocationFacts(info os.FileInfo) (*entity.LocationFileFacts, error) {
	mtime, err := dataformat.Nanoseconds(info.ModTime())
	if err != nil {
		return nil, err
	}
	return &entity.LocationFileFacts{SizeBytes: info.Size(), Mode: uint32(info.Mode()), MtimeNs: mtime}, nil
}

// CheckLocationPath validates administrator boundaries, independently of index Ignore rules.
// The leaf may be a symlink; no ancestor may be one.
func (e *Executor) CheckLocationPath(location *library.Location, relative string) (string, os.FileInfo, error) {
	root, err := e.CheckLocation(location)
	if err != nil {
		return "", nil, err
	}
	return e.checkPreparedLocationPath(location, root, relative)
}

// checkPreparedLocationPath shares one validated configuration within a bounded read.
func (e *Executor) checkPreparedLocationPath(location *library.Location, root, relative string) (string, os.FileInfo, error) {
	// Validate physical ancestors and the leaf without repeating catalog/configuration queries.
	if relative != "" {
		if err := entity.ValidateRelativePath(relative); err != nil {
			return "", nil, err
		}
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	if relative != "" {
		if _, err := e.LocationRoot(filepath.Dir(full)); err != nil {
			return "", nil, err
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", nil, err
	}

	// Runtime/archive exclusions and administrator rules are mandatory even for explicit selections.
	if location.AccessExcluded(relative, info.IsDir()) {
		return "", nil, ErrAccessExcluded
	}
	return full, info, nil
}

// ResolveLocationEntry resolves one selected path under its current registration.
func (e *Executor) ResolveLocationEntry(ctx context.Context, ref *entity.LocationEntryRef) (*library.Location, string, os.FileInfo, error) {
	if ref == nil || ref.LocationId <= 0 {
		return nil, "", nil, fmt.Errorf("Location selection requires a registration and path")
	}
	location, err := e.lib.GetLocation(ctx, ref.LocationId)
	if err != nil {
		return nil, "", nil, err
	}
	full, info, err := e.CheckLocationPath(location, ref.Path)
	if err != nil {
		return nil, "", nil, err
	}
	return location, full, info, nil
}

// ObserveLocationEntry returns the actual object; its File association is optional.
func (e *Executor) ObserveLocationEntry(ctx context.Context, locationID int64, relative string) (*entity.LocationEntry, error) {
	location, err := e.lib.GetLocation(ctx, locationID)
	if err != nil {
		return nil, err
	}
	_, info, err := e.CheckLocationPath(location, relative)
	if err != nil {
		return nil, err
	}
	return locationEntry(location.ID, relative, info)
}

type locationCursor struct {
	LocationID int64
	Path       string
	Filter     string
	After      string
}

// ListLocationEntries keeps only a bounded lexical page, never a cached physical tree.
func (e *Executor) ListLocationEntries(ctx context.Context, req *entity.LocationEntriesQuery) (*entity.LocationEntriesPage, error) {
	return e.ListLocationEntriesMatching(ctx, req, nil)
}

// ListLocationEntriesMatching applies a read-only batch predicate during one
// directory enumeration. The caller binds the predicate to its public cursor.
// Listing hides administrator, YATM storage and user Ignore entries; explicit
// entry resolution and size measurement remain available for them.
func (e *Executor) ListLocationEntriesMatching(ctx context.Context, req *entity.LocationEntriesQuery, match func([]*entity.LocationEntry) ([]int, error)) (*entity.LocationEntriesPage, error) {
	return e.listLocationEntries(ctx, req, req.GetCursor(), match)
}

// LocationEntriesRead carries the metadata used to match this query page into its projection.
type LocationEntriesRead struct {
	Page     *entity.LocationEntriesPage
	Location *library.Location
	Parent   *entity.LocationEntry
	Infos    map[string]os.FileInfo
}

type locationCandidate struct {
	name  string
	child os.DirEntry
	entry *entity.LocationEntry
	info  os.FileInfo
}
type locationCandidates []locationCandidate

func (h locationCandidates) Len() int           { return len(h) }
func (h locationCandidates) Less(i, j int) bool { return h[i].name > h[j].name }
func (h locationCandidates) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *locationCandidates) Push(v any)        { *h = append(*h, v.(locationCandidate)) }
func (h *locationCandidates) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func (e *Executor) listLocationEntries(ctx context.Context, req *entity.LocationEntriesQuery, cursor string, match func([]*entity.LocationEntry) ([]int, error)) (*entity.LocationEntriesPage, error) {
	var predicate func(*library.Location, []*entity.LocationEntry, map[string]os.FileInfo) ([]int, error)
	if match != nil {
		predicate = func(_ *library.Location, rows []*entity.LocationEntry, _ map[string]os.FileInfo) ([]int, error) {
			return match(rows)
		}
	}
	result, err := e.ReadLocationEntriesMatching(ctx, req, cursor, predicate)
	if err != nil {
		return nil, err
	}
	return result.Page, nil
}

// ReadLocationEntriesMatching enumerates once per query page, retaining only its smallest matches.
func (e *Executor) ReadLocationEntriesMatching(ctx context.Context, req *entity.LocationEntriesQuery, requestCursor string,
	match func(*library.Location, []*entity.LocationEntry, map[string]os.FileInfo) ([]int, error)) (*LocationEntriesRead, error) {
	// Preserve existing stateless cursor binding and bounded query-page semantics.
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 100000 {
		return nil, fmt.Errorf("invalid directory page size")
	}
	location, err := e.lib.GetLocation(ctx, req.GetLocationId())
	if err != nil {
		return nil, err
	}
	parent := strings.TrimSuffix(req.GetParentPath(), "/")
	full, info, err := e.CheckLocationPath(location, parent)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("selected path is not a directory")
	}
	reader, err := e.newLocationDirectoryReader(location, parent, full, info)
	if err != nil {
		return nil, err
	}
	filter := strings.ToLower(req.GetNameFilter())
	cursor := locationCursor{LocationID: location.ID, Path: parent, Filter: filter}
	if requestCursor != "" {
		var stored locationCursor
		data, err := base64.RawURLEncoding.DecodeString(requestCursor)
		if len(requestCursor) > 16384 || err != nil || json.Unmarshal(data, &stored) != nil {
			return nil, fmt.Errorf("invalid directory cursor")
		}
		if stored.LocationID != cursor.LocationID || stored.Path != parent || stored.Filter != filter {
			return nil, library.ErrLocationConflict
		}
		cursor.After = stored.After
	}

	// A max heap bounds retained observations to limit+1 without quadratic sorted insertion.
	candidates := make(locationCandidates, 0, limit+1)
	err = reader.ReadEntries(ctx, func(children []os.DirEntry) error {
		eligible := children[:0]
		for _, child := range children {
			name := child.Name()
			if name <= cursor.After || !strings.Contains(strings.ToLower(name), filter) || reader.Excluded(child) {
				continue
			}
			eligible = append(eligible, child)
		}
		var rows []*entity.LocationEntry
		var infos map[string]os.FileInfo
		indexes := make([]int, 0, len(eligible))
		if match != nil {
			var err error
			rows, infos, err = reader.Observe(ctx, eligible)
			if err != nil {
				return err
			}
			indexes, err = match(location, rows, infos)
			if err != nil {
				return err
			}
		} else {
			for index := range eligible {
				indexes = append(indexes, index)
			}
		}
		for _, index := range indexes {
			if index < 0 || index >= len(eligible) {
				return fmt.Errorf("invalid directory predicate result")
			}
			child := eligible[index]
			candidate := locationCandidate{name: child.Name(), child: child}
			if match != nil {
				candidate.entry, candidate.info = rows[index], infos[rows[index].Path]
			}
			if len(candidates) < limit+1 {
				heap.Push(&candidates, candidate)
				continue
			}
			if candidate.name < candidates[0].name {
				candidates[0] = candidate
				heap.Fix(&candidates, 0)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Only retained rows need metadata when the query has no observation predicate.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })
	result := &LocationEntriesRead{Page: &entity.LocationEntriesPage{Revision: location.Revision, HasMore: len(candidates) > limit}, Location: location, Parent: reader.Parent, Infos: make(map[string]os.FileInfo, min(len(candidates), limit))}
	if result.Page.HasMore {
		candidates = candidates[:limit]
	}
	for _, candidate := range candidates {
		if candidate.entry == nil {
			rows, infos, err := reader.Observe(ctx, []os.DirEntry{candidate.child})
			if err != nil {
				return nil, err
			}
			candidate.entry, candidate.info = rows[0], infos[rows[0].Path]
		}
		result.Page.Entries = append(result.Page.Entries, candidate.entry)
		result.Infos[candidate.entry.Path] = candidate.info
	}
	if result.Page.HasMore {
		cursor.After = candidates[len(candidates)-1].name
		data, err := json.Marshal(cursor)
		if err != nil {
			return nil, err
		}
		result.Page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return result, nil
}

// AdmitLocationEntry resolves one explicit ordinary-file selection under the Location gate.
func (e *Executor) AdmitLocationEntry(ctx context.Context, ref *entity.LocationEntryRef) (*library.FileLocation, error) {
	results, err := e.AdmitLocationEntries(ctx, []*entity.LocationEntryRef{ref})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}
