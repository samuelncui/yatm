package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
)

func (api *API) hydrateFileTags(ctx context.Context, files ...*library.File) error {
	// Load all requested relations in one bounded Library MGet.
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		if file != nil {
			ids = append(ids, file.ID)
		}
	}
	tags, err := api.lib.MGetFileTags(ctx, ids...)
	if err != nil {
		return fmt.Errorf("load File Tags failed, %w", err)
	}

	// Attach stable sorted Tags to the response models only.
	for _, file := range files {
		if file != nil {
			file.Tags = tags[file.ID]
		}
	}
	return nil
}

func convertPositions(positions ...*library.Position) []*entity.Position {
	// Persisted instants are already exact signed nanoseconds; no display-time conversion is needed.
	results := make([]*entity.Position, 0, len(positions))
	for _, p := range positions {
		results = append(results, &entity.Position{
			Id:          p.ID,
			Signature:   p.Signature,
			MediaId:     p.MediaID,
			Path:        p.Path,
			Mode:        int64(p.Mode),
			MtimeNs:     p.MtimeNS,
			WrittenAtNs: p.WrittenAtNS,
			SizeBytes:   p.Size,
			Sha256:      p.Hash,
			Health:      p.Health, CheckedAtNs: p.CheckedAtNS, HealthJobId: p.HealthJobID,
		})
	}
	return results
}

func convertMedia(value *library.Media) (*entity.Media, error) {
	// The immutable profile remains the authority for supported Media operations.
	if value == nil {
		return nil, nil
	}
	capabilities, err := mediapkg.CapabilitiesForProfile(value.Profile)
	if err != nil {
		return nil, fmt.Errorf("convert Media capabilities failed, media_id=%d, %w", value.ID, err)
	}

	// Preserve optional timestamps as absent or exact ns, including an explicitly known epoch zero.
	var destroyedAt *int64
	if value.DestroyedAtNS != nil {
		destroyedAt = proto.Int64(*value.DestroyedAtNS)
	}
	return &entity.Media{
		Id: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name,
		Capabilities: &entity.Capabilities{
			Read: mediapkg.AccessToEntity(capabilities.Read), Write: mediapkg.AccessToEntity(capabilities.Write),
		},
		Profile: proto.Clone(value.Profile).(*entity.MediaProfile), CreatedAtNs: value.CreatedAtNS,
		DestroyedAtNs: destroyedAt, CapacityBytes: value.CapacityBytes,
		WrittenBytes: value.WrittenBytes,
	}, nil
}

func convertJobs(jobs ...*executor.Job) []*entity.Job {
	converted := make([]*entity.Job, 0, len(jobs))
	for _, job := range jobs {
		converted = append(converted, job.ToEntity())
	}
	return converted
}
