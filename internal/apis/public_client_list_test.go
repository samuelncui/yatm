package apis_test

import (
	"errors"
	"io"

	"github.com/samuelncui/yatm/entity"
)

// collectListed drains one public streaming listing into its rows.
func collectListed(stream entity.FilesService_ListClient) ([]*entity.FilesEntry, error) {
	var entries []*entity.FilesEntry
	for {
		batch, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, batch.Entries...)
	}
}

// collectSearched returns one public query page's rows.
func collectSearched(reply *entity.SearchFilesResponse, err error) ([]*entity.FilesEntry, error) {
	if err != nil {
		return nil, err
	}
	return reply.Entries, nil
}
