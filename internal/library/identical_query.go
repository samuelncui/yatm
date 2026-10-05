package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// IdenticalSource selects the independent catalog or physical-original matching domain.
type IdenticalSource string

const (
	IdenticalLibrary   IdenticalSource = "library"
	IdenticalLocations IdenticalSource = "locations"
	identicalBatch                     = 256
)

// IdenticalRoot selects a complete Location.
type IdenticalRoot struct {
	LocationID int64
}

// IdenticalScope is explicit and never starts collection or hashes physical content.
type IdenticalScope struct {
	Source IdenticalSource
	Roots  []IdenticalRoot
}

// IdenticalGroup describes a complete connected component, not a visible member page.
type IdenticalGroup struct {
	ID          string
	Name        string
	Count       int64
	Fingerprint string
}

// IdenticalEvidence identifies current content (VersionID zero) or a saved version.
type IdenticalEvidence struct {
	Signature []byte
	VersionID int64
}

// IdenticalMember retains the operation identity and up to 16 shared evidence examples.
// The snapshot retains complete evidence for grouping and mutation validation.
type IdenticalMember struct {
	FileID   int64
	Name     string
	Path     string
	Original *FileLocation
	Evidence []IdenticalEvidence
}

// IdenticalGroupPage is ordered by the smallest File ID in each component.
type IdenticalGroupPage struct {
	Groups     []IdenticalGroup
	NextCursor string
}

// IdenticalMemberPage is ordered by File ID within a complete component.
type IdenticalMemberPage struct {
	Members    []IdenticalMember
	NextCursor string
}

type identicalNode struct {
	FileID     int64 `gorm:"primaryKey;autoIncrement:false"`
	ParentID   int64 `gorm:"index"`
	Component  int64 `gorm:"index;index:idx_identical_frontier,priority:1"`
	Expanded   bool  `gorm:"index:idx_identical_frontier,priority:2"`
	Unresolved bool  `gorm:"index"`
	Name       string
	Size       *int64
	Path       string
	Kind       entity.FileKind
	Excluded   bool          `gorm:"index"`
	Original   *FileLocation `gorm:"serializer:json"`
}
type identicalEdge struct {
	FileID    int64  `gorm:"primaryKey;autoIncrement:false"`
	VersionID int64  `gorm:"primaryKey;autoIncrement:false"`
	Signature []byte `gorm:"index;type:varbinary(256)"`
}

// IdenticalSnapshot owns disposable disk staging. Call Close after consuming its bounded pages.
type IdenticalSnapshot struct {
	db             *gorm.DB
	directory      string
	scope          string
	Revision       string
	AllRows        int64
	VisibleRows    int64
	GroupCount     int64
	componentGroup *IdenticalGroup
	componentOnly  bool
}

// Close releases SQLite before removing its disposable files.
func (s *IdenticalSnapshot) Close() error {
	// Partial construction still owns its directory; a close failure cannot skip file cleanup.
	var result error
	if s.db != nil {
		db, err := s.db.DB()
		if err == nil {
			err = db.Close()
		}
		if err != nil {
			result = fmt.Errorf("close identical staging database failed, %w", err)
		}
	}
	if err := os.RemoveAll(s.directory); err != nil {
		result = errors.Join(result, fmt.Errorf("remove identical staging failed, %w", err))
	}
	return result
}

func (l *Library) newIdenticalSnapshot(
	ctx context.Context, scope IdenticalScope, prefix string,
) (result *IdenticalSnapshot, returnErr error) {
	// Both builders transfer the same SQLite/directory owner without sharing their query semantics.
	directory, err := l.identicalDirectory(prefix)
	if err != nil {
		return nil, fmt.Errorf("create identical staging failed, %w", err)
	}
	s := &IdenticalSnapshot{directory: directory}
	defer func() {
		if result == nil {
			returnErr = errors.Join(returnErr, s.Close())
		}
	}()
	db, err := resource.OpenSQLite(filepath.Join(directory, "groups.sqlite"))
	if err != nil {
		return nil, fmt.Errorf("open identical staging failed, %w", err)
	}
	s.db = db.WithContext(ctx)
	encoded, _ := json.Marshal(scope)
	s.scope = string(encoded)
	return s, nil
}

func normalizeIdenticalScope(scope IdenticalScope) (IdenticalScope, error) {
	// Canonical scope identity binds pagination and mutations to the same selection.
	if scope.Source != IdenticalLibrary && scope.Source != IdenticalLocations {
		return scope, fmt.Errorf("invalid identical-file source")
	}
	if scope.Source == IdenticalLibrary {
		if len(scope.Roots) > 0 {
			return scope, fmt.Errorf("Library scope cannot contain Location roots")
		}
		return scope, nil
	}
	if len(scope.Roots) == 0 || len(scope.Roots) > 1000 {
		return scope, fmt.Errorf("select between 1 and 1000 Location roots")
	}
	roots := append([]IdenticalRoot(nil), scope.Roots...)
	for i := range roots {
		if roots[i].LocationID <= 0 {
			return scope, fmt.Errorf("invalid Location ID")
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].LocationID < roots[j].LocationID })
	scope.Roots = nil
	for _, root := range roots {
		if len(scope.Roots) == 0 || scope.Roots[len(scope.Roots)-1].LocationID != root.LocationID {
			scope.Roots = append(scope.Roots, root)
		}
	}

	return scope, nil
}

