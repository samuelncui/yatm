package scan

import (
	"os"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/executor/observation"
	"github.com/samuelncui/yatm/library"
)

func observeEvidence(source *library.Location, name string, scanned os.FileInfo) (Evidence, error) {
	// The shared observer owns platform I/O; the Job keeps only its typed matching projection.
	keys, err := executor.ObserveTracking(source, name, scanned)
	if err != nil {
		return Evidence{}, err
	}
	return observation.FromKeys(keys), nil
}
