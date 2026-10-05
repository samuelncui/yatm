//go:build !linux

package media

// VolumeSerialNumber has no portable block-device probe on this platform; callers keep
// the serial editable instead of guessing.
func VolumeSerialNumber(string) string { return "" }
