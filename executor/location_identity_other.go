//go:build !linux && !darwin && !freebsd

package executor

import "os"

func locationNativeIdentity(os.FileInfo) string { return "" }
