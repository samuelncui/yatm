package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

func (api *API) FileSearch(ctx context.Context, req *entity.FileSearchRequest) (*entity.FileSearchReply, error) {
	// Keep ID-based lookup and hydration within one catalog maintenance admission.
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()

	// Query the bounded result page before hydrating its public File metadata.
	page, err := api.lib.SearchFilesIn(ctx, req.Query, req.GetCursor(), req.GetLimit(), req.Scope, req.LocationId, req.LocationRevision)
	if err != nil {
		return nil, fmt.Errorf("search Files failed, %w", err)
	}
	files := make([]*library.File, 0, len(page.Results))
	for _, result := range page.Results {
		files = append(files, result.File)
	}
	if err := api.hydrateFileTags(ctx, files...); err != nil {
		return nil, err
	}
	if err := api.lib.HydrateFileContent(ctx, files...); err != nil {
		return nil, err
	}

	// Preserve the Library page order while pairing each File with its logical path.
	converted := convertFiles(files...)
	reply := &entity.FileSearchReply{
		Results:    make([]*entity.FileSearchResult, 0, len(page.Results)),
		NextCursor: page.NextCursor,
	}
	for index, result := range page.Results {
		reply.Results = append(reply.Results, &entity.FileSearchResult{File: converted[index], Path: result.Path})
	}
	return reply, nil
}
