package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	_ "github.com/samuelncui/yatm/executor/archive"
	_ "github.com/samuelncui/yatm/executor/restore"
	_ "github.com/samuelncui/yatm/executor/scan"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/internal/buildinfo"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	configOpt = flag.String("config", "./config.yaml", "config file path")
)

func main() {
	// Offline identity checks must not create logs, load configuration or open databases.
	if buildinfo.IsVersion(os.Args[1:]) {
		if err := buildinfo.Write(os.Stdout, "yatm-httpd"); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Initialize process logging before opening application resources.
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
			logrus.PanicLevel: logWriter,
			logrus.FatalLevel: logWriter,
			logrus.ErrorLevel: logWriter,
			logrus.WarnLevel:  logWriter,
			logrus.InfoLevel:  logWriter,
		},
		&logrus.TextFormatter{},
	))

	flag.Parse()
	conf := config.GetConfig(*configOpt)

	if conf.DebugListen != "" {
		go tools.Wrap(context.Background(), func() { tools.NewDebugServer(conf.DebugListen) })
	}

	// Reject unsupported catalog and Job formats before any automatic schema writes.
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

	// Initialize the validated catalog and configured Preview generators.
	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		panic(err)
	}

	previews, err := preview.New(conf.Preview, conf.Paths.Work)
	if err != nil {
		panic(err)
	}
	// Exclude actual process-owned resources without treating the whole work directory as disposable.
	exe := executor.New(db, lib, conf.TapeDevices, conf.Paths, conf.Scripts, previews)
	runtimePaths := []string{"./run.log", "./.yatm-upgrades", *configOpt}
	if conf.Database.Dialect == "sqlite" {
		filename, _, _ := strings.Cut(strings.TrimPrefix(conf.Database.DSN, "file:"), "?")
		if filename != "" && filename != ":memory:" {
			runtimePaths = append(runtimePaths, filename, filename+"-wal", filename+"-shm", filename+"-journal")
		}
	}
	exe.SetOnlineRuntimePaths(runtimePaths...)
	exe.SetOnlineRuntimePathProvider(func() []string { return []string{logWriter.CurrentFileName()} })
	if err := exe.AutoMigrate(); err != nil {
		panic(err)
	}
	if err := exe.InitializeLocations(context.Background()); err != nil {
		panic(err)
	}
	if err := exe.ReconcileStorage(context.Background()); err != nil {
		panic(err)
	}

	// Register typed handlers and recover request panics without terminating the service.
	grpcPanicRecoveryHandler := func(p any) (err error) {
		logrus.Errorf("recovered from panic, %v, stack= %s", p, debug.Stack())
		return status.Errorf(codes.Internal, "%s", p)
	}
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			recovery.UnaryServerInterceptor(recovery.WithRecoveryHandler(grpcPanicRecoveryHandler)),
		),
		grpc.ChainStreamInterceptor(
			recovery.StreamServerInterceptor(recovery.WithRecoveryHandler(grpcPanicRecoveryHandler)),
		),
	)
	api := apis.New(lib, exe)
	entity.RegisterServiceServer(s, api)
	entity.RegisterJobServiceServer(s, api)
	api.RegisterLocations(s)
	exe.RegisterJobServices(s)

	// Serve the control interface and release frontend from one listener.
	mux := http.NewServeMux()
	grpcWebServer := grpcweb.WrapServer(s, grpcweb.WithOriginFunc(func(origin string) bool { return true }))
	// Keep the leading slash required by the gRPC method path.
	mux.Handle("/services/", http.StripPrefix("/services", grpcWebServer))
	mux.Handle("/files/", http.StripPrefix("/files", api.Uploader()))

	fs := http.FileServer(http.Dir("./frontend/assets"))
	mux.Handle("/assets/", http.StripPrefix("/assets/", fs))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		indexBuf, err := os.ReadFile("./frontend/index.html")
		if err != nil {
			panic(err)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(bytes.ReplaceAll(indexBuf, []byte("%%API_BASE%%"), []byte(fmt.Sprintf("%s/services", conf.Domain))))
	})

	srv := &http.Server{
		Handler: mux,
		Addr:    conf.Listen,
	}

	// Drain active operations before stopping the HTTP listener.
	go func() {
		<-tools.ShutdownContext.Done()
		logrus.Infof("Graceful shutdown, wait for working process")
		start := time.Now()
		tools.Wait()
		logrus.Infof("Graceful shutdown, wait done, duration= %s", time.Since(start))
		srv.Shutdown(context.Background())
	}()

	// Start the configured listener after initialization has completed.
	log.Printf("http server listening at %v", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("failed to serve: %v", err)
	}
}
