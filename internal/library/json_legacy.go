package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

type legacyLibraryFile struct {
	ID        int64     `json:"id,omitempty"`
	ParentID  int64     `json:"parent_id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Mode      uint32    `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	Hash      []byte    `json:"hash,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Signature []byte    `json:"signature,omitempty"`
}

type legacyLibraryTape struct {
	ID            int64      `json:"id,omitempty"`
	Barcode       string     `json:"barcode,omitempty"`
	Name          string     `json:"name,omitempty"`
	Encryption    string     `json:"encryption,omitempty"`
	CreateTime    time.Time  `json:"create_time,omitempty"`
	DestroyTime   *time.Time `json:"destroy_time,omitempty"`
	CapacityBytes int64      `json:"capacity_bytes,omitempty"`
	WritenBytes   int64      `json:"writen_bytes,omitempty"`
}

type legacyLibraryPosition struct {
	ID        int64     `json:"id,omitempty"`
	FileID    int64     `json:"file_id,omitempty"`
	TapeID    int64     `json:"tape_id,omitempty"`
	Path      string    `json:"path,omitempty"`
	Mode      uint32    `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	WriteTime time.Time `json:"write_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Hash      []byte    `json:"hash,omitempty"`
}

type legacyLibraryDecoders struct {
	files     func(*json.Decoder) error
	tapes     func(*json.Decoder) error
	positions func(*json.Decoder) error
}

func (l *Library) importLegacyJSON(ctx context.Context, r io.Reader, dryRun bool) (rerr error) {
	// Resolve arbitrary legacy array order in a disposable indexed staging database, before replacement.
	temporary, err := resource.OpenTemporaryDB("", "yatm-legacy-import-")
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, temporary.Close()) }()
	stage := temporary.DB.WithContext(ctx)
	if err := stage.AutoMigrate(&legacyLibraryFile{}, &legacyLibraryTape{}, &legacyLibraryPosition{}); err != nil {
		return err
	}

	// Decode into staging before replacing any live catalog records.
	included := map[string]struct{}{}
	decoders := legacyLibraryDecoders{
		files: func(decoder *json.Decoder) error {
			included[recordTypeFile] = struct{}{}
			return importLegacyArray(decoder, stage, "file", func(v *legacyLibraryFile) *legacyLibraryFile {
				// Root is virtual; discard it before the staging table can allocate a real ID.
				if v.ID == 0 {
					return nil
				}
				return v
			})
		},
		tapes: func(decoder *json.Decoder) error {
			included[recordTypeMedia] = struct{}{}
			return importLegacyArray(decoder, stage, "tape", func(v *legacyLibraryTape) *legacyLibraryTape { return v })
		},
		positions: func(decoder *json.Decoder) error {
			included[recordTypePosition] = struct{}{}
			return importLegacyArray(decoder, stage, "position", func(v *legacyLibraryPosition) *legacyLibraryPosition { return v })
		},
	}
	if _, err := decodeLegacyLibrary(json.NewDecoder(r), decoders); err != nil {
		return fmt.Errorf("validate legacy backup: %w", err)
	}

	// Replace the selected catalog groups and validate their references atomically.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owners, err := retainedImportMedia(tx, included)
		if err != nil {
			return err
		}
		// Logical identities precede archived evidence so only confirmed content becomes history.
		if err := clearImportedEntities(tx, included); err != nil {
			return err
		}
		if _, selected := included[recordTypeFile]; selected {
			if err := eachLegacyRow(ctx, stage, func(v *legacyLibraryFile) error {
				// legacy stored type in Mode; only this compatibility boundary converts it to Kind.
				file := &File{ID: v.ID, ParentID: v.ParentID, Name: v.Name, Kind: entity.FileKind_FILE_KIND_REGULAR}
				if fs.FileMode(v.Mode).IsDir() {
					file.Kind = entity.FileKind_FILE_KIND_DIRECTORY
				}
				modified, err := dataformat.Nanoseconds(v.ModTime)
				if err != nil {
					return fmt.Errorf("convert legacy File mtime, file_id=%d: %w", v.ID, err)
				}
				file.CreatedAtNS, file.UpdatedAtNS = modified, modified
				// Missing legacy source times retain zero while normal name validation still runs.
				return createFileRow(tx.Session(&gorm.Session{NowFunc: func() time.Time { return time.Unix(0, 0) }}), file)
			}); err != nil {
				return err
			}
		}

		// Preserve the immutable Media identity and profile of every legacy Tape.
		if _, selected := included[recordTypeMedia]; selected {
			if err := eachLegacyRow(ctx, stage, func(v *legacyLibraryTape) error {
				media, err := legacyTapeToMedia(v)
				if err != nil {
					return err
				}
				if err := validateMedia(media); err != nil {
					return err
				}
				return tx.Create(media).Error
			}); err != nil {
				return err
			}
		}

		// Rebuild physical inventory and record only the historical content supported by it.
		if _, selected := included[recordTypePosition]; selected {
			if err := eachLegacyRow(ctx, stage, func(v *legacyLibraryPosition) error {
				if err := entity.ValidateRelativePath(v.Path); err != nil {
					return err
				}
				modified, err := dataformat.Nanoseconds(v.ModTime)
				if err != nil {
					return fmt.Errorf("convert legacy Position mtime, position_id=%d: %w", v.ID, err)
				}
				written, err := dataformat.Nanoseconds(v.WriteTime)
				if err != nil {
					return fmt.Errorf("convert legacy Position write time, position_id=%d: %w", v.ID, err)
				}
				position := &Position{ID: v.ID, MediaID: v.TapeID, Path: v.Path, Mode: v.Mode, MtimeNS: modified,
					WrittenAtNS: written, Size: v.Size, Hash: v.Hash}
				var file legacyLibraryFile
				if v.FileID > 0 {
					if err := stage.Where("id = ?", v.FileID).Limit(1).Find(&file).Error; err != nil {
						return err
					}
				}
				signature := file.Signature
				matches := file.ID != 0 && file.Size == v.Size && (len(file.Hash) == 0 || bytes.Equal(file.Hash, v.Hash))
				if !matches || len(signature) == 0 {
					// Incomplete legacy facts remain inventory, not invented content history.
					signature, _ = NewFileSignature(v.Hash, v.Size)
				}
				position.Signature = signature
				if err := tx.Create(position).Error; err != nil {
					return err
				}
				if file.ID == 0 || len(signature) == 0 {
					return nil
				}
				mode, mtime := v.Mode, modified
				if matches {
					mtime, err = dataformat.Nanoseconds(file.ModTime)
					if err != nil {
						return fmt.Errorf("convert legacy version mtime, file_id=%d: %w", file.ID, err)
					}
					mode = file.Mode
				}
				_, err = recordVersion(tx, &FileVersion{FileID: file.ID, Signature: signature, Hash: v.Hash, Size: v.Size, Mode: mode, MtimeNS: mtime})
				return err
			}); err != nil {
				return err
			}
			if err := rebuildPositionIndex(ctx, tx); err != nil {
				return err
			}
		}

		// Reject dangling references before the replacement becomes visible.
		if err := validateRetainedImportMedia(tx, owners); err != nil {
			return err
		}
		if err := validateCatalogImport(ctx, tx); err != nil {
			return err
		}
		if dryRun {
			return ErrImportDryRun
		}
		return nil
	})
}

func eachLegacyRow[T any](ctx context.Context, db *gorm.DB, yield func(*T) error) error {
	after := int64(math.MinInt64)
	for {
		var rows []*T
		if err := db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		// Staging types all carry the original positive legacy catalog ID.
		switch row := any(rows[len(rows)-1]).(type) {
		case *legacyLibraryFile:
			after = row.ID
		case *legacyLibraryTape:
			after = row.ID
		case *legacyLibraryPosition:
			after = row.ID
		default:
			return fmt.Errorf("unsupported legacy staging row")
		}
	}
}

func legacyTapeToMedia(value *legacyLibraryTape) (*Media, error) {
	// Legacy text times retain their original encoding until this checked catalog boundary.
	created, err := dataformat.Nanoseconds(value.CreateTime)
	if err != nil {
		return nil, fmt.Errorf("convert legacy Tape creation time, tape_id=%d: %w", value.ID, err)
	}
	var destroyed *int64
	if value.DestroyTime != nil {
		stamp, err := dataformat.Nanoseconds(*value.DestroyTime)
		if err != nil {
			return nil, fmt.Errorf("convert legacy Tape destruction time, tape_id=%d: %w", value.ID, err)
		}
		destroyed = &stamp
	}
	return &Media{
		ID: value.ID, Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: strings.ToUpper(strings.TrimSpace(value.Barcode)), Name: value.Name,
		Profile: (&entity.TapeMediaProfile{
			Encryption: value.Encryption, Format: TapeFormatLTFSV0,
		}).Pack(),
		CreatedAtNS: created, DestroyedAtNS: destroyed,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WritenBytes,
	}, nil
}

func decodeLegacyLibrary(decoder *json.Decoder, decoders legacyLibraryDecoders) (bool, error) {
	// Decode each declared top-level entity exactly once.
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return false, err
	}
	seen := make(map[string]struct{}, 3)
	positions := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false, err
		}
		name, ok := token.(string)
		if !ok {
			return false, fmt.Errorf("legacy Library field name is invalid")
		}
		if _, exists := seen[name]; exists {
			return false, fmt.Errorf("duplicate legacy Library field %q", name)
		}
		seen[name] = struct{}{}

		var decode func(*json.Decoder) error
		switch name {
		case "files":
			decode = decoders.files
		case "tapes":
			decode = decoders.tapes
		case "positions":
			decode = decoders.positions
			positions = true
		default:
			return false, fmt.Errorf("unknown legacy Library field %q", name)
		}
		if decode == nil {
			return false, fmt.Errorf("legacy Library field %q has no decoder", name)
		}
		if err := decode(decoder); err != nil {
			return false, err
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}

	// Require one complete top-level object and only trailing whitespace.
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return false, fmt.Errorf("unexpected trailing data")
		}
		return false, err
	}
	return positions, nil
}

func validateLegacyArray[Value any](decoder *json.Decoder) error {
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return err
	}
	for decoder.More() {
		if err := decoder.Decode(new(Value)); err != nil {
			return err
		}
	}
	return expectJSONDelimiter(decoder, ']')
}

func importLegacyArray[Legacy any, Current any](
	decoder *json.Decoder,
	tx *gorm.DB,
	recordType string,
	convert func(*Legacy) *Current,
) error {
	// Stage one legacy row at a time without allowing JSON decoding to rewrite its text.
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return err
	}
	rows := make([]*Current, 0, batchSize)
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("decode legacy Library %s failed, %w", recordType, err)
		}
		if err := validateImportJSONText(raw); err != nil {
			return fmt.Errorf("decode legacy Library %s failed, %w", recordType, err)
		}
		value := new(Legacy)
		if err := json.Unmarshal(raw, value); err != nil {
			return fmt.Errorf("decode legacy Library %s failed, %w", recordType, err)
		}
		row := convert(value)
		if row == nil {
			continue
		}
		rows = append(rows, row)
		if len(rows) == batchSize {
			if err := insertJSONLRows(tx, recordType, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}

	// Flush the final bounded batch only after the array closes successfully.
	if err := expectJSONDelimiter(decoder, ']'); err != nil {
		return err
	}
	return insertJSONLRows(tx, recordType, rows)
}

func clearImportedModel(tx *gorm.DB, model any, name string) error {
	if result := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model); result.Error != nil {
		return fmt.Errorf("clear legacy Library %s failed, %w", name, result.Error)
	}
	return nil
}

func expectJSONDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != want {
		return fmt.Errorf("expected %q", want)
	}
	return nil
}
