package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// identicalComponentSignature keeps the graph frontier on disk, including signatures already visited.
type identicalComponentSignature struct {
	Signature []byte `gorm:"primaryKey;type:varbinary(256)"`
	Visited   bool   `gorm:"index"`
}

// OpenIdenticalComponent reads only the connected component reachable from seedFileID.
// It uses the same staged nodes and fingerprint as a complete Identical snapshot.
func (l *Library) OpenIdenticalComponent(
	ctx context.Context, scope IdenticalScope, seedFileID int64,
) (result *IdenticalSnapshot, returnErr error) {
	// Prepare disposable staging with the canonical scope used by existing group fingerprints.
	var err error
	scope, err = normalizeIdenticalScope(scope)
	if err != nil {
		return nil, err
	}
	if seedFileID <= 0 {
		return nil, fmt.Errorf("invalid identical File ID")
	}
	s, err := l.newIdenticalSnapshot(ctx, scope, "yatm-identical-component-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			returnErr = errors.Join(returnErr, s.Close())
		}
	}()
	if err := s.db.AutoMigrate(&identicalNode{}, &identicalEdge{}, &identicalComponentSignature{}); err != nil {
		return nil, fmt.Errorf("migrate identical component staging failed, %w", err)
	}

	// Hold one catalog read while expanding the component; staging remains independent of that transaction.
	if err := l.readDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.stageComponentFile(tx, scope, seedFileID); err != nil {
			return err
		}
		for {
			var frontier identicalComponentSignature
			if err := s.db.Where("visited = ?", false).Order("signature").Limit(1).Find(&frontier).Error; err != nil {
				return fmt.Errorf("read identical component frontier failed, %w", err)
			}
			if len(frontier.Signature) == 0 {
				return nil
			}
			if err := s.expandComponentSignature(tx, scope, frontier.Signature); err != nil {
				return err
			}
			if err := s.db.Model(&identicalComponentSignature{}).Where("signature = ?", frontier.Signature).
				Update("visited", true).Error; err != nil {
				return fmt.Errorf("advance identical component frontier failed, %w", err)
			}
		}
	}); err != nil {
		return nil, err
	}

	// Existing group APIs identify a component by its lowest File ID.
	var minimum struct{ ID int64 }
	if err := s.db.Model(&identicalNode{}).Select("COALESCE(MIN(file_id),0) AS id").Scan(&minimum).Error; err != nil {
		return nil, fmt.Errorf("identify identical component failed, %w", err)
	}
	if minimum.ID != 0 {
		if err := s.db.Model(&identicalNode{}).Where("1 = 1").Update("component", minimum.ID).Error; err != nil {
			return nil, fmt.Errorf("label identical component failed, %w", err)
		}
	}
	s.Revision, err = s.fingerprint(0)
	if err != nil {
		return nil, fmt.Errorf("fingerprint identical component failed, %w", err)
	}
	var group struct {
		Name  string
		Count int64
	}
	if err := s.db.Model(&identicalNode{}).Select("MIN(name) AS name, COUNT(*) AS count").Scan(&group).Error; err != nil {
		return nil, fmt.Errorf("summarize identical component failed, %w", err)
	}
	s.componentOnly = true
	if group.Count > 1 {
		s.componentGroup = &IdenticalGroup{ID: fmt.Sprint(minimum.ID), Name: group.Name,
			Count: group.Count, Fingerprint: s.Revision}
	}

	// The returned owner remains usable after its construction request finishes.
	s.db = s.db.WithContext(context.Background())
	return s, nil
}

