package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrOnlineBusy       = errors.New("Location or Library maintenance is busy")
	ErrOnlineConflict   = errors.New("Location changed; reload and retry")
	ErrOnlineUnverified = errors.New("Location binding is not verified for access")
)

type Location struct {
	ID                 int64                    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name               string                   `gorm:"type:varchar(256);not null" json:"name"`
	ExecutorID         string                   `gorm:"type:varchar(128);not null;uniqueIndex:idx_online_sources_root,priority:1" json:"executor_id"`
	RootPath           string                   `gorm:"type:varchar(4096);not null;uniqueIndex:idx_online_sources_root,priority:2,length:600" json:"root_path"`
	Exclusions         *entity.OnlineExclusions `gorm:"type:blob" json:"ignore"`
	WriteTrackingUUID  bool                     `json:"write_tracking_uuid"`
	RestoreTarget      bool                     `json:"restore_target"`
	Binding            entity.OnlineBinding     `gorm:"not null" json:"binding"`
	BindingToken       string                   `gorm:"type:varchar(36);not null" json:"binding_token"`
	Revision           int64                    `gorm:"not null" json:"revision"`
	CreatedAt          int64                    `gorm:"autoCreateTime:milli" json:"created_at_ms"`
	UpdatedAt          int64                    `gorm:"autoUpdateTime:milli" json:"updated_at_ms"`
	LastSyncAt         int64                    `json:"last_sync_at_ms"`
	LastSyncJobID      int64                    `json:"last_sync_job_id"`
	LastJobID          int64                    `json:"last_job_id"`
	RequiredExclusions []string                 `gorm:"-" json:"-"`
	AccessAllowed      func(string, bool) bool  `gorm:"-" json:"-"`
	matcher            *ignore.Matcher
}

func (s *Location) BeforeCreate(*gorm.DB) error {
	if s.BindingToken == "" {
		s.BindingToken = uuid.NewString()
	}
	return nil
}

func (s *Location) CatalogEntity() *entity.Location {
	return &entity.Location{Id: s.ID, Name: s.Name, ExecutorId: s.ExecutorID, RootPath: s.RootPath,
		Ignore: &entity.IgnoreRules{Format: "gitignore", Text: s.Exclusions.GetText()}, WriteTrackingUuid: s.WriteTrackingUUID,
		Binding: s.Binding, Revision: s.Revision, CreatedAtMs: s.CreatedAt, UpdatedAtMs: s.UpdatedAt,
		LastSyncAtMs: s.LastSyncAt, LastSyncJobId: s.LastSyncJobID, LastJobId: s.LastJobID,
		BindingToken: s.BindingToken, RestoreTarget: s.RestoreTarget}
}

// BeforeSave invalidates cursors through the same GORM path as every catalog mutation.
func (s *Location) BeforeSave(*gorm.DB) error {
	s.Revision = maxOnlineRevision(s.Revision, time.Now().UnixNano())
	s.matcher = compileOnlineIgnore(s.Exclusions)
	return nil
}

func (s *Location) AfterFind(*gorm.DB) error {
	if _, err := NormalizeOnlineExclusions(s.Exclusions); err != nil {
		return err
	}
	s.matcher = compileOnlineIgnore(s.Exclusions)
	return nil
}

func (s *Location) Excluded(value string, directory bool) bool {
	if s.AccessAllowed != nil && !s.AccessAllowed(value, directory) {
		return true
	}
	for _, required := range s.RequiredExclusions {
		if value == required || strings.HasPrefix(value, required+"/") {
			return true
		}
	}
	if s.matcher != nil {
		return s.matcher.Match(value, directory)
	}
	return compileOnlineIgnore(s.Exclusions).Match(value, directory)
}

func compileOnlineIgnore(value *entity.OnlineExclusions) *ignore.Matcher {
	return ignore.Compile(value.GetText())
}

func maxOnlineRevision(previous, now int64) int64 {
	if now > previous {
		return now
	}
	return previous + 1
}

