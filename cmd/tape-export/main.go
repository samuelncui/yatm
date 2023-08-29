package main

import (
	"context"
	"os"

	"github.com/abc950309/tapewriter/external"
	"github.com/abc950309/tapewriter/library"
	"github.com/abc950309/tapewriter/resource"
)

func main() {
	ctx := context.Background()

	db, err := resource.NewDBConn("sqlite", "./tapes.db")
	if err != nil {
		panic(err)
	}

	lib := library.New(db)
	if err := lib.AutoMigrate(); err != nil {
		panic(err)
	}

	file := os.Args[1]
	barcode := os.Args[2]
	name := os.Args[3]

	f, err := os.Open(file)
	if err != nil {
		panic(err)
	}

	ext := external.New(lib)
	if err := ext.ImportACPReport(ctx, barcode, name, "file:tape.key", f); err != nil {
		panic(err)
	}
}
