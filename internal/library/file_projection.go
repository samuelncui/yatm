package library

import (
	"io/fs"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

func hydrateFileViews(tx *gorm.DB, files ...*File) error {
	// Initialize directory projections without querying content tables.
	ids := make([]int64, 0, len(files))
	byID := make(map[int64][]*File, len(files))
	for _, file := range files {
		file.Mode, file.ModTime = 0, time.Unix(0, file.UpdatedAtNS)
		file.Hash, file.Signature, file.Size = nil, nil, 0
		if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
			file.Mode = uint32(fs.ModeDir | 0755)
			continue
		}
		if _, exists := byID[file.ID]; !exists {
			ids = append(ids, file.ID)
		}
		byID[file.ID] = append(byID[file.ID], file)
	}

	// Preserve original-before-history semantics with one bounded read per fact group.
	projection := tx.Session(&gorm.Session{NewDB: true})
	for start := 0; start < len(ids); start += batchSize {
		batch := ids[start:min(start+batchSize, len(ids))]
		var originals []*FileLocation
		if err := projection.Where("file_id IN ?", batch).Find(&originals).Error; err != nil {
			return err
		}
		found := make(map[int64]bool, len(originals))
		for _, original := range originals {
			found[original.FileID] = true
			for _, file := range byID[original.FileID] {
				file.Mode, file.ModTime, file.Size = original.Mode, time.Unix(0, original.MtimeNS), original.Size
				file.Hash, file.Signature = original.Hash, original.Signature
			}
		}
		missing := make([]int64, 0, len(batch)-len(found))
		for _, id := range batch {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			continue
		}
		var versions []*FileVersion
		if err := projection.Table("file_versions AS v").Select("v.*").Where("v.file_id IN ?", missing).
			Where("v.id = (SELECT latest.id FROM file_versions latest WHERE latest.file_id = v.file_id ORDER BY latest.last_archived_at_ns DESC, latest.id DESC LIMIT 1)").Scan(&versions).Error; err != nil {
			return err
		}
		for _, version := range versions {
			for _, file := range byID[version.FileID] {
				file.Mode, file.ModTime, file.Size = version.Mode, time.Unix(0, version.MtimeNS), version.Size
				file.Hash, file.Signature = version.Hash, version.Signature
			}
		}
	}
	return nil
}

// hydrateFileFacts retains the single-row boundary using the same projection semantics.
func hydrateFileFacts(tx *gorm.DB, file *File) error { return hydrateFileViews(tx, file) }