// MatchesScope checks the canonical selection bound to this retained result.
func (s *IdenticalSnapshot) MatchesScope(scope IdenticalScope) (bool, error) {
	normalized, err := normalizeIdenticalScope(scope)
	if err != nil {
		return false, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return false, err
	}
	return s.scope == string(encoded), nil
}

// OpenIdenticalSnapshot builds complete components in a consistent metadata read without live I/O.
func (l *Library) OpenIdenticalSnapshot(
	ctx context.Context, scope IdenticalScope,
) (result *IdenticalSnapshot, returnErr error) {
	// The builder owns cleanup until it returns a complete retained result to its caller.
	scope, err := normalizeIdenticalScope(scope)
	if err != nil {
		return nil, err
	}
	s, err := l.newIdenticalSnapshot(ctx, scope, "yatm-identical-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			returnErr = errors.Join(returnErr, s.Close())
		}
	}()
	if err := s.db.AutoMigrate(
		&identicalNode{}, &identicalEdge{}, &identicalMinimum{}, &identicalComponentSignature{},
	); err != nil {
		return nil, err
	}

	// A consistent read prevents a group from mixing identities and signatures from different revisions.
	// Standalone reads release it before graph computation; failed staging is simply discarded.
	if err := l.readDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.collect(tx, scope)
	}); err != nil {
		return nil, err
	}
	if err := s.connect(); err != nil {
		return nil, err
	}
	s.Revision, err = s.fingerprint(0)
	if err != nil {
		return nil, err
	}
	if err := s.materializeRows(); err != nil {
		return nil, err
	}

	// Retained reads must outlive the request that built the snapshot.
	s.db = s.db.WithContext(context.Background())
	return s, nil
}

func (s *IdenticalSnapshot) groupID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid identical group ID")
	}
	return n, nil
}

// Groups returns a bounded page of complete group summaries.
func (s *IdenticalSnapshot) Groups(cursor string, limit int64) (IdenticalGroupPage, error) {
	// Bind stable cursors to the canonical requested scope.
	page := IdenticalGroupPage{}
	count, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return page, err
	}
	_, after, _, err := decodePageCursor(cursor, byte(6), s.scope+"\x00"+s.Revision)
	if err != nil {
		return page, err
	}
	if s.componentOnly {
		if s.componentGroup == nil {
			return page, nil
		}
		id, _ := s.groupID(s.componentGroup.ID)
		if id > after {
			page.Groups = append(page.Groups, *s.componentGroup)
		}
		return page, nil
	}
	var rows []identicalGroupIndex
	if err := s.db.Model(&identicalGroupIndex{}).
		Where("component > ?", after).
		Order("component").
		Limit(count + 1).
		Find(&rows).Error; err != nil {
		return page, err
	}
	more := len(rows) > count
	if more {
		rows = rows[:count]
	}
	for _, row := range rows {
		id := strconv.FormatInt(row.Component, 10)
		page.Groups = append(page.Groups, IdenticalGroup{ID: id, Name: row.Name, Count: row.Count, Fingerprint: row.Fingerprint})
	}
	if more {
		page.NextCursor = encodePageCursor(byte(6), s.scope+"\x00"+s.Revision, "", rows[len(rows)-1].Component)
	}
	return page, nil
}

// Fingerprint identifies the complete group by its members, signatures and originals.
// Auxiliary metadata and unrelated groups do not invalidate a group operation.
func (s *IdenticalSnapshot) Fingerprint(groupID string) (string, error) {
	// Stream deterministic content evidence through a bounded hash.
	id, err := s.groupID(groupID)
	if err != nil {
		return "", err
	}
	return s.fingerprint(id)
}

