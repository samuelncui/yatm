package external

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"github.com/abc950309/acp"
	"github.com/abc950309/tapewriter/library"
)

func (e *External) ImportACPReport(ctx context.Context, barname, name, encryption string, reader io.Reader) error {
	report := new(acp.Report)
	if err := json.NewDecoder(reader).Decode(report); err != nil {
		return err
	}

	files := make([]*library.TapeFile, 0, 16)
	for _, f := range report.Jobs {
		if len(f.SuccessTargets) == 0 {
			continue
		}
		if !f.Mode.IsRegular() {
			continue
		}

		hash, err := hex.DecodeString(f.SHA256)
		if err != nil {
			return fmt.Errorf("decode sha256 fail, err= %w", err)
		}

		files = append(files, &library.TapeFile{
			Path:      path.Join(f.Path...),
			Size:      f.Size,
			Mode:      f.Mode,
			ModTime:   f.ModTime,
			WriteTime: f.WriteTime,
			Hash:      hash,
		})
	}

	if len(files) == 0 {
		return fmt.Errorf("cannot found files from report")
	}

	if _, err := e.lib.CreateTape(ctx, &library.Tape{
		Barcode:    barname,
		Name:       name,
		Encryption: encryption,
		CreateTime: files[0].WriteTime,
	}, files); err != nil {
		return fmt.Errorf("save tape, err= %w", err)
	}

	if err := e.lib.TrimFiles(ctx); err != nil {
		return err
	}

	return nil
}
