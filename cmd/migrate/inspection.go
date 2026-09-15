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
	ServerURL       string        `json:"server_url"`
	BackupPaths     []string      `json:"backup_paths"`
	TapeScripts     []string      `json:"tape_scripts"`
	Warnings        []string      `json:"warnings"`
	MigrationReport string        `json:"migration_report"`
}

func installedSchema(db *gorm.DB) (legacy.Schema, error) {
	if db == nil {
		return legacy.SchemaEmpty, nil
	}
	return legacy.DetectSchema(db)
}

func inspectFreshInstallation(conf *config.Config, root string) (*installationReport, error) {
	// Resolve the future installation paths without opening or creating service resources.
	report := &installationReport{Schema: legacy.SchemaEmpty, BackupPaths: []string{}, TapeScripts: []string{}, Warnings: []string{}}
	root, err := filepath.Abs(root)
	if err != nil {
		return report, err
	}
	if err := inspectUpgradeDirectory(filepath.Join(root, ".yatm-upgrades")); err != nil {
		return report, err
	}
	if conf.Database.Dialect != "sqlite" {
		return report, fmt.Errorf("automatic installation requires SQLite")
	}
	dbPath, _, err := sqliteFileAt(conf.Database.DSN, root)
	if err != nil {
		return report, err
	}
	if _, err := os.Lstat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		return report, fmt.Errorf("new installation database must not already exist: %s", dbPath)
	}
	work := conf.Paths.Work
	if !filepath.IsAbs(work) {
		work = filepath.Join(root, work)
	}
	previewRoot := conf.Preview.Root
	if previewRoot == "" {
		previewRoot = "previews"
	}
	if !filepath.IsAbs(previewRoot) {
		previewRoot = filepath.Join(work, previewRoot)
	}
	for _, path := range []string{dbPath, work, previewRoot} {
		if !withinRoot(root, path) {
			return report, fmt.Errorf("new installation resource must fit inside the installation root: %s", path)
		}
	}

	// Report the endpoint and manual device configuration separately from service readiness.
	report.ServerURL, err = localServerURL(conf.Listen)
	if err != nil {
		return report, err
	}
	report.MigrationReport = filepath.Join(work, "migration-report.json")
	if len(conf.TapeDevices) > 0 {
		report.Warnings = append(report.Warnings, "Review configured Tape devices and scripts before first use. Installation does not execute Tape scripts or validate physical Tape readiness.")
	}
	return report, nil
}

func sqliteFile(dsn string) (string, url.Values, error) {
	return sqliteFileAt(dsn, ".")
}

func sqliteFileAt(dsn, root string) (string, url.Values, error) {
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
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
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
	report.MigrationReport, _ = filepath.Abs(filepath.Join(conf.Paths.Work, "migration-report.json"))
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
		if db != nil && db.Migrator().HasTable("locations") {
			var after int64
			for {
				var rows []struct {
					ID       int64
					RootPath string
				}
				if err := db.WithContext(ctx).Table("locations").Select("id", "root_path").
					Where("id > ?", after).Order("id").Limit(256).Find(&rows).Error; err != nil {
					return report, err
				}
				if len(rows) == 0 {
					break
				}
				for _, row := range rows {
					if err := inspectBusinessPath(root, row.RootPath); err != nil {
						return report, err
					}
					after = row.ID
				}
			}
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
	for _, script := range []string{conf.Scripts.Encrypt, conf.Scripts.Mkfs, conf.Scripts.Mount, conf.Scripts.Umount, conf.Scripts.ReadInfo} {
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
	for _, business := range append([]string{conf.Paths.Source, conf.Paths.Target}, conf.Paths.Volumes...) {
		if err := inspectBusinessPath(canonicalRoot, business); err != nil {
			return err
		}
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
		if path == filepath.Join(canonicalRoot, ".yatm-upgrades") {
			if err := inspectUpgradeDirectory(path); err != nil {
				return err
			}
			return filepath.SkipDir
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

func inspectUpgradeDirectory(path string) error {
	// Fresh and existing installations reserve the same installer-owned backup directory.
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("reserved upgrade directory is not an owned directory: %s", path)
	}
	marker := filepath.Join(path, "OWNER")
	info, err = os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("reserved upgrade directory has no valid ownership marker: %s", path)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != "yatm-installer-upgrades" {
		return fmt.Errorf("reserved upgrade directory belongs to another owner: %s", path)
	}
	return nil
}

func inspectBusinessPath(root, path string) error {
	// Registered originals, restore output and Volume roots remain outside software replacement.
	if path == "" {
		return nil
	}
	canonicalRoot, err := canonicalExisting(root)
	if err != nil {
		return err
	}
	canonical, err := canonicalExisting(path)
	if err != nil {
		return err
	}
	if !withinRoot(canonicalRoot, canonical) {
		return nil
	}
	entries, err := os.ReadDir(canonical)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || len(entries) > 0 {
		return fmt.Errorf("business files are mixed into the installation at %s; use a reviewed manual backup and upgrade", path)
	}
	return nil
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
