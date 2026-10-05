package scan

import (
	"bytes"
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

func (r *runner) previewWorkload(ctx context.Context, config *Config, scope *Scope) (int64, error) {
	// Stream content groups, including prior results, without retaining a file-sized set or issuing row lookups.
	query := r.db.WithContext(ctx).Model(&Entry{}).
		Select("id", "location_id", "scope_path", "source_path", "sha256", "size", "change", "finding").
		Where(r.scopeQuery(ctx, scope)).
		Order("sha256, size, id")
	rows, err := query.Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	// Only one content group's classification is retained; the store answers once per group.
	var hash []byte
	var size, total int64
	var candidate bool
	finish := func() error {
		if !candidate || config.Spec.PreviewPolicy == entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL {
			if candidate {
				total++
			}
			return nil
		}
		signature, err := library.NewFileSignature(hash, size)
		if err != nil {
			return err
		}
		exists, err := r.exe.Previews().Exists(signature)
		if err != nil {
			return err
		}
		if !exists {
			total++
		}
		return nil
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var row Entry
		if err := r.db.ScanRows(rows, &row); err != nil {
			return 0, err
		}
		if len(row.SHA256) != 32 {
			continue
		}
		if !bytes.Equal(hash, row.SHA256) || size != row.Size {
			if err := finish(); err != nil {
				return 0, err
			}
			hash, size = append(hash[:0], row.SHA256...), row.Size
			candidate = false
		}
		if row.LocationID != scope.LocationID || (scope.LocationID > 0 && scope.ID > 0 && row.ScopePath != scope.Path) {
			continue
		}
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			continue
		}
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES && row.Finding != entity.ScanFinding_SCAN_FINDING_MATCH {
			continue
		}
		if !candidate && r.exe.Previews().Supports(row.SourcePath, config.PreviewJobSettings) {
			candidate = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := finish(); err != nil {
		return 0, err
	}
	return total, nil
}
