package settings

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalid = errors.New("invalid Settings")
	ErrStored  = errors.New("stored Settings are unusable")
)

// Stored is one effective Settings value and whether the operator saved that group.
type Stored[T proto.Message] struct {
	Value   T
	Present bool
}

// PreviewDefinition supplies the Preview domain's defaults and validation without exposing
// storage concerns to the Preview module.
type PreviewDefinition struct {
	Default  func() (*entity.PreviewSettings, error)
	Validate func(*entity.PreviewSettings) error
}

type definition[T proto.Message] struct {
	key      string
	defaults func() (T, error)
	validate func(T) error
}

// Group stores and validates one independently typed Settings value.
type Group[T proto.Message] struct {
	db         *gorm.DB
	reader     *gorm.DB
	definition definition[T]
}

// Module owns the three persisted Settings groups.
type Module struct {
	db                *gorm.DB
	previewDefinition PreviewDefinition

	Library Group[*entity.LibrarySettings]
	Preview Group[*entity.PreviewSettings]
	Job     Group[*entity.JobSettings]
}

type row struct {
	Key         string `gorm:"primaryKey;type:varchar(128)"`
	Value       []byte `gorm:"type:blob;not null"`
	CreatedAtNS int64  `gorm:"autoCreateTime:nano"`
	UpdatedAtNS int64  `gorm:"autoUpdateTime:nano"`
}

func (row) TableName() string { return "settings" }

// New constructs the concrete Settings module over the Catalog database.
func New(db *gorm.DB, preview PreviewDefinition) *Module {
	module := &Module{db: db, previewDefinition: preview}
	module.Library = newGroup(db, definition[*entity.LibrarySettings]{
		key: "library",
		defaults: func() (*entity.LibrarySettings, error) {
			return &entity.LibrarySettings{IncludeUnbackedFiles: true, ConfirmRemove: true}, nil
		},
	})
	module.Preview = newGroup(db, definition[*entity.PreviewSettings]{
		key: "preview",
		defaults: func() (*entity.PreviewSettings, error) {
			if preview.Default == nil {
				return nil, fmt.Errorf("Preview Settings defaults are not configured")
			}
			return preview.Default()
		},
		validate: preview.Validate,
	})
	module.Job = newGroup(db, definition[*entity.JobSettings]{
		key: "jobs",
		defaults: func() (*entity.JobSettings, error) {
			return &entity.JobSettings{Execution: DefaultJobExecution()}, nil
		},
		validate: ValidateJob,
	})
	return module
}

func newGroup[T proto.Message](db *gorm.DB, definition definition[T]) Group[T] {
	return Group[T]{db: db, definition: definition}
}

// WithDB binds the same typed definitions to a transaction-scoped database handle.
func (m *Module) WithDB(db *gorm.DB) *Module {
	return New(db, m.previewDefinition)
}

// NewWithCatalog keeps committed Settings reads off the Catalog writer connection.
func NewWithCatalog(write, read *gorm.DB, preview PreviewDefinition) *Module {
	module := New(write, preview)
	module.Library.reader, module.Preview.reader, module.Job.reader = read, read, read
	return module
}

// AutoMigrate installs the Settings row store after the Catalog format has been admitted.
func (m *Module) AutoMigrate() error {
	if err := m.db.AutoMigrate(&row{}); err != nil {
		return fmt.Errorf("migrate Settings storage failed, %w", err)
	}
	return nil
}

// Current returns one independent effective value without persistence metadata.
func (g Group[T]) Current(ctx context.Context) (T, error) {
	stored, err := g.Read(ctx)
	return stored.Value, err
}

