package archive

import (
	"context"
	"github.com/samuelncui/yatm/entity"
)

func (archivePreviewer) CheckGeneration(context.Context) error { return nil }
func (archivePreviewer) Capabilities(context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	return &entity.GetPreviewCapabilitiesResponse{Available: true}, nil
}
