package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/abc950309/tapewriter/apis"
	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/executor"
	"github.com/abc950309/tapewriter/library"
	"github.com/abc950309/tapewriter/resource"
	"github.com/abc950309/tapewriter/tools"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v2"
)

type config struct {
	Domain        string `yaml:"domain"`
	Listen        string `yaml:"listen"`
	DebugListen   string `yaml:"debug_listen"`
	WorkDirectory string `yaml:"work_directory"`

	Database struct {
		Dialect string `yaml:"dialect"`
		DSN     string `yaml:"dsn"`
	} `yaml:"database"`

	TapeDevices    []string `yaml:"tape_devices"`
	FilesystemRoot string   `yaml:"filesystem_root"`

	Scripts struct {
		Encrypt string `yaml:"encrypt"`
		Mkfs    string `yaml:"mkfs"`
		Mount   string `yaml:"mount"`
		Umount  string `yaml:"umount"`
	} `yaml:"scripts"`
}

var (
	configPath = flag.String("config", "./config.yaml", "config file path")
)

func main() {
	flag.Parse()

	cf, err := os.Open(*configPath)
	if err != nil {
		panic(err)
	}

	conf := new(config)
	if err := yaml.NewDecoder(cf).Decode(conf); err != nil {
		panic(err)
	}

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

	exe := executor.New(
		db, lib, conf.TapeDevices, conf.WorkDirectory,
		conf.Scripts.Encrypt, conf.Scripts.Mkfs, conf.Scripts.Mount, conf.Scripts.Umount,
	)
	if err := exe.AutoMigrate(); err != nil {
		panic(err)
	}

	s := grpc.NewServer()
	api := apis.New(conf.FilesystemRoot, lib, exe)
	entity.RegisterServiceServer(s, api)

	mux := http.NewServeMux()

	grpcWebServer := grpcweb.WrapServer(s, grpcweb.WithOriginFunc(func(origin string) bool { return true }))
	mux.Handle("/services/", http.StripPrefix("/services/", grpcWebServer))

	fs := http.FileServer(http.Dir("./frontend/assets"))
	mux.Handle("/assets/", http.StripPrefix("/assets/", fs))

	indexBuf, err := ioutil.ReadFile("./frontend/index.html")
	if err != nil {
		panic(err)
	}

	indexBuf = bytes.ReplaceAll(indexBuf, []byte("%%API_BASE%%"), []byte(fmt.Sprintf("%s/services", conf.Domain)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexBuf)
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
