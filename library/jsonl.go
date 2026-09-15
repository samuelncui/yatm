package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

const (
	JSONLContentType              = "application/x-ndjson"
	jsonlFormat                   = "yatm-library-backup"
	jsonlFormatVersion            = 1
	maxJSONLRecordSize            = 1 << 20
	recordTypeHeader              = "header"
	recordTypeMedia               = "media"
	recordTypeLegacyTape          = "tape"
	recordTypeFile                = "file"
	recordTypePosition            = "position"
	recordTypeEnd                 = "end"
	recordTypeOnlineSource        = "location"
	recordTypeOnlinePosition      = "file_location"
	recordTypeFileVersion         = "file_version"
	recordTypeFileVersionArchive  = "file_version_archive"
	recordTypeTracking            = "file_tracking_key"
	recordTypeRestoreResult       = "restore_result"
	recordTypeFileOperationResult = "file_operation_result"
)

type jsonlOutputRecord struct {
	Type     string           `json:"type"`
	Format   string           `json:"format,omitempty"`
	Version  int              `json:"version,omitempty"`
	Entities []string         `json:"entities,omitempty"`
	Data     any              `json:"data,omitempty"`
	Settings *LibrarySettings `json:"settings,omitempty"`
}
type jsonlInputRecord struct {
	Type     string           `json:"type"`
	Format   string           `json:"format,omitempty"`
	Version  int              `json:"version,omitempty"`
	Entities []string         `json:"entities,omitempty"`
	Data     json.RawMessage  `json:"data,omitempty"`
	Settings *LibrarySettings `json:"settings,omitempty"`
}
type libraryImportFormat uint8

const (
	libraryImportJSONL libraryImportFormat = iota + 1
	libraryImportLegacyJSON
)

// Export reads every selected entity from one consistent metadata snapshot.
func (l *Library) Export(ctx context.Context, w io.Writer, types []entity.LibraryEntityType) error {
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return (&Library{db: tx, online: l.online}).exportSnapshot(ctx, w, types)
	})
}

func (l *Library) exportSnapshot(ctx context.Context, w io.Writer, types []entity.LibraryEntityType) error {
	// Expand requested entity groups to their dependent saved-content and provenance records.
	selected := map[string]struct{}{}
	for _, kind := range types {
		switch kind {
		case entity.LibraryEntityType_MEDIA:
			selected[recordTypeMedia] = struct{}{}
		case entity.LibraryEntityType_POSITION:
			selected[recordTypePosition] = struct{}{}
		case entity.LibraryEntityType_FILE:
			selected[recordTypeFile], selected[recordTypeFileVersion] = struct{}{}, struct{}{}
			selected[recordTypeFileVersionArchive] = struct{}{}
		case entity.LibraryEntityType_LOCATION:
			selected[recordTypeOnlineSource] = struct{}{}
			selected[recordTypeFileOperationResult] = struct{}{}
		case entity.LibraryEntityType_FILE_LOCATION:
			selected[recordTypeOnlinePosition], selected[recordTypeTracking] = struct{}{}, struct{}{}
			selected[recordTypeRestoreResult] = struct{}{}
		case entity.LibraryEntityType_FILE_VERSION:
			selected[recordTypeFileVersion] = struct{}{}
			selected[recordTypeFileVersionArchive] = struct{}{}
		case entity.LibraryEntityType_FILE_TRACKING_KEY:
			selected[recordTypeTracking] = struct{}{}
		case entity.LibraryEntityType_RESTORE_RESULT:
			selected[recordTypeRestoreResult] = struct{}{}
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
	if _, ok := selected[recordTypeFile]; ok {
		var err error
		header.Settings, err = l.GetLibrarySettings(ctx)
		if err != nil {
			return err
		}
	}
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
		case recordTypeOnlineSource:
			err = exportJSONLRows(ctx, l.db, encoder, name, func(v *Location) int64 { return v.ID })
		case recordTypeOnlinePosition, recordTypeTracking:
			err = l.exportOriginalRelations(ctx, encoder, name)
		case recordTypeRestoreResult:
			err = l.exportRestoreResults(ctx, encoder)
		case recordTypeFileOperationResult:
			err = l.exportFileOperationResults(ctx, encoder)
		}
		if err != nil {
			return err
		}
	}
	return encoder.Encode(jsonlOutputRecord{Type: recordTypeEnd})
}

var catalogRecordOrder = []string{recordTypeMedia, recordTypeFile, recordTypeFileVersion, recordTypeFileVersionArchive, recordTypePosition, recordTypeOnlineSource, recordTypeOnlinePosition, recordTypeTracking, recordTypeRestoreResult, recordTypeFileOperationResult}

