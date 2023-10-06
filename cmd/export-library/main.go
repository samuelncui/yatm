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

	flag.Parse()
	conf := config.GetConfig(*configOpt)

	db, err := resource.NewDBConn(conf.Database.Dialect, conf.Database.DSN)
	if err != nil {
		panic(err)
	}

	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		panic(err)
	}

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
		panic(fmt.Errorf("cannot found types, use 'types' option to specify at least one type"))
	}

	jsonBuf, err := lib.Export(ctx, types)
	if err != nil {
		panic(fmt.Errorf("export json fail, %w", err))
	}

	f := func() io.WriteCloser {
		if *outputOpt == "stdout" {
			return os.Stdout
		}

		f, err := os.Create(*outputOpt)
		if err != nil {
			panic(fmt.Errorf("open output file fail, path= '%s', %w", *outputOpt, err))
		}
		return f
	}()

	defer f.Close()
	if _, err := f.Write(jsonBuf); err != nil {
		panic(fmt.Errorf("write output file fail, %w", err))
	}
}
