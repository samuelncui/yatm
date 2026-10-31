package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
)

func (e *Executor) EnsureTapeWorkPath(ctx context.Context, jobID int64, barcode string) (string, error) {
	// Resolve both untrusted path components before creating Tape artifacts.
	dir, err := e.EnsureJobWorkPath(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("ensure job work path failed, job_id=%d, %w", jobID, err)
	}
	barcode, err = entity.NormalizeTapeBarcode(barcode)
	if err != nil {
		return "", err
	}

	// Keep every artifact for one physical Tape under the same directory.
	tapeDir := filepath.Join(dir, "tapes", barcode)
	if err := os.MkdirAll(tapeDir, 0o755); err != nil {
		return "", fmt.Errorf("create tape work directory failed, barcode=%q, %w", barcode, err)
	}
	return tapeDir, nil
}

func (e *Executor) WriteReport(ctx context.Context, jobID int64, barcode string, data []byte) error {
	// Write the derived summary beside the other artifacts for this Tape.
	tapeDir, err := e.EnsureTapeWorkPath(ctx, jobID, barcode)
	if err != nil {
		return err
	}
	target := filepath.Join(tapeDir, "yatm-report.json")
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("write report failed, %w", err)
	}
	return nil
}
