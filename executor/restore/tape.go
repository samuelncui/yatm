package restore

import (
	"context"
	"path/filepath"

	"github.com/samuelncui/acp"
)

func (a *jobRestoreRunner) restoreEventHandler(ctx context.Context) acp.EventHandler {
	return func(event acp.Event) {
		switch value := event.(type) {
		case *acp.EventUpdateCount:
			a.logger.WithContext(ctx).Infof("restore copy started, files=%d bytes=%d", value.Files, value.Bytes)
		case *acp.EventUpdateProgress:
			a.getProgress().UpdateSessionCurrent(value.Bytes, value.Files)
		case *acp.EventReportError:
			a.logger.WithContext(ctx).Errorf("restore copy error, source=%q target=%q error=%q", value.Error.Src, value.Error.Dst, value.Error.Err)
		case *acp.EventUpdateJob:
			if value.Job.Status == acp.JobStatusFinished {
				a.logger.WithContext(ctx).Infof("restore file finished, source=%q size=%d", value.Job.FullPath, value.Job.Size)
			}
		}
	}
}

func (a *jobRestoreRunner) restoreTarget(relative string) string {
	return filepath.Join(a.destination.GetRootPath(), filepath.FromSlash(a.destination.GetPath()), filepath.FromSlash(relative))
}
