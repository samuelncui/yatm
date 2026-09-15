package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

// walk visits only managed ordinary files, retaining a bounded directory page at each depth.
func walk(ctx context.Context, source *library.Location, relative string, depth int, yield func(string, os.FileInfo) error) error {
	// Prune ignored subtrees before opening them; excessive depth is a retryable scan error.
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.Excluded(relative, true) {
		return nil
	}
	if depth > 256 {
		return fmt.Errorf("online source exceeds 256 directory levels")
	}
	directory, err := os.Open(filepath.Join(source.RootPath, filepath.FromSlash(relative)))
	if err != nil {
		return fmt.Errorf("open online directory %q failed, %w", relative, err)
	}
	defer directory.Close()

	// ReadDir errors are never interpreted as an empty or partially successful directory.
	for {
		entries, err := directory.ReadDir(batchSize)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read online directory %q failed, %w", relative, err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if relative != "" {
				name = relative + "/" + name
			}
			if source.Excluded(name, entry.IsDir()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect online file %q failed, %w", name, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				if err := walk(ctx, source, name, depth+1, yield); err != nil {
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
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (r *locationStage) enumerate(ctx context.Context, source *library.Location, policy entity.ScanSignaturePolicy) error {
	// Persist bounded pages before any sorting or content reading; indexes provide path order.
	batch := make([]*Item, 0, batchSize)
	var files, total int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.db.WithContext(ctx).Create(&batch).Error; err != nil {
			return err
		}
		batch = batch[:0]
		r.progress.SetGlobalTotal(total, files)
		return nil
	}

	// Each selected range can fail independently without publishing a partial range.
	return r.eachScope(ctx, func(scope *Scope) error {
		if scope.Error != "" {
			return nil
		}
		if err := walkSelection(ctx, source, scope.Path, func(name string, info os.FileInfo) error {
			fullPath := filepath.Join(source.RootPath, filepath.FromSlash(name))
			evidence, err := observeEvidence(source, fullPath, info)
			if err != nil {
				return err
			}
			item := &Item{Path: name, ScopePath: scope.Path, Change: entity.ScanChange_SCAN_CHANGE_ADDED, NeedsHash: policy != entity.ScanSignaturePolicy_KNOWN_ONLY,
				Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), Evidence: evidence}
			item.CopyResult, err = r.exe.Lib().CopyAdmission(ctx, source.ID, source.BindingToken, executor.LocationFacts(info).Identity)
			if err != nil {
				return err
			}
			if policy != entity.ScanSignaturePolicy_FORCE_READ {
				cached, valid, err := reusableSignature(fullPath)
				if err != nil {
					return err
				}
				if valid && cached.Size == item.Size && cached.MtimeNS == item.MtimeNS {
					applyHash(item, cached.SHA256[:])
				}
			}
			batch = append(batch, item)
			files++
			total += info.Size()
			if len(batch) == batchSize {
				return flush()
			}
			return nil
		}); err != nil {
			batch = batch[:0]
			if err := r.failScope(ctx, scope, err); err != nil {
				return err
			}
			return nil
		}
		return flush()
	})
}

