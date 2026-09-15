package entity

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

var (
	_ = sql.Scanner(&ArchiveJobSpec{})
	_ = driver.Valuer(&ArchiveJobSpec{})
	_ = sql.Scanner(&RestoreJobSpec{})
	_ = driver.Valuer(&RestoreJobSpec{})
	_ = sql.Scanner(&ScanJobSpec{})
	_ = driver.Valuer(&ScanJobSpec{})
	_ = sql.Scanner(&ArchiveManifestFile{})
	_ = driver.Valuer(&ArchiveManifestFile{})
	_ = sql.Scanner(&ArchiveCopyResult{})
	_ = driver.Valuer(&ArchiveCopyResult{})
)

func (x *ArchiveJobSpec) Scan(src any) error {
	return Scan(x, src)
}

func (x *ArchiveJobSpec) Value() (driver.Value, error) {
	return Value(x)
}

func (x *RestoreJobSpec) Scan(src any) error {
	return Scan(x, src)
}

func (x *RestoreJobSpec) Value() (driver.Value, error) {
	return Value(x)
}

func (x *ScanJobSpec) Scan(src any) error {
	return Scan(x, src)
}

func (x *ScanJobSpec) Value() (driver.Value, error) {
	return Value(x)
}

func (x *ArchiveManifestFile) Scan(src any) error {
	if err := Scan(x, src); err != nil {
		return fmt.Errorf("scan archive file failed, %w", err)
	}
	return x.Validate()
}

func (x *ArchiveManifestFile) Value() (driver.Value, error) {
	if err := x.Validate(); err != nil {
		return nil, err
	}
	return Value(x)
}

func (x *ArchiveManifestFile) Validate() error {
	if x == nil {
		return fmt.Errorf("archive file is nil")
	}
	if !filepath.IsAbs(x.SourcePath) || filepath.Clean(x.SourcePath) != x.SourcePath {
		return fmt.Errorf("invalid archive source path, path=%q", x.SourcePath)
	}
	return nil
}

func (x *ArchiveCopyResult) Scan(src any) error {
	if err := Scan(x, src); err != nil {
		return fmt.Errorf("scan archive copy result failed, %w", err)
	}
	return x.Validate()
}

func (x *ArchiveCopyResult) Value() (driver.Value, error) {
	if x == nil {
		return nil, nil
	}
	if err := x.Validate(); err != nil {
		return nil, err
	}
	return Value(x)
}

func (x *ArchiveCopyResult) Validate() error {
	if x == nil {
		return fmt.Errorf("archive copy result is nil")
	}
	if x.Size < 0 {
		return fmt.Errorf("invalid archive copy size, size=%d", x.Size)
	}
	if len(x.Sha256) != 32 {
		return fmt.Errorf("invalid archive copy SHA-256 length, length=%d", len(x.Sha256))
	}
	return nil
}

func ValidateRelativePath(value string) error {
	// Reject traversal components before any caller resolves a relative content path.
	if value == "" || value == "." {
		return fmt.Errorf("path is empty")
	}
	if strings.Contains(value, "\\") {
		return fmt.Errorf("path contains backslash, path=%q", value)
	}
	if value == ".." || strings.ContainsRune(value, 0) || path.IsAbs(value) || path.Clean(value) != value || strings.HasPrefix(value, "../") {
		return fmt.Errorf("path is not clean and relative, path=%q", value)
	}
	return nil
}