// Read returns one independent effective value and whether a stored replacement exists.
func (g Group[T]) Read(ctx context.Context) (Stored[T], error) {
	// A missing row resolves to validated defaults without turning a read into a write.
	var stored row
	reader := g.reader
	if reader == nil {
		reader = g.db
	}
	result := reader.WithContext(ctx).Where("key = ?", g.definition.key).Limit(1).Find(&stored)
	if result.Error != nil {
		return Stored[T]{}, fmt.Errorf("read %s Settings failed, %w", g.definition.key, result.Error)
	}
	if result.RowsAffected == 0 {
		value, defaultErr := g.definition.defaults()
		if defaultErr != nil {
			return Stored[T]{}, fmt.Errorf("build %s Settings defaults failed, %w", g.definition.key, defaultErr)
		}
		if validateErr := g.validate(value); validateErr != nil {
			return Stored[T]{}, fmt.Errorf("%w, group=%s defaults: %v", ErrStored, g.definition.key, validateErr)
		}
		return Stored[T]{Value: clone(value)}, nil
	}

	// Stored values are complete authoritative documents; malformed values never become defaults.
	value, err := g.definition.defaults()
	if err != nil {
		return Stored[T]{}, fmt.Errorf("build %s Settings value failed, %w", g.definition.key, err)
	}
	if err := protojson.Unmarshal(stored.Value, value); err != nil {
		return Stored[T]{}, fmt.Errorf("%w, group=%s: %v", ErrStored, g.definition.key, err)
	}
	if err := g.validate(value); err != nil {
		return Stored[T]{}, fmt.Errorf("%w, group=%s: %v", ErrStored, g.definition.key, err)
	}
	return Stored[T]{Value: clone(value), Present: true}, nil
}

// Save validates and atomically replaces one complete Settings group.
func (g Group[T]) Save(ctx context.Context, value T) (T, error) {
	// Validate an isolated value before replacing the group.
	if isNil(value) {
		var zero T
		return zero, fmt.Errorf("%w, group=%s is missing", ErrInvalid, g.definition.key)
	}
	next := clone(value)
	if err := g.validate(next); err != nil {
		var zero T
		return zero, fmt.Errorf("%w, group=%s: %v", ErrInvalid, g.definition.key, err)
	}
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(next)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("encode %s Settings failed, %w", g.definition.key, err)
	}

	// One upsert publishes the complete group; this local single-operator surface is last-write-wins.
	result := g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at_ns"}),
	}).Create(&row{Key: g.definition.key, Value: data})
	if result.Error != nil {
		var zero T
		return zero, fmt.Errorf("save %s Settings failed, %w", g.definition.key, result.Error)
	}
	return clone(next), nil
}

func (g Group[T]) validate(value T) error {
	if isNil(value) {
		return fmt.Errorf("value is missing")
	}
	if g.definition.validate != nil {
		return g.definition.validate(value)
	}
	return nil
}

func isNil[T proto.Message](value T) bool {
	ref := reflect.ValueOf(value)
	return !ref.IsValid() || ref.IsNil()
}

func clone[T proto.Message](value T) T {
	return proto.Clone(value).(T)
}

// DefaultJobExecution returns the pipeline limits used before the operator saves the group.
func DefaultJobExecution() *entity.JobExecutionSettings {
	return &entity.JobExecutionSettings{
		ReadBatch: 256, ReadBufferMax: 4096, WriteBufferMax: 4096, WriteBatchSize: 256, FlushIntervalMs: 1000,
	}
}

// ValidateJob rejects limits that an attempt cannot run with.
func ValidateJob(value *entity.JobSettings) error {
	settings := value.GetExecution()
	if settings.GetReadBatch() < 1 {
		return fmt.Errorf("Job execution read batch must be at least 1")
	}
	if settings.GetReadBufferMax() < 1 || settings.GetReadBufferMax() > 1_000_000 {
		return fmt.Errorf("Job execution read buffer must be between 1 and 1000000")
	}
	if settings.GetWriteBufferMax() < 1 || settings.GetWriteBufferMax() > 1_000_000 {
		return fmt.Errorf("Job execution write buffer must be between 1 and 1000000")
	}
	if settings.GetWriteBatchSize() < 1 {
		return fmt.Errorf("Job execution write batch must be at least 1")
	}
	if settings.GetWriteBatchSize() > settings.GetWriteBufferMax() {
		return fmt.Errorf("Job execution write batch must not exceed the write buffer")
	}
	if settings.GetFlushIntervalMs() < 100 {
		return fmt.Errorf("Job execution flush interval must be at least 100 milliseconds")
	}
	return nil
}
