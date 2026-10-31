package apis

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
)

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

	// Preserve optional timestamps and give the response its own mutable geometry.
	var destroyedAt *int64
	if value.DestroyedAtNS != nil {
		destroyedAt = proto.Int64(*value.DestroyedAtNS)
	}
	var positionMap *entity.TapePositionMap
	if value.TapePositionMap != nil {
		positionMap = proto.Clone(value.TapePositionMap).(*entity.TapePositionMap)
	}

	// The saved key never leaves the Library through the API; only its presence is reported.
	profile := proto.Clone(value.Profile).(*entity.MediaProfile)
	keySaved := false
	if tape := profile.GetTape(); tape != nil {
		keySaved, tape.Encryption = tape.Encryption != "", ""
	}
	return &entity.Media{
		Id: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name,
		Capabilities: &entity.Capabilities{
			Read: mediapkg.AccessToEntity(capabilities.Read), Write: mediapkg.AccessToEntity(capabilities.Write),
		},
		Profile: profile, CreatedAtNs: value.CreatedAtNS,
		TapePositionMap: positionMap, EncryptionKeySaved: keySaved,
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
