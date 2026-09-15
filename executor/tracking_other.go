//go:build !linux && !darwin

package executor

import (
	"github.com/samuelncui/yatm/library"
	"os"
)

func ObserveTracking(*library.Location, string, os.FileInfo) ([]*library.FileTrackingKey, error) {
	return nil, nil
}

func ReadTracking(*library.Location, string, os.FileInfo) ([]*library.FileTrackingKey, error) {
	return nil, nil
}
