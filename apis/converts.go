package apis

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"google.golang.org/protobuf/proto"
)

func convertFiles(files ...*library.File) []*entity.File {
	// Return logical organization with current display facts, never a File-level content identity.
	results := make([]*entity.File, 0, len(files))
	for _, f := range files {
		results = append(results, &entity.File{
			Id:       f.ID,
			ParentId: f.ParentID,
			Name:     f.Name,
			Mode:     int64(f.Mode),
			ModTime:  f.ModTime.Unix(),
			Size:     f.Size,
			Hash:     f.Hash,
			Tags:     append([]string{}, f.Tags...),
			Note:     f.Note,
			Kind:     f.Kind, CreatedAtMs: f.CreatedAt, UpdatedAtMs: f.UpdatedAt,
			ContentSummary: f.ContentSummary,
		})
	}
	return results
}

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
	results := make([]*entity.Position, 0, len(positions))
	for _, p := range positions {
		results = append(results, &entity.Position{
			Id:        p.ID,
			Signature: p.Signature,
			MediaId:   p.MediaID,
			Path:      p.Path,
			Mode:      int64(p.Mode),
			ModTime:   p.ModTime.Unix(),
			WriteTime: p.WriteTime.Unix(),
			Size:      p.Size,
			Hash:      p.Hash,
			Health:    p.Health, CheckedAtMs: p.CheckedAt, HealthJobId: p.HealthJobID,
		})
	}
	return results
}

func convertMedia(value *library.Media) (*entity.Media, error) {
	if value == nil {
		return nil, nil
	}
	capabilities, err := mediapkg.CapabilitiesForProfile(value.Profile)
	if err != nil {
		return nil, fmt.Errorf("convert Media capabilities failed, media_id=%d, %w", value.ID, err)
	}
	return &entity.Media{
		Id: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name,
		Capabilities: &entity.Capabilities{
			Read: mediapkg.AccessToEntity(capabilities.Read), Write: mediapkg.AccessToEntity(capabilities.Write),
		},
		Profile: proto.Clone(value.Profile).(*entity.MediaProfile), CreateTime: value.CreateTime.Unix(),
		DestroyTime: convertOptionalTime(value.DestroyTime), CapacityBytes: value.CapacityBytes,
		WrittenBytes: value.WrittenBytes,
	}, nil
}

func convertSourceFiles(parent string, files ...fs.FileInfo) []*entity.SourceFile {
	results := make([]*entity.SourceFile, 0, len(files))
	for _, f := range files {
		if !f.Mode().IsDir() && !f.Mode().IsRegular() {
			continue
		}

		_, file := path.Split(f.Name())
		results = append(results, &entity.SourceFile{
			Path:       filepath.Join(parent, file),
			ParentPath: parent,
			Name:       file,
			Mode:       int64(f.Mode()),
			ModTime:    f.ModTime().Unix(),
			Size:       f.Size(),
		})
	}
	return results
}

func convertJobs(jobs ...*executor.Job) []*entity.Job {
	converted := make([]*entity.Job, 0, len(jobs))
	for _, job := range jobs {
		converted = append(converted, job.ToEntity())
	}
	return converted
}

func convertOptionalTime(t *time.Time) *int64 {
	if t == nil {
		return nil
	}

	u := t.Unix()
	return &u
}