func (r *locationStage) reconcile(ctx context.Context, source *library.Location, force bool) error {
	// Merge ordered old pages with bounded indexed path lookups in the new manifest.
	var after string
	for {
		old, err := r.exe.Lib().OnlineFilesPage(ctx, source.ID, after, batchSize)
		if err != nil {
			return err
		}
		if len(old) == 0 {
			return r.snapshotTracking(ctx, source)
		}
		paths := make([]string, 0, len(old))
		for _, row := range old {
			paths = append(paths, row.Path)
		}
		var items []*Item
		if err := r.db.WithContext(ctx).Where("path IN ?", paths).Find(&items).Error; err != nil {
			return err
		}
		found := make(map[string]*Item, len(items))
		for _, item := range items {
			found[item.Path] = item
		}

		// Restored files may have current observations even before the first complete directory scan.
		if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, previous := range old {
				covered, scope, err := r.covered(ctx, tx, source, previous.Path)
				if err != nil {
					return err
				}
				if !covered {
					continue
				}
				original := &Original{FileID: previous.FileID, Path: previous.Path}
				current := source.BindingToken != "" && previous.ObservedBindingToken == source.BindingToken
				if current {
					original.Signature = previous.Signature
					original.Hash, original.Size = previous.Hash, previous.Size
				}
				if err := tx.Create(original).Error; err != nil {
					return err
				}
				item := found[previous.Path]
				if item == nil {
					if err := tx.Create(&Item{Path: previous.Path, ScopePath: scope.Path, Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Before: previous.ToEntity()}).Error; err != nil {
						return err
					}
					continue
				}
				item.Before, item.Change = previous.ToEntity(), entity.ScanChange_SCAN_CHANGE_CHANGED
				unchanged := item.Size == previous.Size && item.Mode == previous.Mode && item.MtimeNS == previous.MtimeNS && !item.Evidence.ReplacedNative(previous.TrackingKeys)
				if unchanged && current && !force {
					item.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
					if len(previous.Hash) == 32 && len(item.Hash) == 0 {
						item.Hash, item.Signature, item.NeedsHash = previous.Hash, previous.Signature, false
					}
				}
				if len(item.Hash) == 32 {
					applyHash(item, item.Hash)
				}
				if err := tx.Save(item).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after = old[len(old)-1].Path
	}
}

func (r *locationStage) eachScope(ctx context.Context, use func(*Scope) error) error {
	// Range manifests can contain arbitrarily many expanded Library selections.
	var after int64
	for {
		var scopes []*Scope
		if err := r.db.WithContext(ctx).Where("location_id = ? AND id > ?", r.source.ID, after).Order("id").Limit(batchSize).Find(&scopes).Error; err != nil {
			return err
		}
		if len(scopes) == 0 {
			return nil
		}
		for _, scope := range scopes {
			if err := use(scope); err != nil {
				return err
			}
		}
		after = scopes[len(scopes)-1].ID
	}
}

func (r *locationStage) validate(ctx context.Context, source *library.Location) error {
	// Failed ranges never participate in absence reasoning or identity matching.
	return r.eachScope(ctx, func(scope *Scope) error {
		if scope.Error != "" {
			return nil
		}
		if err := r.validateScope(ctx, source, scope.Path); err != nil {
			return r.failScope(ctx, scope, err)
		}
		return nil
	})
}

func (r *locationStage) validateScope(ctx context.Context, source *library.Location, scope string) error {
	// A fresh traversal must observe the complete same managed membership and metadata.
	var seen int64
	if err := walkSelection(ctx, source, scope, func(name string, info os.FileInfo) error {
		var item Item
		if err := r.db.WithContext(ctx).Where("path = ? AND change != ?", name, entity.ScanChange_SCAN_CHANGE_REMOVED).First(&item).Error; err != nil {
			return fmt.Errorf("online membership changed at %q, %w", name, err)
		}
		if item.NeedsHash || !executor.OnlineFactsMatch(item.Position(source.ID), info) {
			return fmt.Errorf("online file changed during analysis: %q", name)
		}
		current, err := observeEvidence(source, filepath.Join(source.RootPath, filepath.FromSlash(name)), info)
		if err != nil {
			return err
		}
		if current.ReplacedNative(item.Evidence.Keys()) {
			return fmt.Errorf("online file replaced during analysis: %q", name)
		}
		seen++
		return nil
	}); err != nil {
		return err
	}

	// Missing files cannot be hidden by a successful enumeration of the surviving subset.
	var expected int64
	if err := r.db.WithContext(ctx).Model(&Item{}).Where("scope_path = ? AND change != ?", scope, entity.ScanChange_SCAN_CHANGE_REMOVED).Count(&expected).Error; err != nil {
		return err
	}
	if seen != expected {
		return fmt.Errorf("online membership changed during analysis, observed=%d expected=%d", seen, expected)
	}
	return nil
}

// walkSelection bypasses user Ignore only for an explicitly selected ordinary file.
func walkSelection(ctx context.Context, source *library.Location, selected string, yield func(string, os.FileInfo) error) error {
	// Resolve each path without following symlink components or escaping its registered root.
	if selected == "" {
		return walk(ctx, source, "", 0, yield)
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
		return walk(ctx, source, selected, 0, yield)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("analysis selection %q is not an ordinary file or directory", selected)
	}

	// Ignore is not access authorization; explicit files still obey administrator/resource boundaries.
	if source.AccessAllowed != nil && !source.AccessAllowed(selected, false) {
		return executor.ErrAccessExcluded
	}
	for _, required := range source.RequiredExclusions {
		if selected == required || strings.HasPrefix(selected, required+"/") {
			return executor.ErrAccessExcluded
		}
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
		if len(result) > 0 {
			previous := result[len(result)-1].Path
			if previous == "" || value == previous || strings.HasPrefix(value, previous+"/") {
				continue
			}
		}
		result = append(result, &Scope{Path: value})
	}
	return result
}

func (r *locationStage) covered(ctx context.Context, db *gorm.DB, source *library.Location, path string) (bool, *Scope, error) {
	// Query observed range membership; an unavailable/unselected range cannot surrender its identities.
	var scope Scope
	result := db.WithContext(ctx).Where("location_id = ? AND error = ? AND (path = ? OR path = ? OR substr(?,1,length(path)+1) = path || '/')",
		source.ID, "", "", path, path).Order("length(path) DESC").Limit(1).Find(&scope)
	if result.Error != nil {
		return false, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil, nil
	}
	return scope.Path == path || !source.Excluded(path, false), &scope, nil
}

func (r *locationStage) failScope(ctx context.Context, scope *Scope, cause error) error {
	// Discard this range's tentative observations, retaining a diagnostic and its previous Library facts.
	scope.Error = cause.Error()
	r.logger.WithError(cause).WithField("path", scope.Path).Warn("Scan range incomplete")
	if err := r.db.WithContext(ctx).Save(scope).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Where("scope_path = ?", scope.Path).Delete(&Item{}).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Where("location_id = ? AND scope_path = ?", r.source.ID, scope.Path).Delete(&Entry{}).Error; err != nil {
		return err
	}
	return ctx.Err()
}

func applyHash(item *Item, hash []byte) {
	// Preserve a known opaque identity only when the newly read content matches its previous observation.
	item.Hash, item.NeedsHash = hash, false
	item.Signature, _ = library.NewFileSignature(hash, item.Size)
	if item.Before == nil {
		return
	}
	item.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
	if item.Before.Size != item.Size || !bytes.Equal(item.Before.Sha256, hash) {
		return
	}
	if len(item.Before.Signature) > 0 {
		item.Signature = item.Before.Signature
	}
	if item.Before.Mode == item.Mode && item.Before.MtimeNs == item.MtimeNS {
		item.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
	}
}
