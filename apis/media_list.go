package apis

import (
	"context"
	"fmt"
	"sort"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
)

func (api *API) MediaList(ctx context.Context, req *entity.MediaListRequest) (*entity.MediaListReply, error) {
	// Reject an absent request before routing its typed filter.
	if req == nil {
		return nil, fmt.Errorf("Media list request is missing")
	}

	// Resolve the requested Library page through the existing typed oneof router.
	var values []*library.Media
	var hasMore bool
	route := tools.NewActionRouter[*entity.MediaListRequest, entity.OneofMediaListRequest](
		tools.ActionMethod(func(ctx context.Context, filter *entity.MediaFilter) error {
			var err error
			values, hasMore, err = api.lib.ListMediaPage(ctx, filter)
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, filter *entity.MediaMGetRequest) error {
			indexed, err := api.lib.MGetMedia(ctx, filter.Ids...)
			if err != nil {
				return err
			}
			values = make([]*library.Media, 0, len(indexed))
			for _, value := range indexed {
				values = append(values, value)
			}
			sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
			return nil
		}),
	)
	if err := route(ctx, req); err != nil {
		return nil, err
	}

	// Discover mounted Volumes once for the complete response page.
	hasVolume := false
	for _, value := range values {
		if value.Kind == entity.MediaKind_MEDIA_KIND_VOLUME {
			hasVolume = true
			break
		}
	}
	volumes := map[string]*mediapkg.Volume{}
	if hasVolume {
		var err error
		volumes, err = discoverVolumeRuntime(api.exe.Paths().Volumes)
		if err != nil {
			return nil, fmt.Errorf("discover mounted Volumes failed, %w", err)
		}
	}

	// Convert durable Media facts and attach response-only runtime availability.
	result := make([]*entity.Media, 0, len(values))
	for _, value := range values {
		converted, err := convertMedia(value)
		if err != nil {
			return nil, err
		}
		if err := applyVolumeRuntime(value, converted, volumes[value.Identity]); err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return &entity.MediaListReply{Media: result, HasMore: hasMore}, nil
}
