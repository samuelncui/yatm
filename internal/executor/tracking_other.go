//go:build !linux && !darwin

package executor

import (
	"github.com/samuelncui/yatm/internal/library"
	"os"
)

func ObserveTracking(*library.Location, os.FileInfo) []*library.FileTrackingKey {
	return nil
}
