package executor

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/samber/lo"
	"github.com/samuelncui/tapemanager/entity"
	"github.com/samuelncui/tapemanager/library"
	"github.com/samuelncui/tapemanager/tools"
	"github.com/sirupsen/logrus"
)

type restoreFile struct {
	*library.File
	target string
}

func (e *Executor) createRestore(ctx context.Context, job *Job, param *entity.JobRestoreParam) error {
	files, err := e.getRestoreFiles(ctx, param.FileIds...)
	if err != nil {
		return fmt.Errorf("get restore files fail, ids= %v, %w", param.FileIds, err)
	}

	fileIDs := make([]int64, 0, len(files))
	for _, file := range files {
		fileIDs = append(fileIDs, file.ID)
	}

	positions, err := e.lib.MGetPositionByFileID(ctx, fileIDs...)
	if err != nil {
		return err
	}

	tapeMapping := make(map[int64]mapset.Set[int64], 4)
	for _, file := range files {
		for _, posi := range positions[file.ID] {
			set := tapeMapping[posi.TapeID]
			if set == nil {
				tapeMapping[posi.TapeID] = mapset.NewThreadUnsafeSet(file.ID)
				continue
			}
			set.Add(file.ID)
		}
	}

	tapeMap, err := e.lib.MGetTape(ctx, lo.Keys(tapeMapping)...)
	if err != nil {
		return err
	}
	for tapeID := range tapeMapping {
		if tape, has := tapeMap[tapeID]; has && tape != nil {
			continue
		}

		logrus.WithContext(ctx).Infof("tape not found, tape_id= %d", tapeID)
		delete(tapeMap, tapeID)
	}

	restoreTapes := make([]*entity.RestoreTape, 0, len(tapeMapping))
	for len(tapeMapping) > 0 {
		var maxTapeID int64
		for tapeID, files := range tapeMapping {
			if maxTapeID == 0 {
				maxTapeID = tapeID
				continue
			}

			diff := files.Cardinality() - tapeMapping[maxTapeID].Cardinality()
			if diff > 0 {
				maxTapeID = tapeID
				continue
			}
			if diff < 0 {
				continue
			}
			if tapeID < maxTapeID {
				maxTapeID = tapeID
				continue
			}
		}
		if maxTapeID == 0 {
			return fmt.Errorf("max tape not found, tape_ids= %v", lo.Keys(tapeMapping))
		}

		fileIDs := tapeMapping[maxTapeID]
		delete(tapeMapping, maxTapeID)
		if fileIDs.Cardinality() == 0 {
			continue
		}
		for i, f := range tapeMapping {
			tapeMapping[i] = f.Difference(fileIDs)
		}

		targets := make([]*entity.RestoreFile, 0, fileIDs.Cardinality())
		for _, fileID := range fileIDs.ToSlice() {
			file := files[fileID]
			if file == nil {
				continue
			}

			posi := positions[fileID]
			if len(posi) == 0 {
				logrus.WithContext(ctx).Infof("file position not found, file_id= %d", fileID)
				continue
			}

			for _, p := range posi {
				if p.TapeID != maxTapeID {
					continue
				}

				targets = append(targets, &entity.RestoreFile{
					FileId:     file.ID,
					TapeId:     p.TapeID,
					PositionId: p.ID,
					Status:     entity.CopyStatus_PENDING,
					Size:       file.Size,
					Hash:       file.Hash,
					TapePath:   p.Path,
					TargetPath: file.target,
				})
				break
			}
		}

		convertPath := tools.Cache(func(p string) string { return strings.ReplaceAll(p, "/", "\x00") })
		sort.Slice(targets, func(i, j int) bool {
			return convertPath(targets[i].TapePath) < convertPath(targets[j].TapePath)
		})

		restoreTapes = append(restoreTapes, &entity.RestoreTape{
			TapeId:  maxTapeID,
			Barcode: tapeMap[maxTapeID].Barcode,
			Status:  entity.CopyStatus_PENDING,
			Files:   targets,
		})
	}

	job.State = &entity.JobState{State: &entity.JobState_Restore{Restore: &entity.JobRestoreState{
		Step:  entity.JobRestoreStep_PENDING,
		Tapes: restoreTapes,
	}}}
	return nil
}

func (e *Executor) getRestoreFiles(ctx context.Context, rootIDs ...int64) (map[int64]*restoreFile, error) {
	rootIDSet := mapset.NewThreadUnsafeSet(rootIDs...)
	for _, id := range rootIDs {
		parents, err := e.lib.ListParents(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(parents) <= 1 {
			continue
		}

		for _, parent := range parents[:len(parents)-1] {
			if !rootIDSet.Contains(parent.ID) {
				continue
			}

			rootIDSet.Remove(id)
			break
		}
	}

	rootIDs = rootIDSet.ToSlice()
	mapping, err := e.lib.MGetFile(ctx, rootIDs...)
	if err != nil {
		return nil, fmt.Errorf("mget file fail, ids= %v, %w", rootIDs, err)
	}

	files := make([]*restoreFile, 0, len(rootIDs)*8)
	visited := mapset.NewThreadUnsafeSet[int64]()
	for _, root := range mapping {
		if visited.Contains(root.ID) {
			continue
		}

		visited.Add(root.ID)
		if !fs.FileMode(root.Mode).IsDir() {
			files = append(files, &restoreFile{File: root, target: root.Name})
			continue
		}

		found, err := e.visitFiles(ctx, root.Name, nil, visited, root.ID)
		if err != nil {
			return nil, err
		}

		files = append(files, found...)
	}

	results := make(map[int64]*restoreFile, len(files))
	for _, f := range files {
		results[f.ID] = f
	}

	return results, nil
}

func (e *Executor) visitFiles(ctx context.Context, path string, files []*restoreFile, visited mapset.Set[int64], parentID int64) ([]*restoreFile, error) {
	children, err := e.lib.List(ctx, parentID)
	if err != nil {
		return nil, err
	}

	for _, child := range children {
		if visited.Contains(child.ID) {
			continue
		}

		visited.Add(child.ID)

		target := path + "/" + child.Name
		if !fs.FileMode(child.Mode).IsDir() {
			files = append(files, &restoreFile{File: child, target: target})
			continue
		}

		files, err = e.visitFiles(ctx, target, files, visited, child.ID)
		if err != nil {
			return nil, err
		}
	}

	return files, nil
}
