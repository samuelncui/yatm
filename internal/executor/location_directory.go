package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"github.com/samuelncui/yatm/internal/library"
)

// LocationDirectory retains one enumeration, with metadata read only for the emitted batch.
type LocationDirectory struct {
	*LocationDirectoryReader
	entries []os.DirEntry
}

func (d *LocationDirectory) Len() int { return len(d.entries) }

// LocationDirectoryEntry retains a child failure without inventing a usable Location reference.
type LocationDirectoryEntry struct {
	Path  string
	Type  os.FileMode
	Entry *entity.LocationEntry
	Error error
}

// Read observes a bounded slice under the directory already validated by enumeration.
func (d *LocationDirectory) Read(ctx context.Context, start, end int) ([]LocationDirectoryEntry, map[string]os.FileInfo, error) {
	// Preserve every enumerated child; only cancellation stops the listing's observation batch.
	rows := make([]LocationDirectoryEntry, 0, end-start)
	infos := make(map[string]os.FileInfo, end-start)
	for _, child := range d.entries[start:end] {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		row := LocationDirectoryEntry{Path: path.Join(d.Parent.Path, child.Name()), Type: child.Type()}
		entry, info, err := d.observe(row.Path, child)
		row.Entry, row.Error = entry, err
		rows = append(rows, row)
		if err == nil {
			infos[row.Path] = info
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return rows, infos, nil
}

// Observe returns entries and reusable stat metadata for one bounded physical batch.
func (d *LocationDirectoryReader) Observe(ctx context.Context, children []os.DirEntry) ([]*entity.LocationEntry, map[string]os.FileInfo, error) {
	// Carry the same metadata into status projection instead of restatting associated entries.
	rows := make([]*entity.LocationEntry, 0, len(children))
	infos := make(map[string]os.FileInfo, len(children))
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		relative := path.Join(d.Parent.Path, child.Name())
		entry, info, err := d.observe(relative, child)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, entry)
		infos[relative] = info
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return rows, infos, nil
}

func (d *LocationDirectoryReader) observe(relative string, child os.DirEntry) (*entity.LocationEntry, os.FileInfo, error) {
	// Unsupported names cannot enter the wire contract or an actionable physical reference.
	if err := entity.ValidateRelativePath(relative); err != nil {
		return nil, nil, fmt.Errorf("read entry %q failed, %w", relative, err)
	}

	// The same single observation supplies display facts and each strict workflow consumer.
	info, err := child.Info()
	if err != nil {
		return nil, nil, fmt.Errorf("observe %q failed, %w", relative, err)
	}
	entry, err := locationEntry(d.Location.ID, relative, info)
	if err != nil {
		return nil, nil, fmt.Errorf("observe %q failed, %w", relative, err)
	}
	return entry, info, nil
}

func locationEntry(id int64, relative string, info os.FileInfo) (*entity.LocationEntry, error) {
	facts, err := InspectLocationFacts(info)
	if err != nil {
		return nil, err
	}
	return &entity.LocationEntry{Path: relative, Directory: info.IsDir(), Reference: &entity.LocationEntryRef{LocationId: id, Path: relative, Facts: facts}}, nil
}

// EnumerateLocationDirectory collects every allowed child and sorts once, without a row cap.
func (e *Executor) EnumerateLocationDirectory(ctx context.Context, id int64, relative string) (*LocationDirectory, error) {
	// Resolve the source and authorize its directory once before opening the read lifetime.
	location, err := e.lib.GetLocation(ctx, id)
	if err != nil {
		return nil, err
	}
	relative = strings.TrimSuffix(relative, "/")
	full, info, err := e.CheckLocationPath(location, relative)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("selected path is not a directory")
	}
	reader, err := e.newLocationDirectoryReader(location, relative, full, info)
	if err != nil {
		return nil, err
	}
	result := &LocationDirectory{LocationDirectoryReader: reader}
	if err := reader.ReadEntries(ctx, func(children []os.DirEntry) error {
		for _, child := range children {
			if reader.Excluded(child) {
				continue
			}
			result.entries = append(result.entries, child)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// The client needs lexical order and an exact total before its first projected batch.
	sort.Slice(result.entries, func(i, j int) bool { return result.entries[i].Name() < result.entries[j].Name() })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// locationChildAccess compiles ancestor decisions once; overlapping administrator ranges are alternatives.
// The returned function is used by the directory reader only, not shared between workers.
func (e *Executor) locationChildAccess(location *library.Location, relative string) func(string, bool) bool {
	// Directory scopes share the exact range binding used by explicit Location reads.
	ranges := e.locationAccessRanges(location.RootPath)
	scopes := make([]*ignore.Scope, 0, len(ranges))
	for _, access := range ranges {
		prefix := path.Join(access.prefix, relative)
		if prefix == "." {
			prefix = ""
		}
		scopes = append(scopes, access.matcher.Scope(prefix))
	}
	return func(name string, directory bool) bool {
		for _, scope := range scopes {
			if !scope.Ignores(name, directory) {
				return true
			}
		}
		return false
	}
}

// LocationDirectoryReader owns one prepared parent's authorization and scoped rules.
// ReadEntries exposes every physical child; each consumer chooses its exclusion policy.
type LocationDirectoryReader struct {
	Location *library.Location
	Parent   *entity.LocationEntry
	full     string
	allowed  func(string, bool) bool
	ignores  *ignore.Scope
}

func (e *Executor) newLocationDirectoryReader(location *library.Location, relative, full string, info os.FileInfo) (*LocationDirectoryReader, error) {
	parent, err := locationEntry(location.ID, relative, info)
	if err != nil {
		return nil, err
	}
	return &LocationDirectoryReader{Location: location, Parent: parent, full: full,
		allowed: e.locationChildAccess(location, relative), ignores: location.IgnoreScope(relative)}, nil
}

// PrepareLocationDirectory validates a parent within an already checked Location.
func (e *Executor) PrepareLocationDirectory(location *library.Location, relative string) (*LocationDirectoryReader, error) {
	full, info, err := e.checkPreparedLocationPath(location, location.RootPath, relative)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("selected entry is not a directory")
	}
	return e.newLocationDirectoryReader(location, relative, full, info)
}

// Allowed applies mandatory administrator rules to a child without reconstructing its ancestry.
func (d *LocationDirectoryReader) Allowed(name string, directory bool) bool {
	return d.allowed(name, directory)
}

// Excluded is the browsing/collection policy; physical mutation callers use Allowed instead.
func (d *LocationDirectoryReader) Excluded(child os.DirEntry) bool {
	return !d.Allowed(child.Name(), child.IsDir()) || d.ignores.Ignores(child.Name(), child.IsDir())
}

// ReadEntries opens once and streams bounded physical batches, closing on every exit.
func (d *LocationDirectoryReader) ReadEntries(ctx context.Context, visit func([]os.DirEntry) error) error {
	// Own the descriptor through cancellation and consumer failures as well as normal completion.
	directory, err := os.Open(d.full)
	if err != nil {
		return err
	}
	defer directory.Close()
	return readLocationEntries(ctx, directory.ReadDir, visit)
}

func readLocationEntries(ctx context.Context, read func(int) ([]os.DirEntry, error), visit func([]os.DirEntry) error) error {
	// Keep sequential consumption independent of filtering, projection and workflow publication.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := read(256)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(entries) > 0 {
			if err := visit(entries); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if readErr == io.EOF {
			return nil
		}
	}
}
