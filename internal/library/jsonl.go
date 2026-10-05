package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

const (
	JSONLContentType             = "application/x-ndjson"
	jsonlFormat                  = "yatm-library-backup"
	jsonlFormatVersion           = 1
	maxJSONLRecordSize           = 1 << 20
	recordTypeHeader             = "header"
	recordTypeMedia              = "media"
	recordTypeLegacyTape         = "tape"
	recordTypeFile               = "file"
	recordTypePosition           = "position"
	recordTypeEnd                = "end"
	recordTypeLocation           = "location"
	recordTypeFileLocation       = "file_location"
	recordTypeFileVersion        = "file_version"
	recordTypeFileVersionArchive = "file_version_archive"
	recordTypeTracking           = "file_tracking_key"
)

type jsonlOutputRecord struct {
	Type     string   `json:"type"`
	Format   string   `json:"format,omitempty"`
	Version  int      `json:"version,omitempty"`
	Entities []string `json:"entities,omitempty"`
	Data     any      `json:"data,omitempty"`
}
type jsonlInputRecord struct {
	Type     string          `json:"type"`
	Format   string          `json:"format,omitempty"`
	Version  int             `json:"version,omitempty"`
	Entities []string        `json:"entities,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}
type libraryImportFormat uint8

const (
	libraryImportJSONL libraryImportFormat = iota + 1
	libraryImportLegacyJSON
)

// Export reads every selected entity from one consistent metadata snapshot.
func (l *Library) Export(ctx context.Context, w io.Writer, types []entity.LibraryEntityType) error {
	return l.readDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return (&Library{db: tx}).exportSnapshot(ctx, w, types)
	})
}

func (l *Library) exportSnapshot(ctx context.Context, w io.Writer, types []entity.LibraryEntityType) error {
	// Expand requested entity groups to their dependent saved-content and provenance records.
	selected := map[string]struct{}{}
	for _, kind := range types {
		switch kind {
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA:
			selected[recordTypeMedia] = struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION:
			selected[recordTypePosition] = struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE:
			selected[recordTypeFile], selected[recordTypeFileVersion] = struct{}{}, struct{}{}
			selected[recordTypeFileVersionArchive] = struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION:
			selected[recordTypeLocation] = struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION:
			selected[recordTypeFileLocation], selected[recordTypeTracking] = struct{}{}, struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_VERSION:
			selected[recordTypeFileVersion] = struct{}{}
			selected[recordTypeFileVersionArchive] = struct{}{}
		case entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_TRACKING_KEY:
			selected[recordTypeTracking] = struct{}{}
		default:
			return fmt.Errorf("unsupported export entity %s", kind)
		}
	}

	// Declare a stable record order and validate the complete snapshot before writing its header.
	names := []string{}
	for _, name := range catalogRecordOrder {
		if _, ok := selected[name]; ok {
			names = append(names, name)
		}
	}
	header := &jsonlInputRecord{Type: recordTypeHeader, Format: jsonlFormat, Version: jsonlFormatVersion, Entities: names}
	if _, err := validateJSONLHeader(header); err != nil {
		return err
	}

	// Stream bounded entity pages from the transaction's consistent metadata view.
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(header); err != nil {
		return err
	}
	for _, name := range names {
		var err error
		switch name {
		case recordTypeFile:
			err = l.exportJSONLFiles(ctx, encoder)
		case recordTypeMedia:
			err = exportJSONLRows(ctx, l.db, encoder, name, func(v *Media) int64 { return v.ID })
		case recordTypePosition:
			err = exportJSONLRows(ctx, l.db, encoder, name, func(v *Position) int64 { return v.ID })
		case recordTypeFileVersion:
			err = exportJSONLRows(ctx, l.db, encoder, name, func(v *FileVersion) int64 { return v.ID })
		case recordTypeFileVersionArchive:
			err = l.exportVersionArchives(ctx, encoder)
		case recordTypeLocation:
			err = exportJSONLRows(ctx, l.db, encoder, name, func(v *Location) int64 { return v.ID })
		case recordTypeFileLocation, recordTypeTracking:
			err = l.exportOriginalRelations(ctx, encoder, name)
		}
		if err != nil {
			return err
		}
	}
	return encoder.Encode(jsonlOutputRecord{Type: recordTypeEnd})
}

var catalogRecordOrder = []string{recordTypeMedia, recordTypeFile, recordTypeFileVersion, recordTypeFileVersionArchive, recordTypePosition, recordTypeLocation, recordTypeFileLocation, recordTypeTracking}

func (l *Library) exportOriginalRelations(ctx context.Context, encoder *json.Encoder, name string) error {
	var after int64
	for {
		// A File has at most one original and one supported native tracking record.
		var ids []int64
		model := any(&FileLocation{})
		if name == recordTypeTracking {
			model = &FileTrackingKey{}
		}
		if err := l.db.WithContext(ctx).Model(model).Where("file_id > ?", after).Distinct("file_id").Order("file_id").Limit(batchSize).Pluck("file_id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		switch name {
		case recordTypeFileLocation:
			var rows []*FileLocation
			if err := l.db.WithContext(ctx).Where("file_id IN ?", ids).Order("file_id").Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if err := encoder.Encode(jsonlOutputRecord{Type: name, Data: row}); err != nil {
					return err
				}
			}
		case recordTypeTracking:
			var rows []*FileTrackingKey
			if err := l.db.WithContext(ctx).Where("file_id IN ?", ids).Order("file_id, kind").Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if err := encoder.Encode(jsonlOutputRecord{Type: name, Data: row}); err != nil {
					return err
				}
			}
		}
		after = ids[len(ids)-1]
	}
}

// Import reads released legacy JSON and the published current Library backup family.
// ErrImportDryRun reports a validated import that was rolled back because the caller asked
// for a report instead of a replacement. Callers treat it as success, never as data loss.
var ErrImportDryRun = errors.New("library import dry run")

// Import replaces the selected catalog groups from one snapshot.
// A dry run validates the complete snapshot and then rolls its transaction back.
func (l *Library) Import(ctx context.Context, input io.Reader, dryRun bool) error {
	format, reader, err := detectLibraryImport(input)
	if err != nil {
		return err
	}
	if format == libraryImportLegacyJSON {
		return l.importLegacyJSON(ctx, reader, dryRun)
	}
	return l.importJSONL(ctx, reader, dryRun)
}

func (l *Library) importJSONL(ctx context.Context, input io.Reader, dryRun bool) error {
	// Validate the declared entity groups before replacing catalog metadata.
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLRecordSize)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("empty Library import")
	}
	var header jsonlInputRecord
	if err := validateImportJSONText(scanner.Bytes()); err != nil {
		return err
	}
	headerDecoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
	headerDecoder.DisallowUnknownFields()
	if err := headerDecoder.Decode(&header); err != nil {
		return err
	}
	included, err := validateJSONLHeader(&header)
	if err != nil {
		return err
	}

	// Retain all-or-nothing import, including relationships derived from imported coverage.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owners, err := retainedImportMedia(tx, included)
		if err != nil {
			return err
		}
		if err := clearImportedEntities(tx, included); err != nil {
			return err
		}
		for number := 2; scanner.Scan(); number++ {
			var record jsonlInputRecord
			if err := validateImportJSONText(scanner.Bytes()); err != nil {
				return fmt.Errorf("decode Library record %d: %w", number, err)
			}
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				return fmt.Errorf("decode Library record %d: %w", number, err)
			}
			if record.Type == recordTypeEnd {
				if err := validateJSONLEnd(scanner, number); err != nil {
					return err
				}
				if err := validateRetainedImportMedia(tx, owners); err != nil {
					return err
				}
				if err := validateCatalogImport(ctx, tx); err != nil {
					return err
				}
				if _, included := included[recordTypePosition]; included {
					if err := rebuildPositionIndex(ctx, tx); err != nil {
						return err
					}
				}
				if err := reconcileCoveredVersions(tx, tx); err != nil {
					return err
				}
				if dryRun {
					return ErrImportDryRun
				}
				return nil
			}
			if _, ok := included[record.Type]; !ok {
				return fmt.Errorf("undeclared Library record type %q", record.Type)
			}
			if err := importCatalogRecord(ctx, tx, &record); err != nil {
				return fmt.Errorf("import record %d: %w", number, err)
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("missing Library end record")
	})
}

func validateImportJSONText(data []byte) error {
	// encoding/json substitutes U+FFFD for bad UTF-8 and lone UTF-16 surrogate escapes.
	if !utf8.Valid(data) {
		return fmt.Errorf("Library JSON contains invalid UTF-8")
	}

	// Check only Unicode escapes; the normal JSON decoder still owns JSON syntax.
	for index := 0; index < len(data); index++ {
		if data[index] != '\\' {
			continue
		}
		index++
		if index+4 >= len(data) || data[index] != 'u' {
			continue
		}
		value, err := strconv.ParseUint(string(data[index+1:index+5]), 16, 16)
		if err != nil {
			return fmt.Errorf("invalid Library JSON Unicode escape, %w", err)
		}
		index += 4
		if value < 0xd800 || value > 0xdfff {
			continue
		}
		if value >= 0xdc00 || index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
			return fmt.Errorf("Library JSON contains an unpaired Unicode surrogate")
		}
		low, err := strconv.ParseUint(string(data[index+3:index+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return fmt.Errorf("Library JSON contains an unpaired Unicode surrogate")
		}
		index += 6
	}
	return nil
}

func importCatalogRecord(ctx context.Context, tx *gorm.DB, record *jsonlInputRecord) error {
	// Resolve the declared record type; identity-owning records apply their local import rules.
	var value any
	var required []string
	switch record.Type {
	case recordTypeFile:
		file := new(File)
		if err := decodeCatalogJSON(record.Data, file, "created_at_ns", "updated_at_ns"); err != nil {
			return err
		}
		if (file.ID <= 0 && file.ID != TrashFileID) || (file.Kind != entity.FileKind_FILE_KIND_REGULAR && file.Kind != entity.FileKind_FILE_KIND_DIRECTORY) {
			return fmt.Errorf("invalid imported File identity")
		}
		return insertJSONLFiles(ctx, tx, []*File{file})
	case recordTypeMedia:
		media := new(Media)
		if err := decodeCatalogJSON(record.Data, media, "created_at_ns"); err != nil {
			return err
		}
		if media.ID <= 0 {
			return fmt.Errorf("Media ID is required")
		}
		if err := validateMedia(media); err != nil {
			return err
		}
		return tx.Create(media).Error
	case recordTypeLocation:
		source := new(Location)
		if err := decodeCatalogJSON(record.Data, source, "created_at_ns", "updated_at_ns", "last_sync_at_ns"); err != nil {
			return err
		}
		if source.ID <= 0 {
			return fmt.Errorf("Location ID is required")
		}
		if source.Config == nil {
			return fmt.Errorf("Location configuration is missing; convert this pre-v1 backup before import")
		}
		if err := validateLocation(source); err != nil {
			return err
		}
		source.Config.Ignore = WithDefaultLocationIgnore(source.Config.Ignore)
		source.LastJobID, source.LastSyncJobID, source.Revision = 0, 0, 0
		// Preserve explicit zero timestamps while retaining normal Location save hooks.
		return tx.Session(&gorm.Session{NowFunc: func() time.Time { return time.Unix(0, 0) }}).Create(source).Error
	case recordTypePosition:
		value = new(Position)
		required = []string{"mtime_ns", "written_at_ns", "checked_at_ns"}
	case recordTypeFileVersion:
		value = new(FileVersion)
		required = []string{"mtime_ns"}
	case recordTypeFileVersionArchive:
		value = new(FileVersionArchive)
		required = []string{"archived_at_ns"}
	case recordTypeFileLocation:
		value = new(FileLocation)
		required = []string{"mtime_ns"}
	case recordTypeTracking:
		value = new(FileTrackingKey)
		required = []string{"observed_at_ns"}
	default:
		return fmt.Errorf("unknown Library entity %q", record.Type)
	}

	// Validate dependent facts before insert and drop installation-local executable state.
	if err := decodeCatalogJSON(record.Data, value, required...); err != nil {
		return err
	}
	switch row := value.(type) {
	case *Position:
		if row.ID <= 0 || row.MediaID <= 0 {
			return fmt.Errorf("invalid Position identity")
		}
		if err := entity.ValidateRelativePath(strings.TrimSuffix(row.Path, "/")); err != nil {
			return err
		}
		if _, ok := entity.PositionHealth_name[int32(row.Health)]; !ok {
			return fmt.Errorf("invalid imported Position health")
		}
		if row.Health != entity.PositionHealth_POSITION_HEALTH_UNKNOWN && row.CheckedAtNS == 0 {
			return fmt.Errorf("Position health observation has no check time")
		}
		if row.Health == entity.PositionHealth_POSITION_HEALTH_HEALTHY && len(row.Hash) != 32 {
			return fmt.Errorf("healthy Position has no usable checksum baseline")
		}
		row.HealthJobID = 0
	case *FileVersion:
		if row.ID <= 0 {
			return fmt.Errorf("FileVersion ID is required")
		}
	case *FileLocation:
		if row.ParentPath != originalParent(row.Path) {
			return fmt.Errorf("inconsistent original parent path")
		}
	case *FileTrackingKey:
		if row.Kind != TrackingNative {
			return fmt.Errorf("unsupported tracking evidence %q", row.Kind)
		}
	}

	// Reference validation runs after all records, within the same import transaction.
	return tx.Create(value).Error
}

func decodeCatalogJSON(data []byte, value any, required ...string) error {
	// Reject obsolete names and numeric timestamp encodings instead of silently losing their units.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}

	// Required zero-valued instants must be explicit; missing keys indicate an incompatible shape.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if raw := fields[name]; len(raw) == 0 || raw[0] != '"' {
			return fmt.Errorf("Library timestamp %q requires a decimal string; incompatible backup shape", name)
		}
	}
	return nil
}

func validateJSONLHeader(header *jsonlInputRecord) (map[string]struct{}, error) {
	// Reject unknown formats before interpreting any replacement groups.
	if header.Type != recordTypeHeader || header.Format != jsonlFormat {
		return nil, fmt.Errorf("invalid Library header")
	}
	if header.Version != jsonlFormatVersion {
		return nil, fmt.Errorf("unsupported Library backup version %d", header.Version)
	}

	// Each allowed entity appears at most once in the snapshot declaration.
	included := map[string]struct{}{}
	allowed := map[string]bool{}
	for _, name := range catalogRecordOrder {
		allowed[name] = true
	}
	for _, name := range header.Entities {
		if !allowed[name] {
			return nil, fmt.Errorf("unknown Library entity %q", name)
		}
		if _, found := included[name]; found {
			return nil, fmt.Errorf("duplicate Library entity %q", name)
		}
		included[name] = struct{}{}
	}

	// Dependent records must be replaced together with their identity-owning groups.
	_, files := included[recordTypeFile]
	_, versions := included[recordTypeFileVersion]
	if versions && !files {
		return nil, fmt.Errorf("FileVersion import requires the File group")
	}
	if _, history := included[recordTypeFileVersionArchive]; history && !versions {
		return nil, fmt.Errorf("archive observations require FileVersions")
	}
	_, sources := included[recordTypeLocation]
	_, originals := included[recordTypeFileLocation]
	_, tracking := included[recordTypeTracking]
	if sources || originals || tracking {
		if !sources || !originals || !tracking || !files {
			return nil, fmt.Errorf("Location import requires Locations, FileLocations, tracking keys and Files together")
		}
	}
	_, positions := included[recordTypePosition]
	_, media := included[recordTypeMedia]
	if positions && !media {
		return nil, fmt.Errorf("Position import requires the Media group")
	}
	return included, nil
}

func clearImportedEntities(tx *gorm.DB, included map[string]struct{}) error {
	// Remove old organization and all saved-content history before numerical IDs can be reused.
	if _, files := included[recordTypeFile]; files {
		if err := invalidateLocationImport(tx); err != nil {
			return err
		}
		for _, model := range []any{&FileVersionArchive{}, &FileVersion{}, ModelFileTag, ModelFile} {
			if err := clearImportedModel(tx, model, "File metadata"); err != nil {
				return err
			}
		}
	}

	// Replace registered roots only when the complete original group was declared.
	if _, sources := included[recordTypeLocation]; sources {
		for _, model := range []any{&FileTrackingKey{}, &FileLocation{}, &Location{}} {
			if err := clearImportedModel(tx, model, "original metadata"); err != nil {
				return err
			}
		}
	}

	// Inventory replacement remains metadata-only and independent of the File tree.
	if _, positions := included[recordTypePosition]; positions {
		if err := clearImportedModel(tx, ModelPosition, "Positions"); err != nil {
			return err
		}
	}
	if _, media := included[recordTypeMedia]; media {
		if err := clearImportedModel(tx, ModelMedia, "Media"); err != nil {
			return err
		}
	}
	return nil
}

func detectLibraryImport(r io.Reader) (libraryImportFormat, io.Reader, error) {
	// Read only the first object key, then replay the consumed prefix to the selected decoder.
	reader := bufio.NewReader(r)
	var prefix bytes.Buffer
	read := func() (byte, error) {
		value, err := reader.ReadByte()
		if err == nil {
			prefix.WriteByte(value)
		}
		return value, err
	}
	readNonSpace := func() (byte, error) {
		for {
			value, err := read()
			if err != nil {
				return 0, err
			}
			if !strings.ContainsRune(" \t\r\n", rune(value)) {
				return value, nil
			}
		}
	}

	opening, err := readNonSpace()
	if err != nil {
		return 0, nil, fmt.Errorf("detect library import failed, %w", err)
	}
	if opening != '{' {
		return 0, nil, fmt.Errorf("detect library import failed, expected object")
	}
	quote, err := readNonSpace()
	if err != nil {
		return 0, nil, fmt.Errorf("detect library import failed, %w", err)
	}
	if quote != '"' {
		return 0, nil, fmt.Errorf("detect library import failed, expected first object key")
	}

	keyBytes := []byte{'"'}
	escaped := false
	for len(keyBytes) <= 256 {
		value, err := read()
		if err != nil {
			return 0, nil, fmt.Errorf("detect library import failed, %w", err)
		}
		keyBytes = append(keyBytes, value)
		if escaped {
			escaped = false
			continue
		}
		if value == '\\' {
			escaped = true
			continue
		}
		if value == '"' {
			break
		}
	}
	var key string
	if err := json.Unmarshal(keyBytes, &key); err != nil {
		return 0, nil, fmt.Errorf("detect library import failed, %w", err)
	}

	combined := io.MultiReader(bytes.NewReader(prefix.Bytes()), reader)
	switch key {
	case "type":
		return libraryImportJSONL, combined, nil
	case "files", "tapes", "positions":
		return libraryImportLegacyJSON, combined, nil
	default:
		return 0, nil, fmt.Errorf("detect library import failed, unknown first key %q", key)
	}
}

func exportJSONLRows[T any](
	ctx context.Context,
	db *gorm.DB,
	encoder *json.Encoder,
	recordType string,
	getID func(*T) int64,
) error {
	var cursor int64
	for {
		// Read and emit one cursor page before releasing it.
		rows := make([]*T, 0, batchSize)
		result := db.WithContext(ctx).Where("id > ?", cursor).Order("id ASC").Limit(batchSize).Find(&rows)
		if result.Error != nil {
			return fmt.Errorf("query library %s records failed, cursor=%d, %w", recordType, cursor, result.Error)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := encoder.Encode(jsonlOutputRecord{Type: recordType, Data: row}); err != nil {
				return fmt.Errorf("encode library %s record failed, id=%d, %w", recordType, getID(row), err)
			}
		}
		cursor = getID(rows[len(rows)-1])
	}
}

func (l *Library) exportJSONLFiles(ctx context.Context, encoder *json.Encoder) error {
	var cursor int64
	hasCursor := false
	for {
		// Read one bounded File page and hydrate all of its Tag relations together.
		rows := make([]*fileRow, 0, batchSize)
		request := l.db.WithContext(ctx).Order("id ASC").Limit(batchSize)
		if hasCursor {
			request = request.Where("id > ?", cursor)
		}
		result := request.Find(&rows)
		if result.Error != nil {
			return fmt.Errorf("query library file records failed, cursor=%d, %w", cursor, result.Error)
		}
		if len(rows) == 0 {
			return nil
		}
		files := fileViews(rows)
		ids := make([]int64, 0, len(files))
		for _, file := range files {
			ids = append(ids, file.ID)
		}
		tags, err := l.mGetFileTags(ctx, l.db.WithContext(ctx), ids...)
		if err != nil {
			return fmt.Errorf("query exported File Tags failed, cursor=%d, %w", cursor, err)
		}

		// Emit each File with its embedded user metadata.
		for _, file := range files {
			file.Tags = tags[file.ID]
			if err := encoder.Encode(jsonlOutputRecord{Type: recordTypeFile, Data: file}); err != nil {
				return fmt.Errorf("encode library file record failed, id=%d, %w", file.ID, err)
			}
		}
		cursor = files[len(files)-1].ID
		hasCursor = true
	}
}

func validateJSONLEnd(scanner *bufio.Scanner, recordNumber int) error {
	if scanner.Scan() {
		return fmt.Errorf("unexpected data after library end record %d", recordNumber)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read data after library end record %d failed, %w", recordNumber, err)
	}
	return nil
}

func insertJSONLRows[T any](tx *gorm.DB, recordType string, rows []*T) error {
	if len(rows) == 0 {
		return nil
	}
	if result := tx.CreateInBatches(rows, batchSize); result.Error != nil {
		return fmt.Errorf("insert library %s records failed, %w", recordType, result.Error)
	}
	return nil
}

func insertJSONLFiles(ctx context.Context, tx *gorm.DB, files []*File) error {
	if len(files) == 0 {
		return nil
	}

	// Validate the complete annotation group before insertion.
	rows := make([]*FileTag, 0)
	persisted := make([]*fileRow, 0, len(files))
	for _, file := range files {
		if err := validateFileNote(file.Note); err != nil {
			return fmt.Errorf("validate imported File %d note failed, %w", file.ID, err)
		}
		tags, err := normalizeTags(file.Tags)
		if err != nil {
			return fmt.Errorf("validate imported File %d Tags failed, %w", file.ID, err)
		}
		file.Tags = tags
		for _, tag := range tags {
			rows = append(rows, &FileTag{FileID: file.ID, Tag: tag})
		}
		persisted = append(persisted, fileRowFromFile(file))
	}

	// Insert the File identities before their dependent Tag relations.
	// Imported zero timestamps remain zero without disabling name-validation hooks.
	insert := tx.Session(&gorm.Session{NowFunc: func() time.Time { return time.Unix(0, 0) }})
	if err := insertJSONLRows(insert, recordTypeFile, persisted); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if result := tx.WithContext(ctx).CreateInBatches(rows, batchSize); result.Error != nil {
		return fmt.Errorf("insert library File Tag records failed, %w", result.Error)
	}
	return nil
}
