package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/internal/dataformat"
	legacy "github.com/samuelncui/yatm/migrate/legacy"
	"github.com/samuelncui/yatm/resource"
	"gorm.io/gorm"
)

type installationReport struct {
	Schema          legacy.Schema `json:"schema"`
	ServerURL       string    `json:"server_url"`
	BackupPaths     []string  `json:"backup_paths"`
	TapeScripts     []string  `json:"tape_scripts"`
	StandardScripts bool      `json:"standard_scripts"`
	Warnings        []string  `json:"warnings"`
}

func installedSchema(db *gorm.DB) (legacy.Schema, error) {
	if db == nil {
		return legacy.SchemaEmpty, nil
	}
	return legacy.DetectSchema(db)
}

func sqliteFile(dsn string) (string, url.Values, error) {
	// Inspection supports persistent SQLite files, never transient or remote stores.
	if dsn == "" || dsn == ":memory:" {
		return "", nil, errors.New("migration requires a persistent SQLite file")
	}
	path, query, _ := strings.Cut(dsn, "?")
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
		if strings.HasPrefix(path, "//") {
			return "", nil, errors.New("SQLite authority URLs require manual migration")
		}
		var err error
		path, err = url.PathUnescape(path)
		if err != nil {
			return "", nil, errors.New("invalid SQLite file URI")
		}
	}
	values, err := url.ParseQuery(query)
	if err != nil || path == "" || values.Get("mode") == "memory" {
		return "", nil, errors.New("migration requires a persistent SQLite file")
	}
	abs, err := filepath.Abs(path)
	return abs, values, err
}

func openMigrationDB(conf *config.Config, readOnly bool) (*gorm.DB, error) {
	// A read-only open must not create a missing database or accept an in-memory DSN.
	if conf.Database.Dialect != "sqlite" {
		if conf.Database.Dialect != "mysql" {
			return nil, errors.New("unsupported database dialect")
		}
		return resource.NewDBConn(conf.Database.Dialect, conf.Database.DSN)
	}
	path, values, err := sqliteFile(conf.Database.DSN)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if readOnly && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("migration database is unavailable: %w", err)
	}
	if !readOnly {
		return resource.NewDBConn("sqlite", conf.Database.DSN)
	}

	// mode=ro is understood by both supported SQLite drivers and preserves WAL visibility.
	values.Set("mode", "ro")
	values.Del("immutable")
	values.Del("_journal_mode")
	values.Del("_pragma")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
	return resource.NewDBConn("sqlite", dsn)
}

func inspectInstallation(ctx context.Context, db *gorm.DB, conf *config.Config, configPath, root string, stopped bool) (*installationReport, error) {
	// Report version and access information without changing service admission.
	report := &installationReport{BackupPaths: []string{}, TapeScripts: []string{}, Warnings: []string{}}
	var err error
	report.Schema, err = installedSchema(db)
	if err != nil {
		return report, err
	}
	report.ServerURL, err = localServerURL(conf.Listen)
	if err != nil {
		return report, err
	}
	if report.Schema == legacy.SchemaCurrent {
		if err := dataformat.CheckBundles(db, conf.Paths.Work); err != nil {
			return report, err
		}
	}
	if err := preflight(ctx, db, conf.Listen, stopped); err != nil {
		return report, err
	}

	// Automatic replacement is limited to installations with one complete local backup.
	if root != "" {
		if err := inspectBackupScope(conf, configPath, root, report); err != nil {
			return report, err
		}
	}
	if len(conf.TapeDevices) > 0 {
		report.Warnings = append(report.Warnings,
			"Tape scripts must store current and final LTFS indexes under TAPE_DIR; unmount must finish device release before returning. No Tape script was executed by this check.")
	}
	return report, nil
}

func inspectBackupScope(conf *config.Config, configPath, root string, report *installationReport) error {
	// External databases require an operator-managed coordinated backup.
	if conf.Database.Dialect != "sqlite" {
		return errors.New("automatic upgrade requires SQLite; back up the external database and use the manual migration guide")
	}
	dbPath, _, err := sqliteFile(conf.Database.DSN)
	if err != nil {
		return err
	}
	previewRoot := conf.Preview.Root
	if previewRoot == "" {
		previewRoot = "previews"
	}
	if !filepath.IsAbs(previewRoot) {
		previewRoot = filepath.Join(conf.Paths.Work, previewRoot)
	}
	paths := []string{configPath, dbPath, conf.Paths.Work, previewRoot, legacy.LegacyLTFSIndexRoot(conf.Scripts.Mount, conf.Paths.Work)}
	report.StandardScripts = true
	for index, script := range []string{conf.Scripts.Encrypt, conf.Scripts.Mkfs, conf.Scripts.Mount, conf.Scripts.Umount, conf.Scripts.ReadInfo} {
		name := []string{"encrypt", "mkfs", "mount", "umount", "readinfo"}[index]
		absScript, err := filepath.Abs(script)
		if err != nil || absScript != filepath.Join(root, "scripts", name) {
			report.StandardScripts = false
		}
		if script == "" {
			continue
		}
		report.TapeScripts = append(report.TapeScripts, script)
		paths = append(paths, script)
		if len(conf.TapeDevices) > 0 {
			info, err := os.Stat(script)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("configured Tape script must be an executable file: %s", script)
			}
		}
	}

	// Resolve existing ancestors as well as missing future directories without creating them.
	canonicalRoot, err := canonicalExisting(root)
	if err != nil {
		return err
	}
	for _, path := range paths {
		canonical, err := canonicalExisting(path)
		if err != nil {
			return err
		}
		if !withinRoot(canonicalRoot, canonical) {
			return fmt.Errorf("complete backup does not include required resource %s; use the manual upgrade guide", path)
		}
		report.BackupPaths = append(report.BackupPaths, canonical)
	}

	// cp -a preserves links, not their external contents; reject incomplete backup layouts.
	return filepath.WalkDir(canonicalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := canonicalExisting(path)
		if err != nil || !withinRoot(canonicalRoot, target) {
			return fmt.Errorf("installation contains an external or unresolved link %s; use a complete manual backup", path)
		}
		return nil
	})
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canonicalExisting(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return resolved, err
	}
	if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		return canonicalExisting(target)
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	resolved, err = canonicalExisting(parent)
	return filepath.Join(resolved, filepath.Base(abs)), err
}
