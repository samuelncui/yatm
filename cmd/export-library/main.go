package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/buildinfo"
	"github.com/samuelncui/yatm/internal/config"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
)

var (
	configOpt = flag.String("config", "./config.yaml", "config file path")
	typesOpt  = flag.String("types", "file,media,position", "types wants to be exported")
	outputOpt = flag.String("output", "stdout", "output file path, default use stdout")
)

func main() {
	// Inspect release identity without opening a Library or creating export/log files.
	if buildinfo.IsVersion(os.Args[1:]) {
		if err := buildinfo.Write(os.Stdout, "yatm-export-library"); err != nil {
			panic(err)
		}
		return
	}

	// Complete resource cleanup before reporting a command failure.
	flag.Parse()
	ctx, stop := exportContext()
	defer stop()
	if err := exportLibrary(ctx, *configOpt, *typesOpt, *outputOpt); err != nil {
		panic(err)
	}
}

func exportContext() (context.Context, context.CancelFunc) {
	// Release signal interception after cancellation even when a native stream cannot return for cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, func() {
		stop()
		// This executable owns termination; imported package subscriptions cannot swallow a second signal.
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	})
	return ctx, stop
}

func exportLibrary(ctx context.Context, configPath, typeNames, outputPath string) error {
	// Reject every unknown or empty selector before opening configuration, logs, database or output.
	parts := strings.Split(typeNames, ",")
	types := make([]entity.LibraryEntityType, 0, len(parts))
	for _, part := range parts {
		value, known := entity.LibraryEntityType_value["LIBRARY_ENTITY_TYPE_"+strings.ToUpper(strings.TrimSpace(part))]
		if !known || value == int32(entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_UNSPECIFIED) {
			return fmt.Errorf("invalid export type %q", part)
		}
		types = append(types, entity.LibraryEntityType(value))
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Keep stdout streaming; a named regular file becomes visible only after every resource settles.
	stream := func(output io.Writer) error { return streamLibrary(ctx, configPath, output, types) }
	if outputPath == "stdout" {
		return stream(os.Stdout)
	}
	return writeLibraryExport(ctx, outputPath, stream)
}

func streamLibrary(ctx context.Context, configPath string, output io.Writer, types []entity.LibraryEntityType) (returnErr error) {
	// Configure command logging before opening external resources.
	logWriter, err := rotatelogs.New(
		"./run.log.%Y%m%d%H%M",
		rotatelogs.WithLinkName("./run.log"),
		rotatelogs.WithMaxAge(time.Duration(86400)*time.Second),
		rotatelogs.WithRotationTime(time.Duration(604800)*time.Second),
	)
	if err != nil {
		return err
	}
	defer func() {
		if err := logWriter.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close export log failed, %w", err))
		}
	}()
	logrus.AddHook(lfshook.NewHook(
		lfshook.WriterMap{
			logrus.InfoLevel:  logWriter,
			logrus.ErrorLevel: logWriter,
		},
		&logrus.TextFormatter{},
	))

	// Open the configured Library database.
	conf, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logrus.Info("configuration loaded")
	db, err := resource.NewDBConn(conf.Database.Dialect, conf.Database.DSN)
	if err != nil {
		return err
	}
	defer func() {
		sqlDB, err := db.DB()
		if err == nil {
			err = sqlDB.Close()
		}
		if err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close export database failed, %w", err))
		}
	}()
	empty, err := dataformat.CheckCatalog(db)
	if err != nil {
		return err
	}
	if !empty {
		if err := dataformat.CheckBundles(db, conf.Paths.Work); err != nil {
			return err
		}
	}

	// Initialize only the validated release format before streaming the export.
	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		return err
	}

	// Stream the JSON Lines snapshot to its destination.
	if err := lib.Export(ctx, output, types); err != nil {
		return fmt.Errorf("export library failed, %w", err)
	}
	return nil
}

func writeLibraryExport(ctx context.Context, output string, stream func(io.Writer) error) (returnErr error) {
	// Follow final symlinks as os.Create did, including a dangling link to a new regular file.
	target, err := exportTarget(output)
	if err != nil {
		return fmt.Errorf("resolve export output failed, path=%q, %w", output, err)
	}
	info, err := os.Stat(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect export output failed, path=%q, %w", output, err)
	}
	if info != nil && !info.Mode().IsRegular() {
		// Explicit devices and pipes retain streaming semantics; never replace their filesystem nodes.
		file, err := os.OpenFile(target, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open export stream failed, path=%q, %w", output, err)
		}
		streamErr := stream(file)
		closeErr := file.Close()
		return errors.Join(streamErr, closeErr)
	}

	// Allocate one same-directory file with the original creation mode and process umask.
	id, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("allocate export output name failed, %w", err)
	}
	temporary := filepath.Join(filepath.Dir(target), ".yatm-export-"+id.String()+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return fmt.Errorf("create temporary export output failed, path=%q, %w", output, err)
	}
	closed := false
	defer func() {
		if !closed {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary export output failed, path=%q, %w", temporary, err))
		}
	}()
	if info != nil {
		if err := file.Chmod(info.Mode().Perm()); err != nil {
			return fmt.Errorf("retain export output permissions failed, path=%q, %w", output, err)
		}
	}

	// A failed stream, cancellation, sync or close cannot publish a partial replacement.
	streamErr := stream(file)
	var syncErr error
	if streamErr == nil && ctx.Err() == nil {
		if err := file.Sync(); err != nil {
			syncErr = fmt.Errorf("sync export output failed, %w", err)
		}
	}
	closeErr := file.Close()
	closed = true
	if closeErr != nil {
		closeErr = fmt.Errorf("close export output failed, %w", closeErr)
	}
	if err := errors.Join(streamErr, ctx.Err(), syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("publish export output failed, path=%q, %w", output, err)
	}
	return nil
}

func exportTarget(path string) (string, error) {
	for {
		// Stdlib resolves existing links and rejects loops; only a missing final target needs another step.
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		// Resolve the literal parent before joining or staging; a symlink followed by .. is not lexical.
		directory, name := filepath.Split(path)
		if directory == "" {
			directory = "."
		}
		directory, err = filepath.EvalSymlinks(directory)
		if err != nil {
			return "", err
		}
		target := filepath.Join(directory, name)
		link, err := os.Readlink(target)
		if errors.Is(err, os.ErrNotExist) {
			return target, nil
		}
		if err != nil {
			return "", err
		}

		// Keep relative link text intact until stdlib resolution validates every parent component.
		if !filepath.IsAbs(link) {
			link = directory + string(os.PathSeparator) + link
		}
		path = link
	}
}
