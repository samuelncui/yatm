package entity

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	_ = sql.Scanner(&ArchiveJobSpec{})
	_ = driver.Valuer(&ArchiveJobSpec{})
	_ = sql.Scanner(&RestoreJobSpec{})
	_ = driver.Valuer(&RestoreJobSpec{})
	_ = sql.Scanner(&ScanJobSpec{})
	_ = driver.Valuer(&ScanJobSpec{})
	_ = sql.Scanner(&CalibrationJobSpec{})
	_ = driver.Valuer(&CalibrationJobSpec{})
	_ = sql.Scanner(&TapeReadParameters{})
	_ = driver.Valuer(&TapeReadParameters{})
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

func (x *CalibrationJobSpec) Scan(src any) error {
	return Scan(x, src)
}

func (x *CalibrationJobSpec) Value() (driver.Value, error) {
	return Value(x)
}

func (x *TapeReadParameters) Scan(src any) error {
	return Scan(x, src)
}

func (x *TapeReadParameters) Value() (driver.Value, error) {
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
	if !utf8.ValidString(x.SourcePath) || strings.ContainsRune(x.SourcePath, 0) ||
		!filepath.IsAbs(x.SourcePath) || filepath.Clean(x.SourcePath) != x.SourcePath {
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
	if x.SizeBytes < 0 {
		return fmt.Errorf("invalid archive copy size, size=%d", x.SizeBytes)
	}
	if len(x.Sha256) != 32 {
		return fmt.Errorf("invalid archive copy SHA-256 length, length=%d", len(x.Sha256))
	}
	return nil
}

// ValidatePathComponent accepts literal UTF-8 filename text without separators or traversal.
// Arbitrary non-UTF-8 bytes cannot be represented by the current protocol and metadata formats.
func ValidatePathComponent(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("path component is not valid UTF-8, name=%q", value)
	}
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\x00") {
		return fmt.Errorf("invalid path component, name=%q", value)
	}
	if len(value) > MaxNameBytes {
		return fmt.Errorf("path component exceeds %d bytes, name=%q", MaxNameBytes, value)
	}
	return nil
}

// MaxPathDepth bounds the components of a relative path and the ancestry of a Library File.
const MaxPathDepth = 256

// MaxPathBytes and MaxNameBytes follow the common filesystem limits (PATH_MAX and NAME_MAX), so
// they reject no file that ZFS, ext4 or LTFS can hold. Every Catalog path stays within them.
const (
	MaxPathBytes = 4096
	MaxNameBytes = 255
)

// NormalizeRelativePath resolves empty, "." and ".." components within the selected root.
// A ".." above that root is rejected instead of clamped; other components keep their literal text.
func NormalizeRelativePath(value string) ([]string, error) {
	var parts []string
	for part := range strings.SplitSeq(value, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return nil, fmt.Errorf("path escapes the selected root, path=%q", value)
			}
			parts = parts[:len(parts)-1]
		default:
			if err := ValidatePathComponent(part); err != nil {
				return nil, fmt.Errorf("invalid relative path %q, %w", value, err)
			}
			parts = append(parts, part)
			if len(parts) > MaxPathDepth {
				return nil, fmt.Errorf("path exceeds %d directory levels, path=%q", MaxPathDepth, value)
			}
		}
	}
	if len(strings.Join(parts, "/")) > MaxPathBytes {
		return nil, fmt.Errorf("path exceeds %d bytes, path=%q", MaxPathBytes, value)
	}
	return parts, nil
}

// ValidateRelativePath requires slash-separated literal components, without cleaning their identity.
func ValidateRelativePath(value string) error {
	if len(value) > MaxPathBytes {
		return fmt.Errorf("path exceeds %d bytes, path=%q", MaxPathBytes, value)
	}
	for component := range strings.SplitSeq(value, "/") {
		if err := ValidatePathComponent(component); err != nil {
			return fmt.Errorf("invalid relative path %q, %w", value, err)
		}
	}
	return nil
}
