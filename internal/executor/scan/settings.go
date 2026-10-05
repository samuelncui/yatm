package scan

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/protobuf/proto"
)

func freezePreviewSettings(ctx context.Context, exe *executor.Executor) (*entity.PreviewJobSettings, error) {
	if exe.Lib() == nil || exe.Lib().Settings() == nil {
		return nil, fmt.Errorf("Preview Settings module is not configured")
	}
	settings, err := exe.Lib().Settings().Preview.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Preview Settings failed, %w", err)
	}
	return proto.Clone(&entity.PreviewJobSettings{Generators: settings.GetGenerators()}).(*entity.PreviewJobSettings), nil
}
