package scan

import (
	"os"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
)

func observeEvidence(source *library.Location, scanned os.FileInfo) Evidence {
	return observation.FromKeys(executor.ObserveTracking(source, scanned))
}
