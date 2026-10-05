package executor

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (e *Executor) AccessSettings(ctx context.Context) (*entity.GetLocationAccessResponse, error) {
	// Expose administrator rules without promoting them to editable Location configuration.
	reply := &entity.GetLocationAccessResponse{}
	for _, access := range e.AccessRanges() {
		canonical, err := CanonicalConfiguredPath(access.Root)
		if err != nil {
			return nil, err
		}
		reply.Ranges = append(reply.Ranges, &entity.AccessRange{RootPath: canonical, Ignore: access.Ignore})
	}
	return reply, nil
}