// RecordOnlineAttempt exposes the most recent Job, including failures that retain the old index.
// The caller holds the source operation gate.
func (l *Library) RecordOnlineAttempt(ctx context.Context, id, jobID int64) (*Location, error) {
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

// OnlineGate protects operation admission, never a physical device or filesystem writer.
type onlineGate struct {
	sync.Mutex
	maintenance bool
	readers     int
	sources     map[int64]struct{}
}

// UseOnlineSource excludes another operation on the same source until release.
func (l *Library) UseOnlineSource(id int64) (func(), error) {
	// Atomically reserve the source and a shared maintenance slot.
	l.online.Lock()
	defer l.online.Unlock()
	if l.online.maintenance {
		return nil, ErrOnlineBusy
	}
	if _, busy := l.online.sources[id]; busy {
		return nil, ErrOnlineBusy
	}
	l.online.sources[id] = struct{}{}
	l.online.readers++

	// The release belongs to the admitted end-to-end operation.
	return func() {
		l.online.Lock()
		defer l.online.Unlock()
		delete(l.online.sources, id)
		l.online.readers--
	}, nil
}

// UseOnlineRead protects identity-sensitive work from import without reserving a source.
func (l *Library) UseOnlineRead() (func(), error) {
	// Reject maintenance instead of waiting behind a replacement transaction.
	l.online.Lock()
	defer l.online.Unlock()
	if l.online.maintenance {
		return nil, ErrOnlineBusy
	}
	l.online.readers++

	// Ordinary readers may coexist with one another and source configuration work.
	return func() {
		l.online.Lock()
		defer l.online.Unlock()
		l.online.readers--
	}, nil
}

func (l *Library) maintainOnline() (func(), error) {
	// Admit maintenance only when no identity-sensitive operation is active.
	l.online.Lock()
	defer l.online.Unlock()
	if l.online.maintenance || l.online.readers != 0 {
		return nil, ErrOnlineBusy
	}
	l.online.maintenance = true

	// Restore admission even when import validation or publication fails.
	return func() {
		l.online.Lock()
		defer l.online.Unlock()
		l.online.maintenance = false
	}, nil
}

// NormalizeOnlineExclusions validates configured gitignore text without rewriting its rules.
func NormalizeOnlineExclusions(value *entity.OnlineExclusions) (*entity.OnlineExclusions, error) {
	// Absent configuration means no user exclusions, not another encoding.
	if value == nil {
		return &entity.OnlineExclusions{Format: "gitignore"}, nil
	}
	if value.Format != "gitignore" {
		return nil, fmt.Errorf("unsupported Ignore format %q", value.Format)
	}
	if !utf8.ValidString(value.Text) || strings.ContainsRune(value.Text, 0) {
		return nil, fmt.Errorf("Ignore text must be valid UTF-8 without NUL")
	}
	return proto.Clone(value).(*entity.OnlineExclusions), nil
}

func OnlineExcluded(exclusions *entity.OnlineExclusions, value string) bool {
	return compileOnlineIgnore(exclusions).Match(value, false)
}

func validateOnlineSource(s *Location) error {
	// A display name is also one safe logical import-directory component.
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 200 || !utf8.ValidString(s.Name) {
		return fmt.Errorf("invalid online source name %q", s.Name)
	}
	if s.Name == "." || s.Name == ".." || strings.ContainsAny(s.Name, "/\\\x00") {
		return fmt.Errorf("invalid online source name %q", s.Name)
	}

	// Filesystem admission belongs to the Executor; persistence still rejects malformed bindings.
	if s.ExecutorID == "" || !filepath.IsAbs(s.RootPath) || filepath.Clean(s.RootPath) != s.RootPath || strings.ContainsRune(s.RootPath, 0) {
		return fmt.Errorf("invalid online source binding %q", s.RootPath)
	}
	exclusions, err := NormalizeOnlineExclusions(s.Exclusions)
	if err != nil {
		return err
	}
	s.Exclusions = exclusions
	return nil
}

func (l *Library) CreateOnlineSource(ctx context.Context, s *Location) error {
	// Serialize registration against maintenance while database uniqueness resolves root conflicts.
	release, err := l.UseOnlineSource(0)
	if err != nil {
		return err
	}
	defer release()
	if err := validateOnlineSource(s); err != nil {
		return err
	}

	// Registration confirms the path, but does not claim a complete directory observation.
	s.ID = 0
	s.Binding = entity.OnlineBinding_CONFIRMED
	s.BindingToken = uuid.NewString()
	if err := l.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("register online source failed, %w", err)
	}
	return nil
}

