package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
	typesOpt  = flag.String("types", "file,tape,position", "types wants to be exported")
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
	if err := exportLibrary(context.Background(), *configOpt, *typesOpt, *outputOpt); err != nil {
		panic(err)
	}
}

func exportLibrary(ctx context.Context, configPath, typeNames, outputPath string) (returnErr error) {
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

	// Parse and validate the selected Library entity types.
	parts := strings.Split(typeNames, ",")
	toEnum := entity.ToEnum(entity.LibraryEntityType_value, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_UNSPECIFIED)
	types := make([]entity.LibraryEntityType, 0, len(parts))
	for _, part := range parts {
		e := toEnum("LIBRARY_ENTITY_TYPE_" + strings.ToUpper(strings.TrimSpace(part)))
		if e == entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_UNSPECIFIED {
			continue
		}
		types = append(types, e)
	}
	if len(types) == 0 {
		return fmt.Errorf("no export types found; use the types option to specify at least one type")
	}

	// Select stdout or an explicitly requested output file without buffering the export.
	var output io.Writer = os.Stdout
	if outputPath != "stdout" {
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("open output file failed, path=%q, %w", outputPath, err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close export output failed, path=%q, %w", outputPath, err))
			}
		}()
		output = file
	}

	// Stream the JSON Lines snapshot to its destination.
	if err := lib.Export(ctx, output, types); err != nil {
		return fmt.Errorf("export library failed, %w", err)
	}
	return nil
}
