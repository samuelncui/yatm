package library

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type identicalOriginal struct {
	FileLocation
}

func (s *IdenticalSnapshot) collect(tx *gorm.DB, scope IdenticalScope) error {
	// Stage organization separately so Trash descendants can be pruned without recursive in-memory trees.
	if scope.Source == IdenticalLibrary {
		var files []fileRow
		if err := tx.Select("id,parent_id,name,kind").
			Order("id").
			FindInBatches(&files, identicalBatch, func(_ *gorm.DB, _ int) error {
				nodes := make([]identicalNode, 0, len(files))
				for _, f := range files {
					path := ""
					if f.ParentID == 0 {
						path = f.Name
					}
					nodes = append(nodes, identicalNode{FileID: f.ID, ParentID: f.ParentID, Component: f.ID,
						Name: f.Name, Path: path, Kind: f.Kind, Excluded: f.ID == TrashFileID})
				}
				return s.db.Create(&nodes).Error
			}).Error; err != nil {
			return fmt.Errorf("stage Library identities failed, %w", err)
		}
		if err := s.stageIdenticalPaths(); err != nil {
			return err
		}
		for {
			result := s.db.Model(&identicalNode{}).
				Where("excluded = ? AND parent_id IN (?)", false, s.db.Model(&identicalNode{}).
					Select("file_id").
					Where("excluded = ?", true)).
				Update("excluded", true)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				break
			}
		}
	}

	// Library retains every original for fingerprints; Locations group only signed originals.
	request := tx.Model(&identicalOriginal{}).Table("file_locations").Select("file_locations.*").
		Order("file_locations.file_id")
	if scope.Source == IdenticalLocations {
		ids := make([]int64, 0, len(scope.Roots))
		for _, root := range scope.Roots {
			ids = append(ids, root.LocationID)
		}
		request = request.Where("location_id IN ? AND signature IS NOT NULL AND length(signature)>0", uniqueFileIDs(ids))
	}
	var originals []identicalOriginal
	if err := request.FindInBatches(&originals, identicalBatch, func(_ *gorm.DB, _ int) error {
		// Retain bounded batches for both node updates and content evidence.
		nodes := make([]identicalNode, 0, len(originals))
		edges := make([]identicalEdge, 0, len(originals))
		existing := make(map[int64]struct{}, len(originals))
		if scope.Source == IdenticalLibrary {
			ids := make([]int64, 0, len(originals))
			for _, o := range originals {
				ids = append(ids, o.FileID)
			}
			var found []int64
			if err := s.db.Model(&identicalNode{}).Where("file_id IN ?", ids).Pluck("file_id", &found).Error; err != nil {
				return err
			}
			for _, id := range found {
				existing[id] = struct{}{}
			}
		}
		for _, o := range originals {
			if scope.Source == IdenticalLocations && !identicalInRoots(o.FileLocation, scope.Roots) {
				continue
			}
			if scope.Source == IdenticalLibrary {
				if _, ok := existing[o.FileID]; !ok {
					continue
				}
			}
			nodes = append(nodes, identicalNode{FileID: o.FileID, Component: o.FileID, Name: filepath.Base(o.Path), Path: o.Path,
				Kind: entity.FileKind_FILE_KIND_REGULAR, Size: &o.Size, Original: &o.FileLocation})
			if len(o.Signature) > 0 {
				edges = append(edges, identicalEdge{FileID: o.FileID, Signature: o.Signature})
			}
		}
		if len(nodes) == 0 {
			return nil
		}
		if err := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}}, DoUpdates: clause.AssignmentColumns([]string{"original", "size"})}).Create(&nodes).Error; err != nil {
			return err
		}
		if len(edges) > 0 {
			if err := s.db.Create(&edges).Error; err != nil {
				return err
			}
		}

		return nil
	}).Error; err != nil {
		return fmt.Errorf("stage original content failed, %w", err)
	}
	if scope.Source == IdenticalLocations {
		return nil
	}

	// Saved evidence is independent of current originals and establishes transitive Library matches.
	var versions []FileVersion
	if err := tx.Select("id,file_id,signature").Order("id").FindInBatches(&versions, identicalBatch, func(_ *gorm.DB, _ int) error {
		edges := make([]identicalEdge, 0, len(versions))
		for _, v := range versions {
			if len(v.Signature) == 0 {
				continue
			}
			edges = append(edges, identicalEdge{FileID: v.FileID, VersionID: v.ID, Signature: v.Signature})
		}
		if len(edges) == 0 {
			return nil
		}
		return s.db.Create(&edges).Error
	}).Error; err != nil {
		return fmt.Errorf("stage saved content failed, %w", err)
	}
	return s.stageVersionSizes(tx)
}

