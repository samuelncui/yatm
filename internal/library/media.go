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
	"github.com/samuelncui/yatm/internal/dataformat"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ModelMedia = new(Media)

const maxMediaPageSize = 1000

const (
	TapeFormatLTFSV0 = "ltfs_v0"
	TapeFormatLTFSV1 = "ltfs_v1"
)

// Media is the common Library identity for Tape and mounted offline Volumes.
type Media struct {
	ID            int64                `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	Kind          entity.MediaKind     `gorm:"not null;index:idx_media_kind_identity,unique,priority:1" json:"kind,omitempty"`
	Identity      string               `gorm:"type:varchar(128);not null;index:idx_media_kind_identity,unique,priority:2" json:"identity,omitempty"`
	Name          string               `gorm:"type:varchar(256)" json:"name,omitempty"`
	Profile       *entity.MediaProfile `gorm:"type:blob;not null" json:"profile,omitempty"`
	CreatedAtNS   int64                `json:"created_at_ns,string"`
	DestroyedAtNS *int64               `json:"destroyed_at_ns,omitempty,string"`
	CapacityBytes int64                `json:"capacity_bytes,omitempty"`
	WrittenBytes  int64                `json:"written_bytes,omitempty"`
}

type mediaJSON struct {
	ID            int64            `json:"id,omitempty"`
	Kind          entity.MediaKind `json:"kind,omitempty"`
	Identity      string           `json:"identity,omitempty"`
	Name          string           `json:"name,omitempty"`
	Profile       json.RawMessage  `json:"profile"`
	CreatedAtNS   int64            `json:"created_at_ns,string"`
	DestroyedAtNS *int64           `json:"destroyed_at_ns,omitempty,string"`
	CapacityBytes int64            `json:"capacity_bytes,omitempty"`
	WrittenBytes  int64            `json:"written_bytes,omitempty"`
}

func (value Media) MarshalJSON() ([]byte, error) {
	// Keep the typed profile encoding while writing exact decimal-string instants.
	profile, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(value.Profile)
	if err != nil {
		return nil, fmt.Errorf("encode Media profile failed, %w", err)
	}
	return json.Marshal(&mediaJSON{
		ID: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name, Profile: profile,
		CreatedAtNS: value.CreatedAtNS, DestroyedAtNS: value.DestroyedAtNS,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	})
}

func (value *Media) UnmarshalJSON(data []byte) error {
	// An old timestamp name cannot silently become an unknown current instant.
	decoded := new(mediaJSON)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(decoded); err != nil {
		return err
	}

	// Validate the typed profile before replacing the caller's complete Media value.
	profile := new(entity.MediaProfile)
	if err := protojson.Unmarshal(decoded.Profile, profile); err != nil {
		return fmt.Errorf("decode Media profile failed, %w", err)
	}
	*value = Media{
		ID: decoded.ID, Kind: decoded.Kind, Identity: decoded.Identity, Name: decoded.Name, Profile: profile,
		CreatedAtNS: decoded.CreatedAtNS, DestroyedAtNS: decoded.DestroyedAtNS,
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
	// CheckedAtNS is supplied only after actual ACP content verification and successful Media Finalize.
	CheckedAtNS int64
	HealthJobID int64

	StorageOrder    []byte
	StorageMetadata *entity.StorageMetadata
}

// MediaFileSource yields files in strict Media path order.
type MediaFileSource func(context.Context, func(*MediaFile) error) error

// MediaStats contains aggregate information derived from physical positions.
type MediaStats struct {
	FileCount       int64
	LastWrittenAtNS *int64 `json:"last_written_at_ns,omitempty,string"`
}

// GetMedia returns one Media by Library ID.
func (l *Library) GetMedia(ctx context.Context, id int64) (*Media, error) {
	result := new(Media)
	if err := l.readDB().WithContext(ctx).First(result, id).Error; err != nil {
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
	if err := l.readDB().WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
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
	query := l.readDB().WithContext(ctx).Where(
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
	query := l.readDB().WithContext(ctx)
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
		query = query.Order("NULLIF(created_at_ns, 0) DESC, id DESC").Offset(int(filter.GetOffset()))
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
	// Admit the immutable identity before assigning a missing creation time.
	if err := validateMedia(value); err != nil {
		return nil, err
	}

	// Persist new Media with the current ns clock unless a source time is already known.
	if value.CreatedAtNS == 0 {
		value.CreatedAtNS = time.Now().UnixNano()
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

// MediaDeleteResult reports what one Media deletion removes, or would remove under a dry run.
type MediaDeleteResult struct {
	Media     int64
	Positions int64
}

// DeleteMedia atomically removes Library metadata without changing physical Media files.
// A dry run counts the same rows without deleting them.
func (l *Library) DeleteMedia(ctx context.Context, dryRun bool, ids ...int64) (*MediaDeleteResult, error) {
	result := new(MediaDeleteResult)
	if len(ids) == 0 {
		return result, nil
	}
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Resolve the retained selection first; an absent Media removes nothing.
		var found []int64
		if err := tx.Model(ModelMedia).Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
			return fmt.Errorf("resolve Media deletion failed, %w", err)
		}
		if len(found) == 0 {
			return nil
		}
		result.Media = int64(len(found))
		if err := tx.Model(ModelPosition).Where("media_id IN ?", found).Count(&result.Positions).Error; err != nil {
			return fmt.Errorf("count Media positions failed, %w", err)
		}
		if dryRun {
			return nil
		}
		if err := tx.Where("media_id IN ?", found).Delete(ModelPosition).Error; err != nil {
			return fmt.Errorf("delete Media positions failed, %w", err)
		}
		if err := tx.Where("id IN ?", found).Delete(ModelMedia).Error; err != nil {
			return fmt.Errorf("delete Media failed, %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetMediaStats returns bounded aggregates for one Media.
func (l *Library) GetMediaStats(ctx context.Context, mediaID int64) (*MediaStats, error) {
	// Zero remains an unknown write time and cannot displace a known pre-epoch instant.
	result := new(MediaStats)
	if err := l.readDB().WithContext(ctx).Model(ModelPosition).Where("media_id = ? AND is_dir = ?", mediaID, false).
		Select("COUNT(*) AS file_count, MAX(NULLIF(written_at_ns, 0)) AS last_written_at_ns").Scan(result).Error; err != nil {
		return nil, fmt.Errorf("read Media stats failed, media_id=%d, %w", mediaID, err)
	}
	return result, nil
}

func resolveCommittedMedia(tx *gorm.DB, requested *Media) (*Media, error) {
	// First publication can establish a new Media identity and its creation instant.
	if requested.ID == 0 {
		if requested.CreatedAtNS == 0 {
			requested.CreatedAtNS = time.Now().UnixNano()
		}
		if err := tx.Create(requested).Error; err != nil {
			return nil, fmt.Errorf("create Media for commit failed, identity=%q, %w", requested.Identity, err)
		}
		return requested, nil
	}

	// An existing identity must retain its immutable profile before accepting more Positions.
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
	// Keep physical inserts bounded inside the caller's publication transaction.
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

	// Validate the ordered stream and publish selected content with its physical evidence.
	var previous string
	var written int64
	if err := source(ctx, func(file *MediaFile) error {
		// Validate physical facts before resolving any explicitly selected logical identity.
		if err := validateMediaFile(media, file, previous); err != nil {
			return err
		}
		mtimeNS, err := dataformat.Nanoseconds(file.ModTime)
		if err != nil {
			return fmt.Errorf("convert Media file mtime, path=%q: %w", file.Path, err)
		}
		writtenAtNS, err := dataformat.Nanoseconds(file.WriteTime)
		if err != nil {
			return fmt.Errorf("convert Media file write time, path=%q: %w", file.Path, err)
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
			var stored fileRow
			if expected.FileId <= 0 || len(expected.Signature) == 0 {
				return fmt.Errorf("selected Archive File identity is missing")
			}
			if err := tx.First(&stored, expected.FileId).Error; err != nil {
				return err
			}
			if stored.Kind != entity.FileKind_FILE_KIND_REGULAR {
				return fmt.Errorf("Archive target File is not regular, file_id=%d", expected.FileId)
			}
			mode, versionMtimeNS := uint32(file.Mode), mtimeNS
			if len(expected.Sha256) == 32 && file.Size == expected.SizeBytes && bytes.Equal(file.Hash, expected.Sha256) {
				signature = expected.Signature
				mode, versionMtimeNS = expected.Mode, expected.MtimeNs
			}
			fileID = stored.ID
			now := time.Now().UnixNano()
			if _, err := recordVersion(tx, &FileVersion{FileID: fileID, Signature: signature, Hash: file.Hash, Size: file.Size,
				Mode: mode, MtimeNS: versionMtimeNS, FirstArchivedAtNS: &now, LastArchivedAtNS: &now}); err != nil {
				return err
			}
		}

		// Physical inventory has no File owner; verified versions resolve it by content.
		position := &Position{
			Signature: signature,
			MediaID:   media.ID, Path: file.Path, Mode: uint32(file.Mode), MtimeNS: mtimeNS,
			WrittenAtNS: writtenAtNS, Size: file.Size, Hash: file.Hash,
			StorageOrder: file.StorageOrder, StorageMetadata: file.StorageMetadata,
		}
		if file.CheckedAtNS != 0 && len(file.Hash) == 32 {
			position.Health = entity.PositionHealth_POSITION_HEALTH_HEALTHY
			position.CheckedAtNS, position.HealthJobID = file.CheckedAtNS, file.HealthJobID
		}
		positions = append(positions, position)
		if len(positions) == batchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("read Media files failed, media_id=%d, %w", media.ID, err)
	}

	// Finish the last batch before deriving directory summaries from the complete inventory.
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