func (s *IdenticalSnapshot) fingerprint(id int64) (string, error) {
	hash := sha256.New()
	hash.Write([]byte(s.scope))
	request := s.db.Table("identical_nodes AS n").
		Select("n.file_id,n.original,e.version_id,e.signature").
		Joins("JOIN identical_edges AS e ON e.file_id=n.file_id").
		Order("n.file_id,e.version_id")
	if id != 0 {
		request = request.Where("n.component = ?", id)
	}
	rows, err := request.Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var fileID, versionID int64
		var original *string
		var signature []byte
		if err := rows.Scan(&fileID, &original, &versionID, &signature); err != nil {
			return "", err
		}
		var identity struct {
			LocationID int64  `json:"location_id"`
			Path       string `json:"path"`
		}
		if original != nil {
			if err := json.Unmarshal([]byte(*original), &identity); err != nil {
				return "", err
			}
		}
		data, _ := json.Marshal([]any{fileID, identity, versionID, signature})
		hash.Write(data)
		found = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !found && id != 0 {
		return "", fmt.Errorf("identical group no longer exists")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Members loads only the requested member page and its matching evidence.
func (s *IdenticalSnapshot) Members(groupID, cursor string, limit int64) (IdenticalMemberPage, error) {
	// Query File identities first so evidence cardinality cannot truncate a member page.
	page := IdenticalMemberPage{}
	id, err := s.groupID(groupID)
	if err != nil {
		return page, err
	}
	count, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return page, err
	}
	_, after, _, err := decodePageCursor(cursor, byte(7), s.scope+"\x00"+s.Revision+"\x00"+groupID)
	if err != nil {
		return page, err
	}
	var nodes []identicalNode
	if err := s.db.Where("component = ? AND file_id > ?", id, after).
		Order("file_id").
		Limit(count + 1).Find(&nodes).Error; err != nil {
		return page, err
	}
	more := len(nodes) > count
	if more {
		nodes = nodes[:count]
	}
	page.Members, err = s.membersForNodes(nodes)
	if err != nil {
		return page, err
	}
	if more {
		page.NextCursor = encodePageCursor(byte(7), s.scope+"\x00"+s.Revision+"\x00"+groupID, "", nodes[len(nodes)-1].FileID)
	}
	return page, nil
}

// MembersByIDs hydrates only indexed row members and preserves requested order.
func (s *IdenticalSnapshot) MembersByIDs(ids []int64) ([]IdenticalMember, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var nodes []identicalNode
	if err := s.db.Where("file_id IN ?", ids).Find(&nodes).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]identicalNode, len(nodes))
	for _, node := range nodes {
		byID[node.FileID] = node
	}
	ordered := make([]identicalNode, 0, len(ids))
	for _, id := range ids {
		if node, ok := byID[id]; ok {
			ordered = append(ordered, node)
		}
	}
	return s.membersForNodes(ordered)
}

func (s *IdenticalSnapshot) membersForNodes(nodes []identicalNode) ([]IdenticalMember, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	// Batch the page's evidence; a presentation sample never determines mutation membership.
	ids := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.FileID)
	}
	var edges []identicalEdge
	ranked := s.db.Model(&identicalEdge{}).
		Select("file_id,version_id,signature,ROW_NUMBER() OVER (PARTITION BY file_id ORDER BY version_id) AS evidence_rank").
		Where("file_id IN ?", ids).
		Where("EXISTS (?)", s.db.Table("identical_edges AS peer").
			Select("1").
			Where("peer.signature = identical_edges.signature AND peer.file_id <> identical_edges.file_id"))
	if err := s.db.Table("(?) AS evidence", ranked).
		Select("file_id,version_id,signature").
		Where("evidence_rank <= ?", 16).
		Order("file_id,version_id").
		Scan(&edges).Error; err != nil {
		return nil, err
	}
	evidence := make(map[int64][]IdenticalEvidence, len(nodes))
	for _, edge := range edges {
		evidence[edge.FileID] = append(evidence[edge.FileID], IdenticalEvidence{Signature: edge.Signature, VersionID: edge.VersionID})
	}
	members := make([]IdenticalMember, 0, len(nodes))
	for _, node := range nodes {
		members = append(members, IdenticalMember{FileID: node.FileID, Name: node.Name, Path: node.Path,
			Original: node.Original, Evidence: evidence[node.FileID]})
	}
	return members, nil
}

// WalkMembers visits authoritative members in bounded pages; callbacks must not retain every page.
func (s *IdenticalSnapshot) WalkMembers(groupID string, fn func([]IdenticalMember) error) error {
	cursor := ""
	for {
		page, err := s.Members(groupID, cursor, identicalBatch)
		if err != nil {
			return err
		}
		if len(page.Members) > 0 {
			if err := fn(page.Members); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// Group resolves the complete group independently of member pagination.
func (s *IdenticalSnapshot) Group(groupID string) (*IdenticalGroup, error) {
	// Count the authoritative component before accepting its fingerprint for mutation.
	id, err := s.groupID(groupID)
	if err != nil {
		return nil, err
	}
	if s.componentOnly {
		if s.componentGroup == nil || s.componentGroup.ID != groupID {
			return nil, fmt.Errorf("identical group no longer exists")
		}
		group := *s.componentGroup
		return &group, nil
	}
	var row identicalGroupIndex
	if err := s.db.Where("component = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &IdenticalGroup{ID: groupID, Name: row.Name, Count: row.Count, Fingerprint: row.Fingerprint}, nil
}

// HasMember checks exact authoritative membership without loading a component's member list.
func (s *IdenticalSnapshot) HasMember(groupID string, fileID int64) (bool, error) {
	// Use both indexed identities so a large group needs no member hydration.
	id, err := s.groupID(groupID)
	if err != nil {
		return false, err
	}
	var count int64
	if err := s.db.Model(&identicalNode{}).Where("component = ? AND file_id = ?", id, fileID).Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}
