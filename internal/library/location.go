package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrLocationBusy       = errors.New("Library maintenance conflicts with another operation")
	ErrLocationConflict   = errors.New("Location changed; reload and retry")
	ErrLocationUnverified = errors.New("Location root is not usable for access")
)

type Location struct {
	ID            int64                   `gorm:"primaryKey;autoIncrement" json:"id"`
	Name          string                  `gorm:"type:varchar(256);not null" json:"name"`
	ExecutorID    string                  `gorm:"type:varchar(128);not null;uniqueIndex:idx_locations_root,priority:1" json:"executor_id"`
	RootPath      string                  `gorm:"type:varchar(4096);not null;uniqueIndex:idx_locations_root,priority:2,length:600" json:"root_path"`
	Config        *entity.LocationConfig  `gorm:"serializer:json;type:text" json:"config"`
	RestoreTarget bool                    `json:"restore_target"`
	Revision      int64                   `gorm:"not null" json:"revision"`
	CreatedAtNS   int64                   `gorm:"autoCreateTime:nano" json:"created_at_ns,string"`
	UpdatedAtNS   int64                   `gorm:"autoUpdateTime:nano" json:"updated_at_ns,string"`
	LastSyncAtNS  int64                   `json:"last_sync_at_ns,string"`
	LastSyncJobID int64                   `json:"last_sync_job_id"`
	LastJobID     int64                   `json:"last_job_id"`
	AccessAllowed func(string, bool) bool `gorm:"-" json:"-"`
	matcher       *ignore.Matcher
}

func (s *Location) CatalogEntity() *entity.Location {
	config := new(entity.LocationConfig)
	if s.Config != nil {
		config = proto.Clone(s.Config).(*entity.LocationConfig)
	}
	return &entity.Location{Id: s.ID, Name: s.Name, ExecutorId: s.ExecutorID, RootPath: s.RootPath,
		Config:   config,
		Revision: s.Revision, CreatedAtNs: s.CreatedAtNS, UpdatedAtNs: s.UpdatedAtNS,
		LastSyncAtNs: s.LastSyncAtNS, LastSyncJobId: s.LastSyncJobID, LastJobId: s.LastJobID,
		RestoreTarget: s.RestoreTarget}
}

// BeforeSave invalidates cursors through the same GORM path as every catalog mutation.
func (s *Location) BeforeSave(*gorm.DB) error {
	s.Revision = maxLocationRevision(s.Revision, time.Now().UnixNano())
	s.matcher = compileLocationIgnore(s.Config.GetIgnore())
	return nil
}

func (s *Location) AfterFind(*gorm.DB) error {
	if s.Config == nil {
		return fmt.Errorf("Location configuration is missing; convert this pre-v1 catalog before use")
	}
	if _, err := NormalizeIgnoreRules(s.Config.Ignore); err != nil {
		return err
	}
	s.matcher = compileLocationIgnore(s.Config.Ignore)
	return nil
}

// AccessExcluded applies administrator boundaries and YATM storage resources without user Ignore.
func (s *Location) AccessExcluded(value string, directory bool) bool {
	if s.AccessAllowed != nil && !s.AccessAllowed(value, directory) {
		return true
	}
	return false
}

// Excluded applies one decision for browsing and collection, including user Ignore.
func (s *Location) Excluded(value string, directory bool) bool {
	return s.AccessExcluded(value, directory) || s.Ignored(value, directory)
}

// Ignored applies this Location's user Ignore rules to one relative path.
func (s *Location) Ignored(value string, directory bool) bool {
	return s.ignoreMatcher().Match(value, directory)
}

// IgnoreScope resolves the user Ignore decision of one directory once; every entry of
// that directory is then decided by name, independently of path depth.
func (s *Location) IgnoreScope(relative string) *ignore.Scope {
	return s.ignoreMatcher().Scope(relative)
}

// ExcludedIn applies one directory's Ignore scope plus the administrator boundary to a
// single enumerated entry. It is the per-entry form of Excluded for listings and walks.
func (s *Location) ExcludedIn(scope *ignore.Scope, relative, name string, directory bool) bool {
	return s.AccessExcluded(relative, directory) || scope.Ignores(name, directory)
}

// ignoreMatcher returns the compiled user Ignore matcher, compiling on demand for a
// Location that was constructed without a catalog read.
func (s *Location) ignoreMatcher() *ignore.Matcher {
	if s.matcher != nil {
		return s.matcher
	}
	return compileLocationIgnore(s.Config.GetIgnore())
}

func compileLocationIgnore(value *entity.IgnoreRules) *ignore.Matcher {
	return ignore.Compile(value.GetText())
}

func maxLocationRevision(previous, now int64) int64 {
	if now > previous {
		return now
	}
	return previous + 1
}

// RecordLocationAttempt exposes the most recent Job, including failures that retain the old index.
// The caller holds the source operation gate.
func (l *Library) RecordLocationAttempt(ctx context.Context, id, jobID int64) (*Location, error) {
	var source Location
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&source, id).Error; err != nil {
			return err
		}
		source.LastJobID = jobID
		return tx.Save(&source).Error
	})
	return &source, err
}

// NormalizeIgnoreRules validates configured gitignore text without rewriting its rules.
func NormalizeIgnoreRules(value *entity.IgnoreRules) (*entity.IgnoreRules, error) {
	// Absent configuration means no user exclusions, not another encoding.
	if value == nil {
		return &entity.IgnoreRules{Format: "gitignore"}, nil
	}
	if value.Format != "gitignore" {
		return nil, fmt.Errorf("unsupported Ignore format %q", value.Format)
	}
	if !utf8.ValidString(value.Text) || strings.ContainsRune(value.Text, 0) {
		return nil, fmt.Errorf("Ignore text must be valid UTF-8 without NUL")
	}
	return proto.Clone(value).(*entity.IgnoreRules), nil
}

