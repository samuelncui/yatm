//go:build !linux && !darwin && !freebsd

package media

// SeparateFilesystem has no portable device-number comparison on this platform.
func SeparateFilesystem(string) bool { return false }
