// Command demo prepares the disposable local YATM review environment.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/internal/demo"
)

func main() {
	// Parse the isolated runtime location, server address, and optional review asset.
	root := flag.String("root", filepath.Join(os.TempDir(), "yatm-demo"), "disposable Demo root")
	listen := flag.String("listen", "127.0.0.1:18080", "Demo HTTP listen address")
	reset := flag.Bool("reset", false, "discard and recreate the Demo fixture")
	video := flag.String("video", "", "MP4 used for the Demo video Preview; requires -reset")
	flag.Parse()

	// Prepare the complete fixture before reporting its review surface.
	err := demo.Prepare(context.Background(), demo.Options{
		Root: *root, Listen: *listen, Reset: *reset, VideoPath: *video,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare Demo failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Demo ready: url=http://%s root=%s\n", *listen, *root)
}
