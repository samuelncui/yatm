package library

import "context"

// readOriginal reads one File's current original through the production File read path.
func readOriginal(ctx context.Context, lib *Library, fileID int64) (*FileLocation, error) {
	facts, err := lib.ReadFileFacts(ctx, []int64{fileID}, false, false)
	if err != nil {
		return nil, err
	}
	return facts[fileID].Original, nil
}
