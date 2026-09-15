package treeops

import (
	"bytes"
	"context"
	"fmt"
)

func (e *Engine) Roots(ctx context.Context) ([]Root, error) {
	var roots []Root
	err := e.db.WithContext(ctx).Order("id").Limit(1000).Find(&roots).Error
	return roots, err
}

// Totals counts planned results, not internal observations of a native move.
func (e *Engine) Totals(ctx context.Context) (int64, int64, error) {
	var summary struct{ Count, Size int64 }
	err := e.db.WithContext(ctx).Model(&step{}).Select("COUNT(*) AS count, COALESCE(SUM(size), 0) AS size").Where("check_only = ?", false).Scan(&summary).Error
	return summary.Count, summary.Size, err
}

func (e *Engine) Run(ctx context.Context, emit func(Result) error) error {
	// Each logical root has a metadata transaction; physical outcomes settle individually.
	roots, err := e.Roots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		var emittedThrough int64
		if root.Error == "" {
			var settled func(step) error
			if e.transaction == nil {
				settled = func(item step) error {
					if err := emit(stepResult(item, "")); err != nil {
						return err
					}
					emittedThrough = item.ID
					return nil
				}
			}
			run := func(store Store) error { return e.runRoot(ctx, store, &root, settled) }
			var runErr error
			if e.transaction != nil {
				runErr = e.transaction(ctx, run)
			} else {
				runErr = run(e.store)
			}
			if runErr != nil {
				root.Error = runErr.Error()
				if e.transaction != nil {
					if err := e.db.WithContext(ctx).Model(&step{}).Where("root_id = ? AND check_only = ?", root.ID, false).
						Updates(map[string]any{"outcome": Failed, "error": root.Error}).Error; err != nil {
						return err
					}
				}
			}
		}

		// Only committed metadata success is visible; physical partial results retain their paths.
		if err := e.each(ctx, root.ID, func(item step) error {
			if item.CheckOnly || item.ID <= emittedThrough {
				return nil
			}
			return emit(stepResult(item, root.Error))
		}); err != nil {
			return err
		}
	}
	return nil
}

func stepResult(item step, rootError string) Result {
	outcome := item.Outcome
	if outcome == "" {
		outcome = Unprocessed
	}
	errText := item.Error
	if outcome == Unprocessed && rootError != "" {
		errText = "not executed: " + rootError
	}
	var fileID int64
	if outcome == Succeeded {
		fileID = item.Result.FileID
	}
	return Result{ID: item.ID, Source: item.Source, Target: item.Target, FileID: fileID, Outcome: outcome, Error: errText}
}

func (e *Engine) runRoot(ctx context.Context, store Store, root *Root, settled func(step) error) error {
	// Revalidate complete native-move trees before changing any selected source.
	if err := e.each(ctx, root.ID, func(item step) error {
		if !item.Source.Exists {
			return nil
		}
		current, err := store.Stat(ctx, item.Source.Ref)
		if err != nil {
			return err
		}
		if !current.Exists || !bytes.Equal(current.Guard, item.Source.Guard) {
			return fmt.Errorf("source changed: %q", item.Source.Path)
		}
		return nil
	}); err != nil {
		return e.failFirst(ctx, root.ID, err)
	}

	// Ordered primitives preserve their actual per-entry result before proceeding.
	return e.each(ctx, root.ID, func(item step) error {
		if item.CheckOnly {
			return nil
		}
		if item.Target.Parent != "" && len(item.Target.ParentGuard) == 0 {
			var created step
			if err := e.db.WithContext(ctx).Where("id < ? AND target_ref = ? AND outcome = ?", item.ID, item.Target.Parent, Succeeded).Order("id DESC").Limit(1).Find(&created).Error; err != nil {
				return err
			}
			if created.ID != 0 {
				item.Target.ParentGuard = created.Result.Guard
			}
		}
		receipt := Receipt{Node: item.Target}
		if receipt.Node.FileID == 0 {
			receipt.Node.FileID = item.Source.FileID
		}
		var applyErr error
		if item.Kind != "" {
			receipt, applyErr = store.ApplyPrimitive(ctx, Primitive{ID: item.ID, Kind: item.Kind, Source: item.Source, Target: item.Target})
		}
		item.Result, item.Outcome = receipt.Node, Succeeded
		if applyErr != nil {
			item.Outcome = Failed
			if receipt.PublicationPending {
				item.Outcome = PublicationPending
			}
			item.Error = fmt.Sprintf("%s %q to %q failed: %v", item.Kind, item.Source.Path, item.Target.Path, applyErr)
		}
		if err := e.db.WithContext(ctx).Save(&item).Error; err != nil {
			return err
		}
		if settled != nil {
			if err := settled(item); err != nil {
				return err
			}
		}
		return applyErr
	})
}

func (e *Engine) failFirst(ctx context.Context, rootID int64, cause error) error {
	var first step
	if err := e.db.WithContext(ctx).Where("root_id = ? AND check_only = ?", rootID, false).Order("id").First(&first).Error; err != nil {
		return err
	}
	first.Outcome, first.Error = Failed, cause.Error()
	if err := e.db.WithContext(ctx).Save(&first).Error; err != nil {
		return err
	}
	return cause
}

func (e *Engine) each(ctx context.Context, rootID int64, use func(step) error) error {
	var after int64
	for {
		var rows []step
		if err := e.db.WithContext(ctx).Where("root_id = ? AND id > ?", rootID, after).Order("id").Limit(PageSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, item := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := use(item); err != nil {
				return err
			}
			after = item.ID
		}
	}
}
