package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"

	"github.com/abc950309/tapewriter"
)

func main() {
	f, err := os.OpenFile("/dev/st0", os.O_WRONLY, 0666)
	if err != nil {
		panic(err)
	}

	w, err := tapewriter.NewWriter(f)
	if err != nil {
		panic(err)
	}

	path := os.Args[1]
	info, err := os.Stat(path)
	if err != nil {
		panic(err)
	}

	target, err := os.Open(path)
	if err != nil {
		panic(err)
	}

	w.WriteHeader(&tar.Header{
		Name: info.Name(),
		Size: info.Size(),
	})

	// syscall.Write()

	written, err := io.Copy(w, target)
	if err != nil {
		panic(err)
	}

	fmt.Println(written)
}
