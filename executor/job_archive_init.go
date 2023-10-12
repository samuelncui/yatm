package executor

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
)

func (a *jobArchiveExecutor) Initialize(ctx context.Context, param *entity.JobParam) error {
	if err := a.applyParam(ctx, param.GetArchive()); err != nil {
		return err
	}

	return a.dispatch(ctx, &entity.JobArchiveDispatchParam{Param: &entity.JobArchiveDispatchParam_WaitForTape{
		WaitForTape: &entity.JobArchiveWaitForTapeParam{},
	}})
}

func (a *jobArchiveExecutor) applyParam(ctx context.Context, param *entity.JobArchiveParam) error {
	if param == nil {
		return fmt.Errorf("archive param is nil")
	}

	return a.updateJob(ctx, func(_ *Job, state *entity.JobArchiveState) error {
		var err error
		sources := make([]*entity.SourceState, 0, len(param.Sources)*8)
		for _, src := range param.Sources {
			src.Base = strings.TrimSpace(src.Base)
			if src.Base[0] != '/' {
				src.Base = path.Join(a.exe.paths.Source, src.Base) + "/"
			}

			sources, err = a.walk(ctx, src, sources)
			if err != nil {
				return err
			}
		}
		sort.Slice(sources, func(i, j int) bool {
			return sources[i].Source.Compare(sources[j].Source) < 0
		})

		for idx, src := range sources {
			if idx > 0 && sources[idx-1].Source.Equal(src.Source) {
				return fmt.Errorf("have multi file with same path, path= %s", src.Source.RealPath())
			}
		}

		state.Step = entity.JobArchiveStep_PENDING
		state.Sources = sources
		return nil
	})
}

func (a *jobArchiveExecutor) walk(ctx context.Context, src *entity.Source, sources []*entity.SourceState) ([]*entity.SourceState, error) {
	path := src.RealPath()

	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("walk get stat, path= '%s', %w", path, err)
	}

	mode := stat.Mode()
	if mode.IsRegular() {
		if stat.Name() == ".DS_Store" {
			return sources, nil
		}
		return append(sources, &entity.SourceState{
			Source: src,
			Size:   stat.Size(),
			Status: entity.CopyStatus_PENDING,
		}), nil
	}
	if mode&acp.UnexpectFileMode != 0 {
		return sources, nil
	}

	files, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("walk read dir, path= '%s', %w", path, err)
	}
	for _, file := range files {
		sources, err = a.walk(ctx, src.Append(file.Name()), sources)
		if err != nil {
			return nil, err
		}
	}

	return sources, nil
}
