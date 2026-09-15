package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/migrate/legacy"
)

func migrationBackup(root, backupRoot, configPath string) (legacy.Backup, error) {
	// Resolve the preserved configuration by installation-relative identity, never its active DSN.
	if backupRoot == "" {
		return legacy.Backup{}, fmt.Errorf("this phase requires -backup-root with a complete legacy installation backup")
	}
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return legacy.Backup{}, err
	}
	backupRoot, err = filepath.Abs(backupRoot)
	if err != nil {
		return legacy.Backup{}, err
	}
	if root == backupRoot {
		return legacy.Backup{}, fmt.Errorf("backup root must not be the active installation")
	}
	canonicalBackup, err := canonicalExisting(backupRoot)
	if err != nil {
		return legacy.Backup{}, err
	}
	mapPath := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if !withinRoot(root, path) {
			return "", fmt.Errorf("legacy resource is outside the complete backup: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		mapped := filepath.Join(backupRoot, rel)
		resolved, err := canonicalExisting(mapped)
		if err != nil {
			return "", err
		}
		if !withinRoot(canonicalBackup, resolved) {
			return "", fmt.Errorf("backup resource link escapes the preserved installation: %s", mapped)
		}
		return mapped, nil
	}
	storedConfig, err := mapPath(configPath)
	if err != nil {
		return legacy.Backup{}, err
	}
	conf, err := config.Load(storedConfig)
	if err != nil {
		return legacy.Backup{}, fmt.Errorf("load backup configuration failed, %w", err)
	}
	if conf.Database.Dialect != "sqlite" {
		return legacy.Backup{}, fmt.Errorf("backup validation requires a SQLite installation")
	}

	// Map data evidence into the backup while retaining the original runtime output path.
	dbPath, _, err := sqliteFileAt(conf.Database.DSN, root)
	if err != nil {
		return legacy.Backup{}, err
	}
	catalog, err := mapPath(dbPath)
	if err != nil {
		return legacy.Backup{}, err
	}
	work, err := mapPath(conf.Paths.Work)
	if err != nil {
		return legacy.Backup{}, err
	}
	mount := ""
	if conf.Scripts.Mount != "" {
		mount, err = mapPath(conf.Scripts.Mount)
		if err != nil {
			return legacy.Backup{}, err
		}
	}
	index, err := mapPath(legacy.LegacyLTFSIndexRoot(mount, conf.Paths.Work))
	if err != nil {
		return legacy.Backup{}, err
	}
	var target string
	if conf.Paths.Target != "" {
		target = conf.Paths.Target
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		target, err = executor.CanonicalConfiguredPath(target)
		if err != nil {
			return legacy.Backup{}, err
		}
	}
	if info, err := os.Lstat(backupRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return legacy.Backup{}, fmt.Errorf("backup root must be an existing real directory")
	}
	return legacy.Backup{Root: backupRoot, CatalogPath: catalog, WorkRoot: work, IndexRoot: index, RestoreRoot: target}, nil
}
