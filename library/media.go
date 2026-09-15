package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ModelMedia = new(Media)

const maxMediaPageSize = 1000

// Media is the common Library identity for Tape and mounted offline Volumes.
type Media struct {
	ID            int64                `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	Kind          entity.MediaKind     `gorm:"not null;index:idx_media_kind_identity,unique,priority:1" json:"kind,omitempty"`
	Identity      string               `gorm:"type:varchar(128);not null;index:idx_media_kind_identity,unique,priority:2" json:"identity,omitempty"`
	Name          string               `gorm:"type:varchar(256)" json:"name,omitempty"`
	Profile       *entity.MediaProfile `gorm:"type:blob;not null" json:"profile,omitempty"`
	CreateTime    time.Time            `json:"create_time,omitempty"`
	DestroyTime   *time.Time           `json:"destroy_time,omitempty"`
	CapacityBytes int64                `json:"capacity_bytes,omitempty"`
	WrittenBytes  int64                `json:"written_bytes,omitempty"`
}

type mediaJSON struct {
	ID            int64            `json:"id,omitempty"`
	Kind          entity.MediaKind `json:"kind,omitempty"`
	Identity      string           `json:"identity,omitempty"`
	Name          string           `json:"name,omitempty"`
	Profile       json.RawMessage  `json:"profile"`
	CreateTime    time.Time        `json:"create_time,omitempty"`
	DestroyTime   *time.Time       `json:"destroy_time,omitempty"`
	CapacityBytes int64            `json:"capacity_bytes,omitempty"`
	WrittenBytes  int64            `json:"written_bytes,omitempty"`
}

func (value Media) MarshalJSON() ([]byte, error) {
	profile, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(value.Profile)
	if err != nil {
		return nil, fmt.Errorf("encode Media profile failed, %w", err)
	}
	return json.Marshal(&mediaJSON{
		ID: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name, Profile: profile,
		CreateTime: value.CreateTime, DestroyTime: value.DestroyTime,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	})
}

func (value *Media) UnmarshalJSON(data []byte) error {
	decoded := new(mediaJSON)
	if err := json.Unmarshal(data, decoded); err != nil {
		return err
	}
	profile := new(entity.MediaProfile)
	if err := protojson.Unmarshal(decoded.Profile, profile); err != nil {
		return fmt.Errorf("decode Media profile failed, %w", err)
	}
	*value = Media{
		ID: decoded.ID, Kind: decoded.Kind, Identity: decoded.Identity, Name: decoded.Name, Profile: profile,
		CreateTime: decoded.CreateTime, DestroyTime: decoded.DestroyTime,
		CapacityBytes: decoded.CapacityBytes, WrittenBytes: decoded.WrittenBytes,
	}
	return nil
}

func (Media) TableName() string {
	return "media"
}

// MediaFile is one physical file yielded into a Library commit.
type MediaFile struct {
	Expected  *entity.ExpectedFile
	Path      string
	Size      int64
	Mode      fs.FileMode
	ModTime   time.Time
	WriteTime time.Time
	Hash      []byte
	// CheckedAt is supplied only after actual ACP content verification and successful Media Finalize.
	CheckedAt   int64
	HealthJobID int64

	StorageOrder    []byte
	StorageMetadata *entity.StorageMetadata
}

// MediaFileSource yields files in strict Media path order.
type MediaFileSource func(context.Context, func(*MediaFile) error) error

// MediaStats contains aggregate information derived from physical positions.
type MediaStats struct {
	FileCount     int64
	LastWriteTime *time.Time
}

// GetMedia returns one Media by Library ID.
func (l *Library) GetMedia(ctx context.Context, id int64) (*Media, error) {
	result := new(Media)
	if err := l.db.WithContext(ctx).First(result, id).Error; err != nil {
		return nil, fmt.Errorf("get Media failed, id=%d, %w", id, err)
	}
	return result, nil
}

// MGetMedia returns Media rows keyed by ID.
func (l *Library) MGetMedia(ctx context.Context, ids ...int64) (map[int64]*Media, error) {
	result := make(map[int64]*Media, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	var rows []*Media
	if err := l.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("get Media batch failed, %w", err)
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

// GetMediaByIdentity returns the active Media for one backend identity.
func (l *Library) GetMediaByIdentity(
	ctx context.Context,
	kind entity.MediaKind,
	identity string,
) (*Media, error) {
	// Use Find so a normal identity miss does not emit GORM's record-not-found error log.
	result := new(Media)
	query := l.db.WithContext(ctx).Where(
		"kind = ? AND identity = ?", kind, normalizeMediaIdentity(kind, identity),
	).Limit(1).Find(result)
	if query.Error != nil {
		return nil, fmt.Errorf("get Media by identity failed, kind=%s identity=%q, %w", kind, identity, query.Error)
	}

	// Preserve the existing nil result contract for an unregistered identity.
	if query.RowsAffected == 0 {
		return nil, nil
	}
	return result, nil
}

// ListMedia returns a bounded Media page.
func (l *Library) ListMedia(ctx context.Context, filter *entity.MediaFilter) ([]*Media, error) {
	rows, _, err := l.ListMediaPage(ctx, filter)
	return rows, err
}

// ListMediaPage returns one bounded Media page and whether another row follows it.
func (l *Library) ListMediaPage(ctx context.Context, filter *entity.MediaFilter) ([]*Media, bool, error) {
	// Validate bounds before converting them or choosing one pagination scheme.
	limit := int64(20)
	if filter != nil && filter.Limit != nil {
		limit = *filter.Limit
	}
	if limit <= 0 {
		return nil, false, fmt.Errorf("list Media failed, limit=%d", limit)
	}
	if limit > maxMediaPageSize {
		return nil, false, fmt.Errorf("Media page limit is too large, limit=%d", limit)
	}
	if filter.GetOffset() < 0 {
		return nil, false, fmt.Errorf("Media page offset must not be negative")
	}
	if filter.GetAfterId() < 0 {
		return nil, false, fmt.Errorf("Media page after_id must not be negative")
	}
	if filter != nil && filter.AfterId != nil && filter.Offset != nil {
		return nil, false, fmt.Errorf("Media page after_id and offset are mutually exclusive")
	}

	// Search durable fields before pagination; LIKE metacharacters are literal input.
	query := l.db.WithContext(ctx)
	if filter != nil && len(filter.Kinds) > 0 {
		query = query.Where("kind IN ?", filter.Kinds)
	}
	if text := strings.TrimSpace(filter.GetQuery()); text != "" {
		query = query.Where(clause.Or(containsExpression("name", text), containsExpression("identity", text)))
	}

	// Keyset searches survive deleted prior rows; legacy offset pages retain their ordering.
	if filter != nil && filter.AfterId != nil {
		query = query.Where("id > ?", *filter.AfterId).Order("id ASC")
	} else {
		query = query.Order("create_time DESC, id DESC").Offset(int(filter.GetOffset()))
	}

	// Read one sentinel row so clients can paginate without an additional count query.
	var rows []*Media
	if err := query.Limit(int(limit) + 1).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("list Media failed, %w", err)
	}
	hasMore := int64(len(rows)) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

// CreateMedia registers a Media without physical positions.
func (l *Library) CreateMedia(ctx context.Context, value *Media) (*Media, error) {
	if err := validateMedia(value); err != nil {
		return nil, err
	}
	if value.CreateTime.IsZero() {
		value.CreateTime = time.Now()
	}
	if err := l.db.WithContext(ctx).Create(value).Error; err != nil {
		return nil, fmt.Errorf("create Media failed, kind=%s identity=%q, %w", value.Kind, value.Identity, err)
	}
	return value, nil
}

// CommitMedia atomically stores one ordered set of new physical files.
func (l *Library) CommitMedia(
	ctx context.Context,
	requested *Media,
	source MediaFileSource,
) (*Media, error) {
	// Reject incomplete publication inputs before starting the metadata transaction.
	if err := validateMedia(requested); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("commit Media failed, file source is nil")
	}

	// Publish inventory, selected versions and covered originals as one fact change.
	var committed *Media
	if err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Resolve the immutable Media identity before inserting any Position.
		stored, err := resolveCommittedMedia(tx, requested)
		if err != nil {
			return err
		}

		// Insert the bounded file stream and rebuild only this Media's derived directory index.
		written, err := insertMediaFiles(ctx, tx, stored, source)
		if err != nil {
			return err
		}
		if err := reconcileCoveredVersions(tx, tx.Where("file_locations.signature IN (?)",
			tx.Model(ModelPosition).Select("signature").Where("media_id = ? AND is_dir = ?", stored.ID, false))); err != nil {
			return err
		}
		stored.WrittenBytes += written
		if requested.CapacityBytes > 0 {
			stored.CapacityBytes = requested.CapacityBytes
		}
		if stored.Kind == entity.MediaKind_MEDIA_KIND_TAPE && stored.CapacityBytes < stored.WrittenBytes {
			stored.CapacityBytes = stored.WrittenBytes
		}
		if err := tx.Model(stored).Updates(map[string]any{
			"written_bytes": stored.WrittenBytes, "capacity_bytes": stored.CapacityBytes,
		}).Error; err != nil {
			return fmt.Errorf("update Media totals failed, media_id=%d, %w", stored.ID, err)
		}
		committed = stored
		return nil
	}); err != nil {
		return nil, err
	}
	return committed, nil
}

// DeleteMedia atomically removes Library metadata without changing physical Media files.
func (l *Library) DeleteMedia(ctx context.Context, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("media_id IN ?", ids).Delete(ModelPosition).Error; err != nil {
			return fmt.Errorf("delete Media positions failed, %w", err)
		}
		if err := tx.Where("id IN ?", ids).Delete(ModelMedia).Error; err != nil {
			return fmt.Errorf("delete Media failed, %w", err)
		}
		return nil
	})
}

// GetMediaStats returns bounded aggregates for one Media.
func (l *Library) GetMediaStats(ctx context.Context, mediaID int64) (*MediaStats, error) {
	result := new(MediaStats)
	query := l.db.WithContext(ctx).Model(ModelPosition).Where("media_id = ? AND is_dir = ?", mediaID, false)
	if err := query.Count(&result.FileCount).Error; err != nil {
		return nil, fmt.Errorf("count Media files failed, media_id=%d, %w", mediaID, err)
	}
	if result.FileCount == 0 {
		return result, nil
	}

	latest := new(Position)
	if err := query.Select("write_time").Order("write_time DESC").First(latest).Error; err != nil {
		return nil, fmt.Errorf("read Media last write time failed, media_id=%d, %w", mediaID, err)
	}
	if !latest.WriteTime.IsZero() {
		result.LastWriteTime = &latest.WriteTime
	}
	return result, nil
}

func resolveCommittedMedia(tx *gorm.DB, requested *Media) (*Media, error) {
	if requested.ID == 0 {
		if requested.CreateTime.IsZero() {
			requested.CreateTime = time.Now()
		}
		if err := tx.Create(requested).Error; err != nil {
			return nil, fmt.Errorf("create Media for commit failed, identity=%q, %w", requested.Identity, err)
		}
		return requested, nil
	}

	stored := new(Media)
	if err := tx.First(stored, requested.ID).Error; err != nil {
		return nil, fmt.Errorf("read Media for commit failed, media_id=%d, %w", requested.ID, err)
	}
	if stored.Kind != requested.Kind || stored.Identity != requested.Identity || stored.Name != requested.Name ||
		!proto.Equal(stored.Profile, requested.Profile) {
		return nil, fmt.Errorf("Media identity changed, media_id=%d", requested.ID)
	}
	return stored, nil
}

func insertMediaFiles(
	ctx context.Context,
	tx *gorm.DB,
	media *Media,
	source MediaFileSource,
) (int64, error) {
	positions := make([]*Position, 0, batchSize)
	flush := func() error {
		if len(positions) == 0 {
			return nil
		}
		if err := tx.CreateInBatches(positions, batchSize).Error; err != nil {
			return fmt.Errorf("create Media positions failed, media_id=%d, %w", media.ID, err)
		}
		positions = positions[:0]
		return nil
	}

	var previous string
	var written int64
	if err := source(ctx, func(file *MediaFile) error {
		// Validate physical facts before resolving any explicitly selected logical identity.
		if err := validateMediaFile(media, file, previous); err != nil {
			return err
		}
		previous = file.Path
		written += file.Size
		var fileID int64
		var signature []byte
		if len(file.Hash) > 0 {
			var err error
			signature, err = NewFileSignature(file.Hash, file.Size)
			if err != nil {
				return err
			}
		}
		if expected := file.Expected; expected != nil {
			var stored File
			if expected.FileId <= 0 || len(expected.Signature) == 0 {
				return fmt.Errorf("selected Archive File identity is missing")
			}
			if err := tx.First(&stored, expected.FileId).Error; err != nil {
				return err
			}
			if stored.Kind != entity.FileKind_FILE_KIND_REGULAR {
				return fmt.Errorf("Archive target File is not regular, file_id=%d", expected.FileId)
			}
			if len(expected.Sha256) != 32 || !bytes.Equal(file.Hash, expected.Sha256) || file.Size != expected.Size {
				return fmt.Errorf("Archive copy does not match selected File %d", expected.FileId)
			}
			fileID = stored.ID
			signature = expected.Signature
			now := time.Now().UnixMilli()
			if _, err := recordVersion(tx, &FileVersion{FileID: fileID, Signature: signature, Hash: expected.Sha256, Size: expected.Size,
				Mode: expected.Mode, MtimeNS: expected.MtimeNs, FirstArchivedAt: &now, LastArchivedAt: &now}); err != nil {
				return err
			}
		}

		// Physical inventory has no File owner; verified versions resolve it by content.
		position := &Position{
			Signature: signature,
			MediaID:   media.ID, Path: file.Path, Mode: uint32(file.Mode), ModTime: file.ModTime,
			WriteTime: file.WriteTime, Size: file.Size, Hash: file.Hash,
			StorageOrder: file.StorageOrder, StorageMetadata: file.StorageMetadata,
		}
		if file.CheckedAt > 0 && len(file.Hash) == 32 {
			position.Health = entity.PositionHealth_HEALTHY
			position.CheckedAt, position.HealthJobID = file.CheckedAt, file.HealthJobID
		}
		positions = append(positions, position)
		if len(positions) == batchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("read Media files failed, media_id=%d, %w", media.ID, err)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if err := rebuildMediaPositionIndex(ctx, tx, media.ID); err != nil {
		return 0, err
	}
	return written, nil
}

func validateMediaFile(media *Media, file *MediaFile, previous string) error {
	if file == nil {
		return fmt.Errorf("Media file is nil")
	}
	if err := entity.ValidateRelativePath(file.Path); err != nil {
		return err
	}
	if file.Size < 0 {
		return fmt.Errorf("Media file size is invalid, path=%q size=%d", file.Path, file.Size)
	}
	if previous != "" && file.Path <= previous {
		return fmt.Errorf("Media files are not strictly ordered, previous=%q path=%q", previous, file.Path)
	}

	tape := media.Profile.GetTape()
	if tape == nil {
		if len(file.StorageOrder) != 0 || file.StorageMetadata != nil {
			return fmt.Errorf("random-readable Media file has storage order, path=%q", file.Path)
		}
		return nil
	}
	if tape.Format == TapeFormatLTFSV1 && file.Size > 0 &&
		(len(file.StorageOrder) == 0 || file.StorageMetadata == nil) {
		return fmt.Errorf("sequential Media file has no storage position, path=%q", file.Path)
	}
	return nil
}

func validateMedia(value *Media) error {
	if value == nil {
		return fmt.Errorf("Media is nil")
	}
	value.Identity = normalizeMediaIdentity(value.Kind, value.Identity)
	if value.Identity == "" {
		return fmt.Errorf("Media identity is empty")
	}
	if value.Profile == nil {
		return fmt.Errorf("Media profile is nil")
	}

	switch value.Kind {
	case entity.MediaKind_MEDIA_KIND_TAPE:
		profile := value.Profile.GetTape()
		if profile == nil || value.Profile.GetVolume() != nil {
			return fmt.Errorf("Tape Media profile is invalid")
		}
		if profile.Format != TapeFormatLTFSV0 && profile.Format != TapeFormatLTFSV1 {
			return fmt.Errorf("Tape Media format is unsupported, format=%q", profile.Format)
		}
	case entity.MediaKind_MEDIA_KIND_VOLUME:
		profile := value.Profile.GetVolume()
		if profile == nil || value.Profile.GetTape() != nil {
			return fmt.Errorf("Volume Media profile is invalid")
		}
		if profile.Type != entity.VolumeType_VOLUME_TYPE_HDD && profile.Type != entity.VolumeType_VOLUME_TYPE_HM_SMR {
			return fmt.Errorf("Volume Media type is unsupported, type=%s", profile.Type)
		}
		identity, err := uuid.Parse(value.Identity)
		if err != nil {
			return fmt.Errorf("Volume Media identity is invalid, identity=%q, %w", value.Identity, err)
		}
		value.Identity = identity.String()
	default:
		return fmt.Errorf("unsupported Media kind, kind=%s", value.Kind)
	}
	return nil
}

func normalizeMediaIdentity(kind entity.MediaKind, identity string) string {
	identity = strings.TrimSpace(identity)
	if kind == entity.MediaKind_MEDIA_KIND_TAPE {
		return strings.ToUpper(identity)
	}
	return identity
}