func (l *Library) exportRestoreResults(ctx context.Context, encoder *json.Encoder) error {
	// The composite business key provides bounded, tie-safe history pagination.
	var operation string
	var item int64
	for {
		var rows []*RestoreResult
		if err := l.db.WithContext(ctx).Where("operation_id > ? OR (operation_id = ? AND item_id > ?)", operation, operation, item).
			Order("operation_id, item_id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := encoder.Encode(jsonlOutputRecord{Type: recordTypeRestoreResult, Data: row}); err != nil {
				return err
			}
		}
		last := rows[len(rows)-1]
		operation, item = last.OperationID, last.ItemID
	}
}

func (l *Library) exportOriginalRelations(ctx context.Context, encoder *json.Encoder, name string) error {
	var after int64
	for {
		// A File has at most one original and two supported tracking mechanisms.
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
		case recordTypeOnlinePosition:
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
func (l *Library) Import(ctx context.Context, input io.Reader) error {
	release, err := l.maintainOnline()
	if err != nil {
		return err
	}
	defer release()
	format, reader, err := detectLibraryImport(input)
	if err != nil {
		return err
	}
	if format == libraryImportLegacyJSON {
		return l.importLegacyJSON(ctx, reader)
	}
	return l.importJSONL(ctx, reader)
}

func (l *Library) importJSONL(ctx context.Context, input io.Reader) error {
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
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
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
		if header.Settings != nil {
			if _, ok := included[recordTypeFile]; !ok {
				return fmt.Errorf("Library settings require the File group")
			}
			settings := &LibrarySettings{ID: 1, IncludeUnbackedFiles: header.Settings.IncludeUnbackedFiles,
				AutoCollectFiles: header.Settings.AutoCollectFiles, ConfirmPermanentDelete: header.Settings.ConfirmPermanentDelete}
			view := &Library{db: tx, online: l.online}
			previous, err := view.GetLibrarySettings(ctx)
			if err != nil {
				return err
			}
			settings.Revision = previous.Revision
			if _, err := view.UpdateFileSettings(ctx, settings); err != nil {
				return err
			}
		}
		for number := 2; scanner.Scan(); number++ {
			var record jsonlInputRecord
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
				if err := validateRestoreResultImport(tx); err != nil {
					return err
				}
				if _, included := included[recordTypePosition]; included {
					if err := rebuildPositionIndex(ctx, tx); err != nil {
						return err
					}
				}
				return reconcileCoveredVersions(tx, tx)
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

func importCatalogRecord(ctx context.Context, tx *gorm.DB, record *jsonlInputRecord) error {
	// Resolve the declared record type; identity-owning records apply their local import rules.
	var value any
	switch record.Type {
	case recordTypeFile:
		file := new(File)
		if err := json.Unmarshal(record.Data, file); err != nil {
			return err
		}
		if (file.ID <= 0 && file.ID != TrashFileID) || (file.Kind != entity.FileKind_FILE_KIND_REGULAR && file.Kind != entity.FileKind_FILE_KIND_DIRECTORY) {
			return fmt.Errorf("invalid imported File identity")
		}
		return insertJSONLFiles(ctx, tx, []*File{file})
	case recordTypeMedia:
		media := new(Media)
		if err := json.Unmarshal(record.Data, media); err != nil {
			return err
		}
		if media.ID <= 0 {
			return fmt.Errorf("Media ID is required")
		}
		if err := validateMedia(media); err != nil {
			return err
		}
		return tx.Create(media).Error
	case recordTypeOnlineSource:
		source := new(Location)
		if err := json.Unmarshal(record.Data, source); err != nil {
			return err
		}
		if source.ID <= 0 {
			return fmt.Errorf("Location ID is required")
		}
		if err := validateOnlineSource(source); err != nil {
			return err
		}
		source.Binding = entity.OnlineBinding_UNCONFIRMED
		source.BindingToken = uuid.NewString()
		source.LastJobID, source.LastSyncJobID, source.Revision = 0, 0, 0
		return tx.Create(source).Error
	case recordTypePosition:
		value = new(Position)
	case recordTypeFileVersion:
		value = new(FileVersion)
	case recordTypeFileVersionArchive:
		value = new(FileVersionArchive)
	case recordTypeOnlinePosition:
		value = new(FileLocation)
	case recordTypeTracking:
		value = new(FileTrackingKey)
	case recordTypeRestoreResult:
		value = new(RestoreResult)
	case recordTypeFileOperationResult:
		value = new(FileOperationResult)
	default:
		return fmt.Errorf("unknown Library entity %q", record.Type)
	}

	// Validate dependent facts before insert and remove executable installation-local bindings.
	if err := json.Unmarshal(record.Data, value); err != nil {
		return err
	}
	switch row := value.(type) {
	case *FileOperationResult:
		if _, err := uuid.Parse(row.OperationID); err != nil {
			return fmt.Errorf("invalid imported file operation, %w", err)
		}
		if row.ItemID <= 0 || row.LocationID <= 0 || row.CompletedAt <= 0 {
			return fmt.Errorf("invalid imported file operation result")
		}
		if row.Kind < entity.FileOperationKind_MOVE || row.Kind > entity.FileOperationKind_DELETE {
			return fmt.Errorf("invalid imported file operation kind")
		}
		for _, value := range []string{row.SourcePath, row.TargetPath} {
			if value != "" {
				if err := entity.ValidateRelativePath(value); err != nil {
					return err
				}
			}
		}
		// Operation provenance survives unregister; its historical Location ID is not executable ownership.
		row.BindingToken = ""
		row.AdmittedFileID = nil
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
		if row.Health != entity.PositionHealth_POSITION_HEALTH_UNKNOWN && row.CheckedAt <= 0 {
			return fmt.Errorf("Position health observation has no check time")
		}
		if row.Health == entity.PositionHealth_HEALTHY && len(row.Hash) != 32 {
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
		row.ObservedBindingToken = ""
	case *RestoreResult:
		if _, err := uuid.Parse(row.OperationID); err != nil {
			return fmt.Errorf("invalid imported Restore operation, %w", err)
		}
		if row.ItemID <= 0 || row.SourceFileID <= 0 || row.SourceVersionID <= 0 || row.LocationID <= 0 || row.ResultFileID < 0 {
			return fmt.Errorf("invalid imported Restore result identifiers")
		}
		if err := entity.ValidateRelativePath(row.Path); err != nil {
			return err
		}
		if len(row.Signature) == 0 || len(row.ExpectedHash) != 32 || len(row.ActualHash) != 32 || row.ExpectedSize < 0 || row.ActualSize < 0 {
			return fmt.Errorf("imported Restore result has incomplete content facts")
		}
		if row.Outcome != RestoreDamaged && (row.ExpectedSize != row.ActualSize || !bytes.Equal(row.ExpectedHash, row.ActualHash)) {
			return fmt.Errorf("verified Restore result has mismatched content facts")
		}
		switch row.Outcome {
		case RestoreReconnected, RestoreNewFile:
			if row.ResultFileID == 0 {
				return fmt.Errorf("linked Restore result has no File")
			}
			if (row.Outcome == RestoreReconnected) != (row.ResultFileID == row.SourceFileID) {
				return fmt.Errorf("Restore result outcome disagrees with its File identity")
			}
		case RestoreIgnored, RestoreDamaged:
			if row.ResultFileID != 0 {
				return fmt.Errorf("unlinked Restore result has a File")
			}
		default:
			return fmt.Errorf("unknown imported Restore outcome %q", row.Outcome)
		}
		row.BindingToken = ""
	case *FileTrackingKey:
		if row.FileID <= 0 || row.LocationID <= 0 || len(row.KeyValue) == 0 || len(row.KeyValue) > 256 {
			return fmt.Errorf("invalid tracking key")
		}
		if row.Kind != TrackingNative && row.Kind != TrackingUUID {
			return fmt.Errorf("unsupported tracking mechanism %q", row.Kind)
		}
	}

	// Reference validation runs after all records, within the same import transaction.
	return tx.Create(value).Error
}

func validateJSONLHeader(header *jsonlInputRecord) (map[string]struct{}, error) {
	// Reject unknown formats before interpreting any replacement groups.
	if header.Type != recordTypeHeader || header.Format != jsonlFormat {
		return nil, fmt.Errorf("invalid Library header")
	}
	if header.Version != jsonlFormatVersion {
		return nil, fmt.Errorf("unsupported Library backup revision %d", header.Version)
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
	_, sources := included[recordTypeOnlineSource]
	if _, operations := included[recordTypeFileOperationResult]; operations && !sources {
		return nil, fmt.Errorf("file operation results require Locations")
	}
	_, restored := included[recordTypeRestoreResult]
	if restored && (!files || !versions || !sources) {
		return nil, fmt.Errorf("Restore results require Files, FileVersions and Locations")
	}
	_, originals := included[recordTypeOnlinePosition]
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
		if err := invalidateOnlineImport(tx); err != nil {
			return err
		}
		for _, model := range []any{&FileOperationResult{}, &RestoreResult{}, &FileVersionArchive{}, &FileVersion{}, ModelFileTag, ModelFile} {
			if err := clearImportedModel(tx, model, "File metadata"); err != nil {
				return err
			}
		}
	}

	// Replace registered roots only when the complete original group was declared.
	if _, sources := included[recordTypeOnlineSource]; sources {
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
		files := make([]*File, 0, batchSize)
		request := l.db.WithContext(ctx).Order("id ASC").Limit(batchSize)
		if hasCursor {
			request = request.Where("id > ?", cursor)
		}
		result := request.Find(&files)
		if result.Error != nil {
			return fmt.Errorf("query library file records failed, cursor=%d, %w", cursor, result.Error)
		}
		if len(files) == 0 {
			return nil
		}
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
	}

	// Insert the File identities before their dependent Tag relations.
	if err := insertJSONLRows(tx, recordTypeFile, files); err != nil {
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
