package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/samuelncui/tapemanager/apis"
	"github.com/samuelncui/tapemanager/entity"
	"github.com/samuelncui/tapemanager/executor"
	"github.com/samuelncui/tapemanager/library"
	"github.com/samuelncui/tapemanager/resource"
	"github.com/samuelncui/tapemanager/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v2"
)

type config struct {
	Domain      string `yaml:"domain"`
	Listen      string `yaml:"listen"`
	DebugListen string `yaml:"debug_listen"`

	Database struct {
		Dialect string `yaml:"dialect"`
		DSN     string `yaml:"dsn"`
	} `yaml:"database"`

	Paths       executor.Paths   `yaml:"paths"`
	TapeDevices []string         `yaml:"tape_devices"`
	Scripts     executor.Scripts `yaml:"scripts"`
}

var (
	configPath = flag.String("config", "./config.yaml", "config file path")
)

func main() {
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
	cf, err := os.Open(*configPath)
	if err != nil {
		panic(err)
	}

	conf := new(config)
	if err := yaml.NewDecoder(cf).Decode(conf); err != nil {
		panic(err)
	}
	logrus.Infof("read config success, conf= '%+v'", conf)

	if conf.DebugListen != "" {
		go tools.Wrap(context.Background(), func() { tools.NewDebugServer(conf.DebugListen) })
	}

	db, err := resource.NewDBConn(conf.Database.Dialect, conf.Database.DSN)
	if err != nil {
		panic(err)
	}

	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		panic(err)
	}

	exe := executor.New(db, lib, conf.TapeDevices, conf.Paths, conf.Scripts)
	if err := exe.AutoMigrate(); err != nil {
		panic(err)
	}

	grpcPanicRecoveryHandler := func(p any) (err error) {
		logrus.Infof("recovered from panic, %v, stack= %s", p, debug.Stack())
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
	api := apis.New(conf.Paths.Source, lib, exe)
	entity.RegisterServiceServer(s, api)

	mux := http.NewServeMux()

	grpcWebServer := grpcweb.WrapServer(s, grpcweb.WithOriginFunc(func(origin string) bool { return true }))
	mux.Handle("/services/", http.StripPrefix("/services/", grpcWebServer))
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

	go func() {
		<-tools.ShutdownContext.Done()
		logrus.Infof("Graceful shutdown, wait for working process")
		start := time.Now()
		tools.Wait()
		logrus.Infof("Graceful shutdown, wait done, duration= %s", time.Since(start))
		srv.Shutdown(context.Background())
	}()

	log.Printf("http server listening at %v", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
