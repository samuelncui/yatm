package scan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm/clause"
)

// walk visits only managed ordinary files, retaining a bounded directory page at each depth.
func walk(ctx context.Context, exe *executor.Executor, source *library.Location, relative string, depth int, yield func(string, os.FileInfo) error) error {
	// Scan owns its directory-depth and Trash policy; enumeration and access belong to Executor.
	if err := ctx.Err(); err != nil {
		return err
	}
	if executor.IsLocationTrashPath(relative) || source.IgnoreScope(relative).Ignored() {
		return nil
	}
	if depth > 256 {
		return fmt.Errorf("Location path exceeds 256 directory levels")
	}
	reader, err := exe.PrepareLocationDirectory(source, relative)
	if err != nil {
		return err
	}

	// Each physical directory is consumed once and leaves reuse its metadata observation.
	return reader.ReadEntries(ctx, func(entries []os.DirEntry) error {
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if relative != "" {
				name = relative + "/" + name
			}
			if err := entity.ValidateRelativePath(name); err != nil {
				return fmt.Errorf("invalid Location entry %q, %w", name, err)
			}
			if executor.IsLocationTrashPath(name) || reader.Excluded(entry) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect online file %q failed, %w", name, err)
			}
			if info.IsDir() {
				if err := walk(ctx, exe, source, name, depth+1, yield); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if err := yield(name, info); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *locationStage) enumerate(ctx context.Context, policy entity.ScanSignaturePolicy) error {
	// Physical roots are collapsed before traversal and share one bounded enrichment buffer.
	batch := make([]*Entry, 0, batchSize)
	flush := func() error {
		if err := s.saveObservations(ctx, batch, nil, policy); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for _, scope := range s.scopes {
		if scope.LocationID != s.source.ID {
			continue
		}
		if err := walkSelection(ctx, s.exe, s.source, scope.Path, func(name string, info os.FileInfo) error {
			row, err := s.observation(name, scope.Path, info, policy)
			if err != nil {
				return err
			}
			batch = append(batch, row)
			if len(batch) == batchSize {
				return flush()
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return flush()
}

func (s *locationStage) observation(name, scope string, info os.FileInfo, policy entity.ScanSignaturePolicy) (*Entry, error) {
	facts, err := executor.InspectLocationFacts(info)
	if err != nil {
		return nil, fmt.Errorf("inspect Location entry %q failed, %w", name, err)
	}
	return &Entry{LocationID: s.source.ID, Path: name, ScopePath: scope,
		SourcePath: filepath.Join(s.source.RootPath, filepath.FromSlash(name)),
		Change:     entity.ScanChange_SCAN_CHANGE_ADDED, NeedsHash: policy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		Size: facts.SizeBytes, Mode: facts.Mode, MtimeNS: facts.MtimeNs, Evidence: observeEvidence(s.source, info)}, nil
}

func (s *locationStage) saveObservations(ctx context.Context, rows []*Entry, originals map[string]*library.FileLocation, policy entity.ScanSignaturePolicy) error {
	if len(rows) == 0 {
		return nil
	}

	// Logical leaves already carry their originals; physical observations batch-read only missing facts.
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, ok := originals[row.Path]; !ok {
			paths = append(paths, row.Path)
		}
	}
	found, err := s.exe.Lib().ReadFileOriginalsAt(ctx, s.source.ID, paths)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if old := originals[row.Path]; old != nil {
			found[row.Path] = old
		}
		if old := found[row.Path]; old != nil {
			ids = append(ids, old.FileID)
		}
	}
	keys, err := s.exe.Lib().ReadFileTrackingKeys(ctx, ids)
	if err != nil {
		return err
	}

	// Cache-only reuse and native evidence follow the same policy for every selection source.
	for _, row := range rows {
		if policy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
			cached, valid, err := reusableSignature(row.SourcePath)
			if err != nil {
				return err
			}
			if valid && cached.Size == row.Size && cached.MtimeNS == row.MtimeNS {
				applyHash(row, cached.SHA256[:])
			}
		}
		old := found[row.Path]
		if old == nil {
			continue
		}
		// Exact path continuity wins before every relocation rule; the batched lookup already resolved it.
		row.FileID = old.FileID
		row.Before, row.Change = old.Observation(), entity.ScanChange_SCAN_CHANGE_CHANGED
		unchanged := row.Size == old.Size && row.Mode == old.Mode && row.MtimeNS == old.MtimeNS && !row.Evidence.ReplacedNative(keys[old.FileID])
		if unchanged && policy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
			row.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
			if len(old.Hash) == 32 && len(row.SHA256) == 0 {
				row.SHA256, row.Signature, row.NeedsHash = old.Hash, old.Signature, false
			}
		}
		if len(row.SHA256) == 32 {
			applyHash(row, row.SHA256)
		}
	}

	// The existing Location/path key owns overlap removal; no second selection table is needed.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, batchSize).Error
}

func (s *locationStage) reconcile(ctx context.Context) error {
	// Logical leaves establish only observed membership; absence belongs to explicit physical ranges.
	for _, scope := range s.scopes {
		if scope.LocationID != s.source.ID {
			continue
		}
		if scope.Path != "" {
			old, err := s.exe.Lib().ReadFileOriginalsAt(ctx, s.source.ID, []string{scope.Path})
			if err != nil {
				return err
			}
			if row := old[scope.Path]; row != nil {
				if err := s.recordAbsent(ctx, scope, []*library.FileLocation{row}); err != nil {
					return err
				}
			}
		}

		// Seek directly to each descendant prefix and stop on the first page beyond its range.
		prefix := ""
		if scope.Path != "" {
			prefix = scope.Path + "/"
		}
		after := prefix
		for {
			rows, err := s.exe.Lib().LocationOriginalsPage(ctx, s.source.ID, after, batchSize)
			if err != nil {
				return err
			}
			end := len(rows)
			for i, row := range rows {
				if !strings.HasPrefix(row.Path, prefix) {
					end = i
					break
				}
			}
			if err := s.recordAbsent(ctx, scope, rows[:end]); err != nil {
				return err
			}
			if end < len(rows) || len(rows) < batchSize {
				break
			}
			after = rows[len(rows)-1].Path
		}
	}
	return nil
}

func (s *locationStage) recordAbsent(ctx context.Context, scope *Scope, rows []*library.FileLocation) error {
	// Ignore and administrator exclusions never establish an absent original.
	paths := make([]string, 0, len(rows))
	absent := make(map[string]*library.FileLocation, len(rows))
	for _, row := range rows {
		if executor.IsLocationTrashPath(row.Path) || s.source.Excluded(row.Path, false) {
			continue
		}
		paths = append(paths, row.Path)
		absent[row.Path] = row
	}
	if len(paths) == 0 {
		return nil
	}
	var present []string
	if err := s.db.WithContext(ctx).Model(&Entry{}).Where("location_id = ? AND path IN ?", s.source.ID, paths).
		Pluck("path", &present).Error; err != nil {
		return err
	}
	for _, name := range present {
		delete(absent, name)
	}

	// Write only missing paths, in a single bounded batch; positive observations were enriched once.
	entries := make([]*Entry, 0, len(absent))
	for _, name := range paths {
		row := absent[name]
		if row == nil {
			continue
		}
		entries = append(entries, &Entry{LocationID: s.source.ID, Path: row.Path, ScopePath: scope.Path,
			Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Before: row.Observation()})
	}
	return s.saveEntries(ctx, entries)
}

func (s *locationStage) explicitScope(name string) *Scope {
	for _, scope := range s.scopes {
		if scope.LocationID == s.source.ID && (scope.Path == "" || scope.Path == name || strings.HasPrefix(name, scope.Path+"/")) {
			return scope
		}
	}
	return nil
}

// walkSelection resolves selected paths under one Location, applying Ignore to files and directories.
func walkSelection(ctx context.Context, exe *executor.Executor, source *library.Location, selected string, yield func(string, os.FileInfo) error) error {
	// Resolve each path without following symlink components or escaping its registered root.
	if executor.IsLocationTrashPath(selected) {
		return fmt.Errorf("Trash content cannot be selected for Scan")
	}
	if selected == "" {
		return walk(ctx, exe, source, "", 0, yield)
	}
	if err := entity.ValidateRelativePath(selected); err != nil {
		return err
	}
	full := source.RootPath
	var info os.FileInfo
	for _, component := range strings.Split(selected, "/") {
		full = filepath.Join(full, component)
		current, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if current.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("analysis path contains a symbolic link: %q", selected)
		}
		info = current
	}
	if info.IsDir() {
		return walk(ctx, exe, source, selected, 0, yield)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("analysis selection %q is not an ordinary file or directory", selected)
	}

	// Administrator boundaries stay explicit errors; Location Ignore excludes the named file.
	if source.AccessExcluded(selected, false) {
		return executor.ErrAccessExcluded
	}
	if source.Excluded(selected, false) {
		return nil
	}
	return yield(selected, info)
}

func analysisScopes(paths []string) []*Scope {
	// Explicit overlapping roots collapse before traversal; at most the requested roots stay in memory.
	if len(paths) == 0 {
		return []*Scope{{}}
	}
	values := append([]string{}, paths...)
	sort.Strings(values)
	var result []*Scope
	for _, value := range values {
		covered := false
		for _, scope := range result {
			previous := scope.Path
			if previous == "" || value == previous || strings.HasPrefix(value, previous+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, &Scope{Path: value})
		}
	}
	return result
}

func applyHash(item *Item, hash []byte) {
	// Preserve a known opaque identity only when the newly read content matches its previous observation.
	item.SHA256, item.NeedsHash = hash, false
	item.Signature, _ = library.NewFileSignature(hash, item.Size)
	if item.Before == nil {
		return
	}
	item.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
	if item.Before.SizeBytes != item.Size || !bytes.Equal(item.Before.Sha256, hash) {
		return
	}
	if len(item.Before.Signature) > 0 {
		item.Signature = item.Before.Signature
	}
	if item.Before.Mode == item.Mode && item.Before.MtimeNs == item.MtimeNS {
		item.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
	}
}