func validateLocation(s *Location) error {
	// A display name is also one safe logical import-directory component.
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 200 || !utf8.ValidString(s.Name) {
		return fmt.Errorf("invalid Location name %q", s.Name)
	}
	if s.Name == "." || s.Name == ".." || strings.ContainsAny(s.Name, "/\\\x00") {
		return fmt.Errorf("invalid Location name %q", s.Name)
	}

	// Filesystem admission belongs to the Executor; persistence still rejects malformed paths.
	if s.ExecutorID == "" || !filepath.IsAbs(s.RootPath) || filepath.Clean(s.RootPath) != s.RootPath || strings.ContainsRune(s.RootPath, 0) {
		return fmt.Errorf("invalid Location root path %q", s.RootPath)
	}
	if s.Config == nil {
		s.Config = new(entity.LocationConfig)
	}
	exclusions, err := NormalizeIgnoreRules(s.Config.Ignore)
	if err != nil {
		return err
	}
	s.Config.Ignore = exclusions
	return nil
}

func (l *Library) CreateLocation(ctx context.Context, s *Location) error {
	// Serialize registration against maintenance while database uniqueness resolves root conflicts.
	if err := validateLocation(s); err != nil {
		return err
	}

	// New registrations start from the default YATM entry rules; the text stays editable.
	s.Config.Ignore = WithDefaultLocationIgnore(s.Config.Ignore)

	// Registration records the path, but does not claim a complete directory observation.
	s.ID = 0
	if err := l.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("register Location failed, %w", err)
	}
	return nil
}

func (l *Library) GetLocation(ctx context.Context, id int64) (*Location, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid Location ID %d", id)
	}
	var source Location
	if err := l.readDB().WithContext(ctx).First(&source, id).Error; err != nil {
		return nil, fmt.Errorf("get Location failed, %w", err)
	}
	return &source, nil
}

// LocationListFilter constrains an ID-ordered page of registered directories.
type LocationListFilter struct {
	AfterID       int64
	Limit         int
	RestoreTarget *bool
	Query         string
}

// ListLocations searches registered metadata without accessing the filesystem.
func (l *Library) ListLocations(ctx context.Context, filter LocationListFilter) ([]*Location, bool, error) {
	// Reject invalid bounds before building the catalog query.
	if filter.Limit <= 0 || filter.Limit > 1000 {
		return nil, false, fmt.Errorf("invalid Location page size %d", filter.Limit)
	}
	if filter.AfterID < 0 {
		return nil, false, fmt.Errorf("Location page after_id must not be negative")
	}

	// Apply literal search and preference filters before paging, including the sentinel row.
	query := l.readDB().WithContext(ctx).Where("id > ?", filter.AfterID)
	if filter.RestoreTarget != nil {
		query = query.Where("restore_target = ?", *filter.RestoreTarget)
	}
	if text := strings.TrimSpace(filter.Query); text != "" {
		query = query.Where(clause.Or(containsExpression("name", text), containsExpression("root_path", text)))
	}
	var rows []*Location
	if err := query.Order("id").Limit(filter.Limit + 1).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("list Locations failed, %w", err)
	}

	// Catalog ordering is identity-based; updates do not reorder a source.
	more := len(rows) > filter.Limit
	if more {
		rows = rows[:filter.Limit]
	}
	return rows, more, nil
}

func (l *Library) UpdateLocation(ctx context.Context, s *Location) (*Location, error) {
	// Validate the complete replacement before changing the registered Location.
	if err := validateLocation(s); err != nil {
		return nil, err
	}

	// A configuration change keeps every recorded association and its tracking evidence. Evidence
	// only ever confirms: a listing, a Preview read or a Scan match that observes a path must still
	// agree with it, so an entry whose root moved away simply stops confirming instead of being
	// assumed. Dropping it instead made an Ignore edit silently unconfirm every File of the
	// Location until an operator scanned the whole tree again.
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored Location
		if err := tx.First(&stored, s.ID).Error; err != nil {
			return err
		}
		stored.Name, stored.RootPath, stored.Config = s.Name, s.RootPath, s.Config
		stored.RestoreTarget = s.RestoreTarget
		if err := tx.Save(&stored).Error; err != nil {
			return err
		}
		*s = stored
		return nil
	})
	return s, err
}

// LocationDeleteResult reports what one unregistration removes, or would remove under a dry run.
type LocationDeleteResult struct {
	Locations int64
	Originals int64
}

// DeleteLocation removes catalog references only, under the same source operation boundary.
// A dry run counts the same rows without deleting them.
func (l *Library) DeleteLocation(ctx context.Context, id int64, dryRun bool) (*LocationDeleteResult, error) {
	result := new(LocationDeleteResult)
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored Location
		if err := tx.First(&stored, id).Error; err != nil {
			return err
		}
		result.Locations = 1
		if err := tx.Model(&FileLocation{}).Where("location_id = ?", id).Count(&result.Originals).Error; err != nil {
			return err
		}
		if dryRun {
			return nil
		}
		if err := tx.Where("location_id = ?", id).Delete(&FileLocation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("location_id = ?", id).Delete(&FileTrackingKey{}).Error; err != nil {
			return err
		}
		return tx.Delete(&stored).Error
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
