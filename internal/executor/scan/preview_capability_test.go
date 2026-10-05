package scan

import (
	"context"
	"github.com/samuelncui/yatm/entity"
)

func (*scanPreviewer) CheckGeneration(context.Context) error { return nil }
func (*scanPreviewer) Capabilities(context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	return &entity.GetPreviewCapabilitiesResponse{Available: true}, nil
}

func (analyzePreviewer) CheckGeneration(context.Context) error { return nil }
func (analyzePreviewer) Capabilities(context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	return &entity.GetPreviewCapabilitiesResponse{Available: true}, nil
}
