package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/protobuf/proto"
)

// LocationFacts describes a filesystem observation without reading file content.
func LocationFacts(info os.FileInfo) *entity.LocationFileFacts {
	return &entity.LocationFileFacts{Size: info.Size(), Mode: uint32(info.Mode()), MtimeNs: info.ModTime().UnixNano(), Identity: locationNativeIdentity(info)}
}

// CheckLocationPath validates administrator boundaries, independently of index Ignore rules.
// The leaf may be a symlink; no ancestor may be one.
func (e *Executor) CheckLocationPath(location *library.Location, relative string) (string, os.FileInfo, error) {
	// Imported bindings cannot authorize reads or writes until locally confirmed.
	if location.Binding != entity.OnlineBinding_CONFIRMED || location.BindingToken == "" {
		return "", nil, library.ErrOnlineUnverified
	}
	root, err := e.CheckOnlineSource(location)
	if err != nil {
		return "", nil, err
	}
	if relative != "" {
		if err := entity.ValidateRelativePath(relative); err != nil {
			return "", nil, err
		}
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	if relative != "" {
		if _, err := e.OnlineRoot(filepath.Dir(full)); err != nil {
			return "", nil, err
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", nil, err
	}

	// Runtime/archive exclusions and administrator rules are mandatory even for explicit selections.
	if !location.AccessAllowed(relative, info.IsDir()) {
		return "", nil, ErrAccessExcluded
	}
	for _, excluded := range location.RequiredExclusions {
		if relative == excluded || strings.HasPrefix(relative, excluded+"/") {
			return "", nil, ErrAccessExcluded
		}
	}
	return full, info, nil
}

// ResolveLocationEntry rejects stale object selections, without requiring catalog admission.
func (e *Executor) ResolveLocationEntry(ctx context.Context, ref *entity.LocationEntryRef) (*library.Location, string, os.FileInfo, error) {
	// A concrete expected observation is mandatory for mutation and content access.
	if ref == nil || ref.LocationId <= 0 || ref.Facts == nil || ref.BindingToken == "" {
		return nil, "", nil, fmt.Errorf("Location selection requires a binding and object facts")
	}
	location, err := e.lib.GetOnlineSource(ctx, ref.LocationId)
	if err != nil {
		return nil, "", nil, err
	}
	if location.BindingToken != ref.BindingToken {
		return nil, "", nil, library.ErrOnlineConflict
	}
	full, info, err := e.CheckLocationPath(location, ref.Path)
	if err != nil {
		return nil, "", nil, err
	}
	if !proto.Equal(ref.Facts, LocationFacts(info)) {
		return nil, "", nil, library.ErrOnlineConflict
	}
	return location, full, info, nil
}

// ObserveLocationEntry returns the actual object; its File association is optional.
func (e *Executor) ObserveLocationEntry(ctx context.Context, locationID int64, relative string) (*entity.LocationEntry, error) {
	location, err := e.lib.GetOnlineSource(ctx, locationID)
	if err != nil {
		return nil, err
	}
	_, info, err := e.CheckLocationPath(location, relative)
	if err != nil {
		return nil, err
	}
	return &entity.LocationEntry{Path: relative, IsDir: info.IsDir(), Reference: &entity.LocationEntryRef{
		LocationId: location.ID, Path: relative, BindingToken: location.BindingToken, Facts: LocationFacts(info)}}, nil
}

type locationCursor struct {
	LocationID int64
	Binding    string
	Path       string
	Filter     string
	Facts      *entity.LocationFileFacts
	After      string
}

// ListLocationEntries keeps only a bounded lexical page, never a cached physical tree.
func (e *Executor) ListLocationEntries(ctx context.Context, req *entity.ListLocationEntriesRequest) (*entity.ListLocationEntriesReply, error) {
	return e.ListLocationEntriesMatching(ctx, req, nil)
}

// ListLocationEntriesMatching applies a read-only batch predicate during one
// directory enumeration. The caller binds the predicate to its public cursor.
func (e *Executor) ListLocationEntriesMatching(ctx context.Context, req *entity.ListLocationEntriesRequest, match func([]*entity.LocationEntry) ([]int, error)) (*entity.ListLocationEntriesReply, error) {
	// Bind every continuation to the same directory observation and query.
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid directory page size")
	}
	location, err := e.lib.GetOnlineSource(ctx, req.GetLocationId())
	if err != nil {
		return nil, err
	}
	parent := strings.TrimSuffix(req.GetParentPath(), "/")
	full, before, err := e.CheckLocationPath(location, parent)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("selected path is not a directory")
	}
	filter := strings.ToLower(req.GetNameFilter())
	cursor := locationCursor{LocationID: location.ID, Binding: location.BindingToken, Path: parent, Filter: filter, Facts: LocationFacts(before)}
	if req.GetCursor() != "" {
		var stored locationCursor
		data, decodeErr := base64.RawURLEncoding.DecodeString(req.Cursor)
		if len(req.Cursor) > 16384 || decodeErr != nil || json.Unmarshal(data, &stored) != nil {
			return nil, fmt.Errorf("invalid directory cursor")
		}
		if stored.LocationID != cursor.LocationID || stored.Binding != cursor.Binding || stored.Path != parent || stored.Filter != filter || !proto.Equal(stored.Facts, cursor.Facts) {
			return nil, library.ErrOnlineConflict
		}
		cursor.After = stored.After
	}
	directory, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer directory.Close()

	// Enumerate in fixed batches and retain at most limit+1 candidates.
	names := make([]string, 0, limit+1)
	observed := make(map[string]*entity.LocationEntry, limit+1)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, readErr := directory.ReadDir(256)
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		candidates := make([]string, 0, len(entries))
		for _, entry := range entries {
			name := entry.Name()
			if name <= cursor.After || !strings.Contains(strings.ToLower(name), filter) {
				continue
			}
			relative := path.Join(parent, name)
			if !location.AccessAllowed(relative, entry.IsDir()) {
				continue
			}
			protected := false
			for _, excluded := range location.RequiredExclusions {
				if relative == excluded || strings.HasPrefix(relative, excluded+"/") {
					protected = true
					break
				}
			}
			if protected {
				continue
			}
			candidates = append(candidates, name)
		}
		batch := make(map[string]*entity.LocationEntry, len(candidates))
		if match != nil {
			rows := make([]*entity.LocationEntry, 0, len(candidates))
			for _, name := range candidates {
				entry, err := e.ObserveLocationEntry(ctx, location.ID, path.Join(parent, name))
				if err != nil {
					return nil, err
				}
				rows = append(rows, entry)
				batch[name] = entry
			}
			indexes, err := match(rows)
			if err != nil {
				return nil, err
			}
			matching := make([]string, 0, len(indexes))
			for _, index := range indexes {
				if index < 0 || index >= len(candidates) {
					return nil, fmt.Errorf("invalid directory predicate result")
				}
				matching = append(matching, candidates[index])
			}
			candidates = matching
		}
		for _, name := range candidates {
			at := sort.SearchStrings(names, name)
			if at > limit {
				continue
			}
			names = append(names, "")
			copy(names[at+1:], names[at:])
			names[at] = name
			if match != nil {
				observed[name] = batch[name]
			}
			if len(names) > limit+1 {
				delete(observed, names[limit+1])
				names = names[:limit+1]
			}
		}
		if readErr == io.EOF {
			break
		}
	}

	// Return actual facts, then reject detectable drift instead of joining incompatible pages.
	reply := &entity.ListLocationEntriesReply{Revision: location.Revision, HasMore: len(names) > limit}
	if reply.HasMore {
		names = names[:limit]
	}
	for _, name := range names {
		entry, err := e.ObserveLocationEntry(ctx, location.ID, path.Join(parent, name))
		if err != nil {
			return nil, err
		}
		if prior := observed[name]; prior != nil && !proto.Equal(prior.Reference, entry.Reference) {
			return nil, library.ErrOnlineConflict
		}
		reply.Entries = append(reply.Entries, entry)
	}
	after, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	current, err := e.lib.GetOnlineSource(ctx, location.ID)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !proto.Equal(cursor.Facts, LocationFacts(after)) || current.BindingToken != location.BindingToken {
		return nil, library.ErrOnlineConflict
	}
	if reply.HasMore {
		cursor.After = names[len(names)-1]
		data, err := json.Marshal(cursor)
		if err != nil {
			return nil, err
		}
		reply.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return reply, nil
}

// AdmitLocationEntry resolves one explicit ordinary-file selection under the Location gate.
func (e *Executor) AdmitLocationEntry(ctx context.Context, ref *entity.LocationEntryRef) (*library.FileLocation, error) {
	results, err := e.AdmitLocationEntries(ctx, []*entity.LocationEntryRef{ref})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// OpenLocationEntry uses the validated descriptor for response bytes; no hash is required.
func (e *Executor) OpenLocationEntry(ctx context.Context, ref *entity.LocationEntryRef) (*os.File, error) {
	_, name, info, err := e.ResolveLocationEntry(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("only ordinary files can be opened")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err == nil && (!os.SameFile(info, opened) || !proto.Equal(ref.Facts, LocationFacts(opened))) {
		err = library.ErrOnlineConflict
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
