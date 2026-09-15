// Package buildinfo exposes the identity of an installed release without external I/O.
package buildinfo

import (
	"encoding/json"
	"io"
)

// Version and Commit are set by the release build's linker flags.
var Version = "development"
var Commit = "unknown"

// IsVersion reports whether arguments request only the local program identity.
func IsVersion(args []string) bool {
	return len(args) == 1 && (args[0] == "-version" || args[0] == "--version")
}

// Write emits one JSON object suitable for both users and installer checks.
func Write(w io.Writer, program string) error {
	return json.NewEncoder(w).Encode(struct {
		Program string `json:"program"`
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}{Program: program, Version: Version, Commit: Commit})
}
