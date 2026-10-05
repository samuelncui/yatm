package apis_test

import (
	"context"
	"github.com/samuelncui/yatm/entity"
)

func (*previewFixture) CheckGeneration(context.Context) error { return nil }
func (*previewFixture) Capabilities(context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	return &entity.GetPreviewCapabilitiesResponse{Available: true}, nil
}