func (l *Library) GetOnlineSource(ctx context.Context, id int64) (*Location, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid online source ID %d", id)
	}
	var source Location
	if err := l.db.WithContext(ctx).First(&source, id).Error; err != nil {
		return nil, fmt.Errorf("get online source failed, %w", err)
	}
	return &source, nil
}

func (l *Library) ListOnlineSources(ctx context.Context, after int64, limit int, restoreTarget *bool) ([]*Location, bool, error) {
	return l.ListLocations(ctx, LocationListFilter{AfterID: after, Limit: limit, RestoreTarget: restoreTarget})
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
		return nil, false, fmt.Errorf("invalid online source page size %d", filter.Limit)
	}
	if filter.AfterID < 0 {
		return nil, false, fmt.Errorf("Location page after_id must not be negative")
	}

	// Apply literal search and preference filters before paging, including the sentinel row.
	query := l.db.WithContext(ctx).Where("id > ?", filter.AfterID)
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

func (l *Library) UpdateOnlineSource(ctx context.Context, s *Location, confirm bool) (*Location, error) {
	// Admission and expected revision prevent configuration changes from crossing a sync.
	release, err := l.UseOnlineSource(s.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := validateOnlineSource(s); err != nil {
		return nil, err
	}

	// Keep the previous index but invalidate its use whenever the effective binding changes.
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored Location
		if err := tx.First(&stored, s.ID).Error; err != nil {
			return err
		}
		if stored.Revision != s.Revision {
			return ErrOnlineConflict
		}
		changed := stored.RootPath != s.RootPath || !proto.Equal(stored.Exclusions, s.Exclusions)
		stored.Name, stored.RootPath, stored.Exclusions = s.Name, s.RootPath, s.Exclusions
		stored.WriteTrackingUUID = s.WriteTrackingUUID
		stored.RestoreTarget = s.RestoreTarget
		if confirm {
			stored.ExecutorID = s.ExecutorID
		}
		if changed && stored.Binding != entity.OnlineBinding_UNCONFIRMED {
			stored.Binding = entity.OnlineBinding_CONFIRMED
		}
		if confirm {
			stored.Binding = entity.OnlineBinding_CONFIRMED
		}
		if changed || confirm {
			stored.BindingToken = uuid.NewString()
			if err := tx.Where("location_id = ?", stored.ID).Delete(&FileTrackingKey{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Save(&stored).Error; err != nil {
			return err
		}
		*s = stored
		return nil
	})
	return s, err
}

func (l *Library) DeleteOnlineSource(ctx context.Context, id, revision int64) error {
	// Deletion removes catalog references only, under the same source operation boundary.
	release, err := l.UseOnlineSource(id)
	if err != nil {
		return err
	}
	defer release()

	// Check the displayed revision and delete both kinds of source rows atomically.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored Location
		if err := tx.First(&stored, id).Error; err != nil {
			return err
		}
		if stored.Revision != revision {
			return ErrOnlineConflict
		}
		if err := tx.Where("location_id = ?", id).Delete(&FileLocation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("location_id = ?", id).Delete(&FileTrackingKey{}).Error; err != nil {
			return err
		}
		return tx.Delete(&stored).Error
	})
}
