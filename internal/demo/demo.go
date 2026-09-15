// Package demo builds the disposable local environment used for UI and workflow review.
package demo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"gopkg.in/yaml.v2"
)

const (
	TapeBarcode  = "RVW001"
	defaultPerm  = 0o755
	databaseName = "yatm.db"
)

// Options controls one isolated Demo environment.
type Options struct {
	Root      string
	Listen    string
	Reset     bool
	VideoPath string
}

// Prepare creates or reuses a complete Demo environment.
func Prepare(ctx context.Context, options Options) error {
	// Resolve the disposable root before any destructive reset.
	root, err := validateRoot(options.Root)
	if err != nil {
		return err
	}
	listen, err := validateListen(options.Listen)
	if err != nil {
		return err
	}
	videoPath, err := validateVideoPath(options.VideoPath)
	if err != nil {
		return err
	}
	if options.Reset && videoPath != "" && pathWithin(videoPath, root) {
		return fmt.Errorf("Demo video cannot be inside a root being reset, path=%q", videoPath)
	}
	if options.Reset {
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("reset Demo root failed, root=%q, %w", root, err)
		}
	}

	// Reuse an existing fixture without discarding reviewer mutations.
	databasePath := filepath.Join(root, databaseName)
	if _, err := os.Stat(databasePath); err == nil {
		if videoPath != "" {
			return fmt.Errorf("Demo video override requires reset, path=%q", videoPath)
		}
		if err := writeRuntimeFiles(root, listen); err != nil {
			return err
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Demo database failed, path=%q, %w", databasePath, err)
	}
	if err := requireEmptyRoot(root); err != nil {
		return err
	}

	// Seed physical Media, Library metadata, and operable Job bundles.
	paths, err := createLayout(root)
	if err != nil {
		return err
	}
	db, err := resource.OpenSQLite(databasePath)
	if err != nil {
		return fmt.Errorf("open Demo database failed, %w", err)
	}
	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		return fmt.Errorf("migrate Demo Library failed, %w", err)
	}
	previews, err := newPreviewManager(root, paths.Work, videoPath != "")
	if err != nil {
		return fmt.Errorf("create Demo Preview manager failed, %w", err)
	}
	exe := executor.New(db, lib, []string{"/dev/" + TapeBarcode}, paths, executor.Scripts{}, previews)
	if err := exe.AutoMigrate(); err != nil {
		return fmt.Errorf("migrate Demo Executor failed, %w", err)
	}
	if err := exe.ReconcileStorage(ctx); err != nil {
		return fmt.Errorf("prepare Demo Job storage failed, %w", err)
	}
	if err := seed(ctx, lib, exe, paths, root, videoPath); err != nil {
		return err
	}
	if err := seedVersionDates(ctx, lib, db); err != nil {
		return fmt.Errorf("date Demo version history failed, %w", err)
	}

	// Verify the complete fixture also passes the server's next-start bundle validation.
	if err := exe.ReconcileStorage(ctx); err != nil {
		return fmt.Errorf("validate completed Demo Job bundles failed, %w", err)
	}
	if err := writeRuntimeFiles(root, listen); err != nil {
		return err
	}
	return nil
}

func validateVideoPath(value string) (string, error) {
	requested := strings.TrimSpace(value)
	if requested == "" {
		return "", nil
	}
	path, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve Demo video failed, path=%q, %w", requested, err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Demo video symlinks failed, path=%q, %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat Demo video failed, path=%q, %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Demo video must be a regular file, path=%q", path)
	}
	if !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return "", fmt.Errorf("Demo video must use the .mp4 extension, path=%q", path)
	}
	return path, nil
}

func validateRoot(value string) (string, error) {
	requested, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("resolve Demo root failed, %w", err)
	}
	if !strings.HasPrefix(filepath.Base(requested), "yatm-demo") {
		return "", fmt.Errorf("Demo root name must start with yatm-demo, root=%q", requested)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(requested))
	if err != nil {
		return "", fmt.Errorf("resolve Demo root parent failed, root=%q, %w", requested, err)
	}
	root := filepath.Join(parent, filepath.Base(requested))
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("Demo root cannot be a symlink, root=%q", root)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Demo root failed, root=%q, %w", root, err)
	}
	for _, base := range []string{os.TempDir(), "/tmp", "/private/tmp"} {
		canonical, err := filepath.EvalSymlinks(base)
		if err == nil && pathWithin(root, canonical) {
			return root, nil
		}
	}
	return "", fmt.Errorf("Demo root must be inside a temporary directory, root=%q", root)
}

func validateListen(value string) (string, error) {
	listen := strings.TrimSpace(value)
	host, portValue, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse Demo listen address failed, address=%q, %w", listen, err)
	}
	if host != "localhost" {
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			return "", fmt.Errorf("Demo listen address must use loopback, address=%q", listen)
		}
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("Demo listen port is invalid, address=%q", listen)
	}
	return listen, nil
}

func pathWithin(target, base string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), target)
	if err != nil {
		return false
	}
	return relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func requireEmptyRoot(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, defaultPerm); err != nil {
			return fmt.Errorf("create Demo root failed, root=%q, %w", root, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Demo root failed, root=%q, %w", root, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("Demo root is not empty and has no database, root=%q", root)
	}
	return nil
}

func createLayout(root string) (executor.Paths, error) {
	paths := executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "target"), Volumes: []string{filepath.Join(root, "volumes")},
	}
	paths.Access = []executor.AccessRange{{Root: paths.Source}, {Root: paths.Target}}
	for _, directory := range []string{
		paths.Work, paths.Source, paths.Target, paths.Volumes[0], filepath.Join(root, "offline-shelf"),
	} {
		if err := os.MkdirAll(directory, defaultPerm); err != nil {
			return executor.Paths{}, fmt.Errorf("create Demo directory failed, path=%q, %w", directory, err)
		}
	}
	return paths, nil
}

func writeRuntimeFiles(root, listen string) error {
	// Encode the normal server configuration with only one mock Tape probe.
	conf := new(config.Config)
	conf.Domain = "http://" + listen
	conf.Listen = listen
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, databaseName)
	conf.Paths = executor.Paths{
		Work: filepath.Join(root, "work"), Volumes: []string{filepath.Join(root, "volumes")},
		Access: []executor.AccessRange{{Root: filepath.Join(root, "source")}, {Root: filepath.Join(root, "target")}},
	}
	conf.TapeDevices = []string{"/dev/" + TapeBarcode}
	conf.Scripts.ReadInfo = filepath.Join(root, "read-info.sh")
	conf.Preview.Root = filepath.Join(root, "previews")
	data, err := yaml.Marshal(conf)
	if err != nil {
		return fmt.Errorf("encode Demo config failed, %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), data, 0o644); err != nil {
		return fmt.Errorf("write Demo config failed, %w", err)
	}

	// Keep Tape inspection functional without pretending to emulate LTFS I/O.
	probe := "#!/usr/bin/env bash\nset -euo pipefail\n\nbarcode=${DEVICE##*/}\nprintf '{\"barcode\":\"%s\"}\\n' \"${barcode}\" > \"${OUT}\"\n"
	if err := os.WriteFile(filepath.Join(root, "read-info.sh"), []byte(probe), 0o755); err != nil {
		return fmt.Errorf("write Demo Tape probe failed, %w", err)
	}
	return nil
}
