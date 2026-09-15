package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/samuelncui/yatm/entity"
)

var ErrTapeConflict = errors.New("tape content conflicts with existing barcode")

const (
	TapeFormatLTFSV0 = "ltfs_v0"
	TapeFormatLTFSV1 = "ltfs_v1"
)

// Tape is the compatibility view of a Tape Media.
type Tape struct {
	ID            int64      `json:"id,omitempty"`
	Barcode       string     `json:"barcode,omitempty"`
	Name          string     `json:"name,omitempty"`
	SerialNumber  string     `json:"serial_number,omitempty"`
	Encryption    string     `json:"encryption,omitempty"`
	Format        string     `json:"format,omitempty"`
	CreateTime    time.Time  `json:"create_time,omitempty"`
	DestroyTime   *time.Time `json:"destroy_time,omitempty"`
	CapacityBytes int64      `json:"capacity_bytes,omitempty"`
	WritenBytes   int64      `json:"writen_bytes,omitempty"`
}

type TapeStats = MediaStats

type TapeFilter struct {
	Limit  *int64
	Offset *int64
}

type TapeFile struct {
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	Mode      os.FileMode `json:"mode"`
	ModTime   time.Time   `json:"mod_time"`
	WriteTime time.Time   `json:"write_time"`
	Hash      []byte      `json:"hash"`

	StorageOrder    []byte
	StorageMetadata *entity.StorageMetadata
}

// TapeFileSource yields Tape files in strict path order.
type TapeFileSource func(context.Context, func(*TapeFile) error) error

func (l *Library) CreateTape(ctx context.Context, tape *Tape, files []*TapeFile) (*Tape, error) {
	ordered := append([]*TapeFile(nil), files...)
	for index, file := range ordered {
		if file == nil {
			return nil, fmt.Errorf("create Tape failed, file is nil, index=%d", index)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	return l.CreateTapeFromSource(ctx, tape, func(_ context.Context, yield func(*TapeFile) error) error {
		for _, file := range ordered {
			if err := yield(file); err != nil {
				return err
			}
		}
		return nil
	})
}

func (l *Library) CreateTapeFromSource(ctx context.Context, tape *Tape, source TapeFileSource) (*Tape, error) {
	if tape == nil {
		return nil, fmt.Errorf("create Tape failed, Tape is nil")
	}
	if source == nil {
		return nil, fmt.Errorf("create Tape failed, file source is nil")
	}
	if tape.Format == "" {
		tape.Format = TapeFormatLTFSV0
	}

	media := tapeToMedia(tape)
	stored, err := l.CommitMedia(ctx, media, tapeFileSource(source))
	if err != nil {
		return nil, fmt.Errorf("create Tape failed, barcode=%q, %w", tape.Barcode, err)
	}
	return mediaToTape(stored), nil
}

func (l *Library) AppendTapeFromSource(ctx context.Context, tapeID int64, source TapeFileSource) (*Tape, error) {
	stored, err := l.GetMedia(ctx, tapeID)
	if err != nil {
		return nil, fmt.Errorf("read append Tape failed, tape_id=%d, %w", tapeID, err)
	}
	profile := stored.Profile.GetTape()
	if profile == nil || profile.Format != TapeFormatLTFSV1 {
		return nil, fmt.Errorf("append Tape format is unsupported, tape_id=%d", tapeID)
	}
	stored, err = l.CommitMedia(ctx, stored, tapeFileSource(source))
	if err != nil {
		return nil, fmt.Errorf("append Tape failed, tape_id=%d, %w", tapeID, err)
	}
	return mediaToTape(stored), nil
}

func (l *Library) GetTape(ctx context.Context, id int64) (*Tape, error) {
	stored, err := l.GetMedia(ctx, id)
	if err != nil {
		return nil, err
	}
	if stored.Kind != entity.MediaKind_MEDIA_KIND_TAPE {
		return nil, ErrFileNotFound
	}
	return mediaToTape(stored), nil
}

func (l *Library) DeleteTapes(ctx context.Context, ids ...int64) error {
	return l.DeleteMedia(ctx, ids...)
}

func (l *Library) GetTapeStats(ctx context.Context, tapeID int64) (*TapeStats, error) {
	return l.GetMediaStats(ctx, tapeID)
}

func (l *Library) ListTape(ctx context.Context, filter *TapeFilter) ([]*Tape, error) {
	mediaFilter := &entity.MediaFilter{Kinds: []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE}}
	if filter != nil {
		mediaFilter.Limit = filter.Limit
		mediaFilter.Offset = filter.Offset
	}
	rows, err := l.ListMedia(ctx, mediaFilter)
	if err != nil {
		return nil, err
	}
	result := make([]*Tape, 0, len(rows))
	for _, row := range rows {
		result = append(result, mediaToTape(row))
	}
	return result, nil
}

func (l *Library) MGetTape(ctx context.Context, ids ...int64) (map[int64]*Tape, error) {
	rows, err := l.MGetMedia(ctx, ids...)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]*Tape, len(rows))
	for id, row := range rows {
		if row.Kind == entity.MediaKind_MEDIA_KIND_TAPE {
			result[id] = mediaToTape(row)
		}
	}
	return result, nil
}

func (l *Library) MGetTapeByBarcode(ctx context.Context, barcodes ...string) (map[string]*Tape, error) {
	result := make(map[string]*Tape, len(barcodes))
	for _, barcode := range barcodes {
		row, err := l.GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, barcode)
		if err != nil {
			return nil, err
		}
		if row != nil {
			result[row.Identity] = mediaToTape(row)
		}
	}
	return result, nil
}

func tapeFileSource(source TapeFileSource) MediaFileSource {
	return func(ctx context.Context, yield func(*MediaFile) error) error {
		return source(ctx, func(file *TapeFile) error {
			if file == nil {
				return yield(nil)
			}
			return yield(&MediaFile{
				Path: file.Path, Size: file.Size, Mode: file.Mode, ModTime: file.ModTime,
				WriteTime: file.WriteTime, Hash: file.Hash, StorageOrder: file.StorageOrder,
				StorageMetadata: file.StorageMetadata,
			})
		})
	}
}

func tapeToMedia(tape *Tape) *Media {
	return &Media{
		ID: tape.ID, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: tape.Barcode, Name: tape.Name,
		Profile: (&entity.TapeMediaProfile{
			SerialNumber: tape.SerialNumber, Encryption: tape.Encryption, Format: tape.Format,
		}).Pack(),
		CreateTime: tape.CreateTime, DestroyTime: tape.DestroyTime,
		CapacityBytes: tape.CapacityBytes, WrittenBytes: tape.WritenBytes,
	}
}

func mediaToTape(media *Media) *Tape {
	profile := media.Profile.GetTape()
	if profile == nil {
		profile = new(entity.TapeMediaProfile)
	}
	return &Tape{
		ID: media.ID, Barcode: media.Identity, Name: media.Name, SerialNumber: profile.SerialNumber,
		Encryption: profile.Encryption, Format: profile.Format, CreateTime: media.CreateTime,
		DestroyTime: media.DestroyTime, CapacityBytes: media.CapacityBytes, WritenBytes: media.WrittenBytes,
	}
}