// stageVersionSizes retains the displayed archived size when a File has no original.
func (s *IdenticalSnapshot) stageVersionSizes(tx *gorm.DB) error {
	// Read one latest saved version per File in ID order without collecting the result in memory.
	rows, err := tx.Raw(`SELECT file_id, size FROM (
		SELECT v.file_id, v.size, ROW_NUMBER() OVER (
			PARTITION BY v.file_id ORDER BY v.last_archived_at_ns DESC, v.id DESC) AS version_order
		FROM file_versions AS v LEFT JOIN file_locations AS o ON o.file_id = v.file_id
		WHERE o.file_id IS NULL
	) AS latest_sizes WHERE version_order = 1 ORDER BY file_id`).Rows()
	if err != nil {
		return fmt.Errorf("read latest identical FileVersion sizes failed, %w", err)
	}
	defer rows.Close()

	// Apply each bounded batch only to staged Files lacking an original.
	ids := make([]int64, 0, identicalBatch)
	args := make([]any, 0, 2*identicalBatch+1)
	flush := func() error {
		if len(ids) == 0 {
			return nil
		}
		query := "UPDATE identical_nodes SET size = CASE file_id" + strings.Repeat(" WHEN ? THEN ?", len(ids)) +
			" END WHERE file_id IN ? AND original IS NULL"
		if err := s.db.Exec(query, append(args, ids)...).Error; err != nil {
			return fmt.Errorf("stage latest identical FileVersion sizes failed, %w", err)
		}
		ids = ids[:0]
		args = args[:0]
		return nil
	}
	for rows.Next() {
		var id int64
		var size sql.NullInt64
		if err := rows.Scan(&id, &size); err != nil {
			return fmt.Errorf("scan latest identical FileVersion size failed, %w", err)
		}
		ids = append(ids, id)
		args = append(args, id, size)
		if len(ids) == identicalBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read latest identical FileVersion sizes failed, %w", err)
	}
	return flush()
}

