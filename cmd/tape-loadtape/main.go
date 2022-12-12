package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/abc950309/tapewriter/executor"
	"github.com/abc950309/tapewriter/library"
	"github.com/abc950309/tapewriter/resource"
	"gopkg.in/yaml.v2"
)

type config struct {
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
	barcode    = flag.String("barcode", "", "barcode for tape")
	device     = flag.String("device", "/dev/nst0", "barcode for tape")
)

func main() {
	flag.Parse()

	if *barcode == "" {
		panic("expect barcode")
	}

	cf, err := os.Open(*configPath)
	if err != nil {
		panic(err)
	}

	conf := new(config)
	if err := yaml.NewDecoder(cf).Decode(conf); err != nil {
		panic(err)
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

	ctx := context.Background()
	tapes, err := lib.MGetTapeByBarcode(ctx, *barcode)
	if err != nil {
		panic(err)
	}

	tape := tapes[*barcode]
	if tape == nil {
		panic(fmt.Errorf("tape not found, barcode= %s", *barcode))
	}

	if err := exe.RestoreLoadTape(ctx, *device, tape); err != nil {
		panic(err)
	}
}
