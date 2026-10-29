//go:build !linux

package tape

func OpenTransport(string, bool) (Transport, error) { return nil, ErrUnsupported }
