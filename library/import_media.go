package library

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func retainedImportMedia(tx *gorm.DB, included map[string]struct{}) (map[int64]*Media, error) {
	// Whole-inventory replacement brings its own references; a Media-only import retains old copies.
	_, mediaIncluded := included[recordTypeMedia]
	_, positionsIncluded := included[recordTypePosition]
	if !mediaIncluded || positionsIncluded {
		return nil, nil
	}

	// Retain only immutable owners, paging the small Media catalog instead of the Position manifest.
	owners := make(map[int64]*Media)
	var after int64
	for {
		var values []*Media
		if err := tx.Select("id", "kind", "identity", "profile").
			Where("id > ? AND EXISTS (SELECT 1 FROM positions WHERE positions.media_id = media.id)", after).
			Order("id").Limit(batchSize).Find(&values).Error; err != nil {
			return nil, fmt.Errorf("read retained Position owners failed, %w", err)
		}
		if len(values) == 0 {
			return owners, nil
		}
		for _, value := range values {
			owners[value.ID] = value
		}
		after = values[len(values)-1].ID
	}
}

func validateRetainedImportMedia(tx *gorm.DB, owners map[int64]*Media) error {
	// Compare in stable ID order so partial imports report a deterministic conflicting owner.
	ids := make([]int64, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		var values []*Media
		if err := tx.Where("id IN ?", ids[start:end]).Order("id").Find(&values).Error; err != nil {
			return fmt.Errorf("read imported Position owners failed, %w", err)
		}
		if len(values) != end-start {
			return fmt.Errorf("Media-only import removes an owner of retained Positions")
		}

		// Names and capacity can be refreshed, but a physical copy cannot move through integer-ID reuse.
		for _, value := range values {
			owner := owners[value.ID]
			if owner.Kind != value.Kind || owner.Identity != value.Identity || !proto.Equal(owner.Profile, value.Profile) {
				return fmt.Errorf("Media-only import changes retained Position owner, media_id=%d", value.ID)
			}
		}
	}
	return nil
}
