package library

import (
	"github.com/samuelncui/yatm/internal/resource"
)

// PrepareTemporary configures disposable Library work before requests are admitted.
// The service owns periodic cleanup; live operations retain their own directories.
func (l *Library) PrepareTemporary(work string) error {
	// Validate the shared root before publishing the configured path to requests.
	if err := resource.PrepareTemporary(work); err != nil {
		return err
	}
	l.work = work
	return nil
}
