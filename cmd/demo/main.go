// Command demo prepares the disposable local YATM review environment.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/internal/demo"
	"github.com/samuelncui/yatm/internal/tools"
)

func main() {
	// Parse the isolated runtime location and explicit fixture overrides.
	root := flag.String("root", filepath.Join(os.TempDir(), "yatm-demo"), "disposable Demo root")
	listen := flag.String("listen", "127.0.0.1:18080", "Demo HTTP listen address")
	reset := flag.Bool("reset", false, "discard and recreate the Demo fixture")
	video := flag.String("video", "", "MP4 used for the Demo video Preview; requires -reset")
	identicalFiles := flag.Int("identical-files", 207,
		"total members in Shared files' Large group and Many groups (at least 207); reuse requires -reset")
	flag.Parse()
	options := demo.Options{Root: *root, Listen: *listen, Reset: *reset, VideoPath: *video}
	flag.Visit(func(value *flag.Flag) {
		if value.Name == "identical-files" {
			options.IdenticalFiles = identicalFiles
		}
	})

	// Prepare the complete fixture before reporting its review surface.
	err := demo.Prepare(tools.ShutdownContext, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare Demo failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Demo ready: url=http://%s root=%s\n", *listen, *root)
}
