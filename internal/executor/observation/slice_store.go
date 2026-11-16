package observation

import (
	"bytes"
	"context"
	"fmt"
	"sort"
)

// sliceStore applies the manifest matching contract to one small request-owned batch of files.
type sliceStore struct {
	items []*Item
}

// NewSliceStore matches over items in memory; items must be ordinary files with unique paths.
func NewSliceStore(items []*Item) MatchStore {
	sorted := append([]*Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	return &sliceStore{items: sorted}
}

func (s *sliceStore) FileClaimed(_ context.Context, fileID int64) (bool, error) {
	for _, item := range s.items {
		if item.FileID == fileID {
			return true, nil
		}
	}
	return false, nil
}

func (s *sliceStore) FindCandidate(_ context.Context, criteria MatchCriteria) (*MatchCandidate, error) {
	for _, item := range s.items {
		if item.FileID != 0 {
			continue
		}
		matched := false
		switch criteria.Kind {
		case MatchPath:
			matched = item.Path == criteria.Path
		case MatchSignature:
			matched = len(item.Signature) != 0 && bytes.Equal(item.Signature, criteria.Signature)
		case MatchNative:
			matched = item.Evidence.SameObject(criteria.Evidence)
		default:
			return nil, fmt.Errorf("unsupported continuity match kind %d", criteria.Kind)
		}
		if matched {
			return &MatchCandidate{ID: item.ID, Path: item.Path, Size: item.Size, Hash: item.Hash}, nil
		}
	}
	return nil, nil
}

func (s *sliceStore) Assign(_ context.Context, id, fileID int64, signature []byte) error {
	for _, item := range s.items {
		if item.ID == id {
			item.FileID = fileID
			if len(signature) != 0 {
				item.Signature = signature
			}
			return nil
		}
	}
	return fmt.Errorf("observation %d is not staged", id)
}

func (s *sliceStore) Pending(_ context.Context, after string, limit int) ([]Pending, error) {
	var pending []Pending
	for _, item := range s.items {
		if item.FileID == 0 && item.Path > after && len(pending) < limit {
			pending = append(pending, Pending{Path: item.Path, Signature: item.Signature, Evidence: item.Evidence})
		}
	}
	return pending, nil
}

func (s *sliceStore) Observed(_ context.Context, paths []string) (map[string]bool, error) {
	observed := make(map[string]bool, len(paths))
	for _, name := range paths {
		index := sort.Search(len(s.items), func(i int) bool { return s.items[i].Path >= name })
		observed[name] = index < len(s.items) && s.items[index].Path == name
	}
	return observed, nil
}
