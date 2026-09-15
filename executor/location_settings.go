package executor

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// InitializeLocations imports old configuration once without traversing or indexing its contents.
func (e *Executor) InitializeLocations(ctx context.Context) error {
	// Relative legacy paths were resolved from the process working directory.
	paths := []string{e.paths.Source, e.paths.Target}
	for index, value := range paths {
		if value == "" {
			continue
		}
		canonical, err := CanonicalConfiguredPath(value)
		if err != nil {
			return fmt.Errorf("resolve legacy configured directory failed, %w", err)
		}
		paths[index] = canonical
	}
	return e.lib.MigrateLocationPaths(ctx, localExecutorID, paths[0], paths[1])
}

func (e *Executor) AccessSettings(ctx context.Context) (*entity.GetAccessReply, error) {
	// Expose administrator rules without promoting them to editable Location configuration.
	reply := &entity.GetAccessReply{}
	for _, access := range e.AccessRanges() {
		canonical, err := CanonicalConfiguredPath(access.Root)
		if err != nil {
			return nil, err
		}
		reply.Ranges = append(reply.Ranges, &entity.AccessRange{RootPath: canonical, Ignore: access.Ignore})
	}
	var err error
	reply.MigrationMessages, err = e.lib.LocationMigrationMessages(ctx, localExecutorID)
	return reply, err
}