func (s *IdenticalSnapshot) expandComponentSignature(tx *gorm.DB, scope IdenticalScope, signature []byte) error {
	// Both original and saved-content indexes can lead to new members. Page by File ID.
	var after int64
	for {
		var ids []int64
		request := tx.Model(&FileLocation{}).Where("signature = ? AND file_id > ?", signature, after)
		if scope.Source == IdenticalLocations {
			roots := make([]int64, 0, len(scope.Roots))
			for _, root := range scope.Roots {
				roots = append(roots, root.LocationID)
			}
			request = request.Where("location_id IN ?", roots).
				Where("path <> ? AND path NOT LIKE ?", ".trash", ".trash/%")
		}
		if err := request.Order("file_id").Limit(identicalBatch).Pluck("file_id", &ids).Error; err != nil {
			return fmt.Errorf("find identical original peers failed, %w", err)
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if err := s.stageComponentFile(tx, scope, id); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
	if scope.Source == IdenticalLocations {
		return nil
	}

	// Saved versions extend Library components even when no original currently has the signature.
	after = 0
	for {
		var ids []int64
		if err := tx.Model(&FileVersion{}).Distinct("file_id").
			Where("signature = ? AND file_id > ?", signature, after).
			Order("file_id").Limit(identicalBatch).Pluck("file_id", &ids).Error; err != nil {
			return fmt.Errorf("find identical saved peers failed, %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := s.stageComponentFile(tx, scope, id); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

func (s *IdenticalSnapshot) stageComponentFile(tx *gorm.DB, scope IdenticalScope, id int64) error {
	// A staged node marks a File whose complete evidence has already been copied.
	var existing identicalNode
	if err := s.db.Select("file_id").Where("file_id = ?", id).Limit(1).Find(&existing).Error; err != nil {
		return fmt.Errorf("check identical component member failed, %w", err)
	}
	if existing.FileID != 0 {
		return nil
	}

	// Apply the same source and Trash eligibility as complete collection.
	var node identicalNode
	if scope.Source == IdenticalLibrary {
		var row fileRow
		if err := tx.Select("id,parent_id,name,kind").Where("id = ?", id).Limit(1).Find(&row).Error; err != nil {
			return fmt.Errorf("read identical Library File failed, %w", err)
		}
		if row.ID == 0 || row.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return nil
		}
		for parent, depth := row.ParentID, 0; parent != 0; depth++ {
			if depth > 1000 {
				return fmt.Errorf("identical File ancestry is too deep")
			}
			if parent == TrashFileID {
				return nil
			}
			var ancestor fileRow
			if err := tx.Select("id,parent_id").Where("id = ?", parent).Limit(1).Find(&ancestor).Error; err != nil {
				return fmt.Errorf("read identical File ancestry failed, %w", err)
			}
			if ancestor.ID == 0 {
				break
			}
			parent = ancestor.ParentID
		}
		node = identicalNode{FileID: id, Component: id, Name: row.Name, Kind: row.Kind}
	} else {
		var original FileLocation
		roots := make([]int64, 0, len(scope.Roots))
		for _, root := range scope.Roots {
			roots = append(roots, root.LocationID)
		}
		if err := tx.Where("file_id = ? AND location_id IN ? AND signature IS NOT NULL AND length(signature)>0", id, roots).
			Limit(1).Find(&original).Error; err != nil {
			return fmt.Errorf("read identical Location original failed, %w", err)
		}
		if original.FileID == 0 || !identicalInRoots(original, scope.Roots) {
			return nil
		}
		node = identicalNode{FileID: id, Component: id, Name: filepath.Base(original.Path),
			Kind: entity.FileKind_FILE_KIND_REGULAR, Original: &original}
	}

	// Retain unsigned Library originals for fingerprints without treating them as matching evidence.
	if scope.Source == IdenticalLibrary {
		var original FileLocation
		if err := tx.Where("file_id = ?", id).
			Limit(1).Find(&original).Error; err != nil {
			return fmt.Errorf("read identical Library original failed, %w", err)
		}
		if original.FileID != 0 {
			node.Original = &original
		}
	}
	if err := s.db.Create(&node).Error; err != nil {
		return fmt.Errorf("stage identical component member failed, %w", err)
	}
	if node.Original != nil && len(node.Original.Signature) > 0 {
		if err := s.stageComponentEdge(identicalEdge{FileID: id, Signature: node.Original.Signature}); err != nil {
			return err
		}
	}
	if scope.Source == IdenticalLocations {
		return nil
	}

	// An individual File can have long saved history, so use a keyset page for its versions.
	var after int64
	for {
		var versions []FileVersion
		if err := tx.Select("id,file_id,signature").Where("file_id = ? AND id > ?", id, after).
			Order("id").Limit(identicalBatch).Find(&versions).Error; err != nil {
			return fmt.Errorf("read identical FileVersions failed, %w", err)
		}
		if len(versions) == 0 {
			return nil
		}
		for _, version := range versions {
			if len(version.Signature) == 0 {
				continue
			}
			if err := s.stageComponentEdge(identicalEdge{FileID: id, VersionID: version.ID,
				Signature: version.Signature}); err != nil {
				return err
			}
		}
		after = versions[len(versions)-1].ID
	}
}

func (s *IdenticalSnapshot) stageComponentEdge(edge identicalEdge) error {
	// The edge is authoritative evidence; a duplicate frontier signature still needs only one visit.
	if err := s.db.Create(&edge).Error; err != nil {
		return fmt.Errorf("stage identical component evidence failed, %w", err)
	}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&identicalComponentSignature{Signature: edge.Signature}).Error; err != nil {
		return fmt.Errorf("enqueue identical component signature failed, %w", err)
	}
	return nil
}
