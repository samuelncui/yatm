package apis

import (
	"io/fs"
	"path"
	"path/filepath"
	"time"

	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/executor"
	"github.com/abc950309/tapewriter/library"
)

func convertFiles(files ...*library.File) []*entity.File {
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
		})
	}
	return results
}

func convertPositions(positions ...*library.Position) []*entity.Position {
	results := make([]*entity.Position, 0, len(positions))
	for _, p := range positions {
		results = append(results, &entity.Position{
			Id:        p.ID,
			FileId:    p.FileID,
			TapeId:    p.TapeID,
			Path:      p.Path,
			Mode:      int64(p.Mode),
			ModTime:   p.ModTime.Unix(),
			WriteTime: p.WriteTime.Unix(),
			Size:      p.Size,
			Hash:      p.Hash,
		})
	}
	return results
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
		converted = append(converted, &entity.Job{
			Id:         job.ID,
			Status:     job.Status,
			Priority:   job.Priority,
			CreateTime: job.CreateTime.Unix(),
			UpdateTime: job.UpdateTime.Unix(),
			State:      job.State,
		})
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

func map2list[K, T comparable](mapping map[K]T) []T {
	result := make([]T, 0, len(mapping))
	for _, v := range mapping {
		result = append(result, v)
	}
	return result
}
