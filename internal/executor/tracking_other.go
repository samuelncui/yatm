//go:build !linux && !darwin

package executor

import "os"

func nativeStat(os.FileInfo) (nativeObject, bool) { return nativeObject{}, false }

func filesystemScope(string) string { return "" }

// Lstat observes one entry; this platform supplies no native identity.
func Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