func (s *IdenticalSnapshot) stageIdenticalPaths() error {
	// Parent rows are still staged here; fill paths by depth before graph pruning removes directories.
	for {
		result := s.db.Exec(`UPDATE identical_nodes SET path = (
			SELECT parent.path || '/' || identical_nodes.name FROM identical_nodes AS parent
			WHERE parent.file_id = identical_nodes.parent_id)
			WHERE path = '' AND parent_id <> 0 AND EXISTS (
				SELECT 1 FROM identical_nodes AS parent
				WHERE parent.file_id = identical_nodes.parent_id AND parent.path <> '')`)
		if result.Error != nil {
			return fmt.Errorf("stage identical File paths failed, %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
	}
}

func identicalInRoots(o FileLocation, roots []IdenticalRoot) bool {
	if o.Path == ".trash" || strings.HasPrefix(o.Path, ".trash/") {
		return false
	}
	for _, r := range roots {
		if r.LocationID == o.LocationID {
			return true
		}
	}
	return false
}
func (s *IdenticalSnapshot) connect() error {
	// Exclude logical Trash and non-files before traversing shared signatures.
	if err := s.db.Where("excluded = ? OR kind <> ?", true, entity.FileKind_FILE_KIND_REGULAR).Delete(&identicalNode{}).Error; err != nil {
		return err
	}
	if err := s.db.Where("file_id NOT IN (?)", s.db.Model(&identicalNode{}).
		Select("file_id")).Delete(&identicalEdge{}).Error; err != nil {
		return err
	}

	// A bounded number of whole-graph reductions settles common small groups in one batch.
	for pass := 0; pass < 2; pass++ {
		if err := s.reduceSignatures(); err != nil {
			return err
		}
		neighbors := s.db.Table("identical_edges AS own").Select("MIN(shared.component)").
			Joins("JOIN identical_minimums AS shared ON shared.signature = own.signature").
			Where("own.file_id = identical_nodes.file_id")
		result := s.db.Model(&identicalNode{}).Where("component > (?)", neighbors).
			Update("component", gorm.Expr("(?)", neighbors))
		if result.Error != nil {
			return fmt.Errorf("connect identical content failed, %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
	}

	// A long chain uses disk-backed traversal instead of more whole-graph reductions.
	// Stage shared signatures, but visit only components whose labels have not converged.
	if err := s.db.Exec(`INSERT INTO identical_component_signatures (signature, visited)
		SELECT signature, 0 FROM identical_edges GROUP BY signature HAVING MIN(file_id) <> MAX(file_id)`).Error; err != nil {
		return fmt.Errorf("stage shared identical signatures failed, %w", err)
	}
	if err := s.db.Exec(`UPDATE identical_nodes SET unresolved = 1 WHERE file_id IN (
		SELECT edge.file_id FROM identical_edges AS edge WHERE edge.signature IN (
			SELECT linked.signature FROM identical_edges AS linked
			JOIN identical_nodes AS node ON node.file_id = linked.file_id
			GROUP BY linked.signature HAVING MIN(node.component) <> MAX(node.component)))`).Error; err != nil {
		return fmt.Errorf("mark unresolved identical Files failed, %w", err)
	}

	// An unresolved edge seeds one whole component; settled unrelated groups are untouched.
	for {
		var seed identicalNode
		if err := s.db.Select("file_id").Where("unresolved = ? AND expanded = ?", true, false).
			Order("file_id").Limit(1).Find(&seed).Error; err != nil {
			return fmt.Errorf("find identical component seed failed, %w", err)
		}
		if seed.FileID == 0 {
			return nil
		}
		if err := s.db.Model(&identicalNode{}).Where("file_id = ?", seed.FileID).
			Update("component", 0).Error; err != nil {
			return fmt.Errorf("start identical component %d failed, %w", seed.FileID, err)
		}
		if err := s.expandIdenticalComponent(); err != nil {
			return err
		}
		var minimum int64
		if err := s.db.Model(&identicalNode{}).Where("component = ?", 0).
			Select("MIN(file_id)").Scan(&minimum).Error; err != nil {
			return fmt.Errorf("finish identical component failed, %w", err)
		}
		if err := s.db.Model(&identicalNode{}).Where("component = ?", 0).
			Update("component", minimum).Error; err != nil {
			return fmt.Errorf("label identical component %d failed, %w", minimum, err)
		}
	}
}

// Each reduction is materialized so SQLite never reevaluates all peers for every member.
type identicalMinimum struct {
	Signature []byte `gorm:"primaryKey;type:varbinary(256)"`
	Component int64
}

func (s *IdenticalSnapshot) reduceSignatures() error {
	// Rebuild the bounded, disk-backed reduction from the previous component labels.
	if err := s.db.Where("1 = 1").Delete(&identicalMinimum{}).Error; err != nil {
		return err
	}
	var after []byte
	for {
		request := s.db.Table("identical_edges AS edge").Select("edge.signature,MIN(node.component) AS component").
			Joins("JOIN identical_nodes AS node ON node.file_id = edge.file_id").Group("edge.signature").
			Order("edge.signature").Limit(identicalBatch)
		if after != nil {
			request = request.Where("edge.signature > ?", after)
		}
		var rows []identicalMinimum
		if err := request.Scan(&rows).Error; err != nil {
			return fmt.Errorf("reduce identical signatures failed, %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := s.db.Create(&rows).Error; err != nil {
			return err
		}
		after = rows[len(rows)-1].Signature
	}
}

func (s *IdenticalSnapshot) expandIdenticalComponent() error {
	// Process each File once and each shared signature once, without holding a whole group in memory.
	for {
		var nodes []identicalNode
		if err := s.db.Select("file_id").Where("component = ? AND expanded = ?", 0, false).
			Order("file_id").Limit(identicalBatch).Find(&nodes).Error; err != nil {
			return fmt.Errorf("read identical component frontier failed, %w", err)
		}
		if len(nodes) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(nodes))
		for _, node := range nodes {
			ids = append(ids, node.FileID)
		}
		for {
			var signatures []identicalComponentSignature
			if err := s.db.Table("identical_component_signatures AS signature").Select("signature.signature").
				Joins("JOIN identical_edges AS edge ON edge.signature = signature.signature").
				Where("edge.file_id IN ? AND signature.visited = ?", ids, false).
				Group("signature.signature").Order("signature.signature").Limit(identicalBatch).
				Scan(&signatures).Error; err != nil {
				return fmt.Errorf("read shared identical signatures failed, %w", err)
			}
			if len(signatures) == 0 {
				break
			}
			keys := make([][]byte, 0, len(signatures))
			for _, signature := range signatures {
				keys = append(keys, signature.Signature)
			}
			peers := s.db.Model(&identicalEdge{}).Select("file_id").Where("signature IN ?", keys)
			if err := s.db.Model(&identicalNode{}).Where("expanded = ? AND file_id IN (?)", false, peers).
				Update("component", 0).Error; err != nil {
				return fmt.Errorf("expand identical component failed, %w", err)
			}
			if err := s.db.Model(&identicalComponentSignature{}).Where("signature IN ?", keys).
				Update("visited", true).Error; err != nil {
				return fmt.Errorf("advance identical signatures failed, %w", err)
			}
		}
		if err := s.db.Model(&identicalNode{}).Where("file_id IN ?", ids).
			Update("expanded", true).Error; err != nil {
			return fmt.Errorf("advance identical Files failed, %w", err)
		}
	}
}
