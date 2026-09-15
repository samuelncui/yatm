package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/buildinfo"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
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

	// Configure command logging before opening external resources.
	ctx := context.Background()
	logWriter, err := rotatelogs.New(
		"./run.log.%Y%m%d%H%M",
		rotatelogs.WithLinkName("./run.log"),
		rotatelogs.WithMaxAge(time.Duration(86400)*time.Second),
		rotatelogs.WithRotationTime(time.Duration(604800)*time.Second),
	)
	if err != nil {
		panic(err)
	}
	logrus.AddHook(lfshook.NewHook(
		lfshook.WriterMap{
			logrus.InfoLevel:  logWriter,
			logrus.ErrorLevel: logWriter,
		},
		&logrus.TextFormatter{},
	))

	// Open the configured Library database.
	flag.Parse()
	conf := config.GetConfig(*configOpt)
	db, err := resource.NewDBConn(conf.Database.Dialect, conf.Database.DSN)
	if err != nil {
		panic(err)
	}
	empty, err := dataformat.CheckCatalog(db)
	if err != nil {
		panic(err)
	}
	if !empty {
		if err := dataformat.CheckBundles(db, conf.Paths.Work); err != nil {
			panic(err)
		}
	}

	// Initialize only the validated release format before streaming the export.
	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		panic(err)
	}

	// Parse and validate the selected Library entity types.
	parts := strings.Split(*typesOpt, ",")
	toEnum := entity.ToEnum(entity.LibraryEntityType_value, entity.LibraryEntityType_NONE)
	types := make([]entity.LibraryEntityType, 0, len(parts))
	for _, part := range parts {
		e := toEnum(strings.ToUpper(strings.TrimSpace(part)))
		if e == entity.LibraryEntityType_NONE {
			continue
		}

		types = append(types, e)
	}
	if len(types) == 0 {
		panic(fmt.Errorf("no export types found; use the types option to specify at least one type"))
	}

	// Select stdout or an explicitly requested output file without buffering the export.
	var output io.Writer = os.Stdout
	if *outputOpt != "stdout" {
		file, err := os.Create(*outputOpt)
		if err != nil {
			panic(fmt.Errorf("open output file failed, path=%q, %w", *outputOpt, err))
		}
		defer file.Close()
		output = file
	}

	// Stream the JSON Lines snapshot to its destination.
	if err := lib.Export(ctx, output, types); err != nil {
		panic(fmt.Errorf("export library failed, %w", err))
	}
}
