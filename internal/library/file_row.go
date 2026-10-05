package library

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// fileRow is the persisted File identity. File adds read-only presentation facts for callers.
type fileRow struct {
	ID       int64 `gorm:"primaryKey;autoIncrement"`
	ParentID int64 `gorm:"index:idx_files_parent_name,unique"`

	Name        string          `gorm:"type:varchar(256);index:idx_files_parent_name,unique;index:idx_files_search_name"`
	Kind        entity.FileKind `gorm:"not null"`
	CreatedAtNS int64           `gorm:"autoCreateTime:nano"`
	UpdatedAtNS int64           `gorm:"autoUpdateTime:nano"`
	Note        string          `gorm:"type:varchar(4096);not null;default:''"`
}

func (*fileRow) TableName() string { return "files" }

func (row *fileRow) BeforeCreate(*gorm.DB) error {
	if err := entity.ValidatePathComponent(row.Name); err != nil {
		return fmt.Errorf("invalid File name, %w", err)
	}
	return nil
}

func (row *fileRow) BeforeUpdate(tx *gorm.DB) error {
	// Validate only fields this update will persist; partial metadata edits have no new name.
	selected, restricted := tx.Statement.SelectAndOmitColumns(false, true)
	if included, found := selected["name"]; !included && (found || restricted) {
		return nil
	}
	name := row.Name
	includeEmpty := selected["name"]
	switch fields := tx.Statement.Dest.(type) {
	case map[string]any:
		value, found := fields["name"]
		if !found {
			value, found = fields["Name"]
		}
		if !found {
			return nil
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("File name must be literal UTF-8 text")
		}
		name = text
		includeEmpty = true
	case *fileRow:
		name = fields.Name
	case fileRow:
		name = fields.Name
	}
	if name == "" && !includeEmpty {
		return nil
	}

	// Full-row saves and explicit name updates share the component syntax owner.
	if err := entity.ValidatePathComponent(name); err != nil {
		return fmt.Errorf("invalid File name, %w", err)
	}
	return nil
}

func fileRowFromFile(file *File) *fileRow {
	kind := file.Kind
	if kind == entity.FileKind_FILE_KIND_UNSPECIFIED {
		kind = entity.FileKind_FILE_KIND_REGULAR
	}
	return &fileRow{ID: file.ID, ParentID: file.ParentID, Name: file.Name, Kind: kind,
		CreatedAtNS: file.CreatedAtNS, UpdatedAtNS: file.UpdatedAtNS, Note: file.Note}
}

func (row *fileRow) file() *File {
	return &File{ID: row.ID, ParentID: row.ParentID, Name: row.Name, Kind: row.Kind,
		CreatedAtNS: row.CreatedAtNS, UpdatedAtNS: row.UpdatedAtNS, Note: row.Note}
}

func (row *fileRow) apply(file *File) {
	file.ID, file.ParentID, file.Name, file.Kind = row.ID, row.ParentID, row.Name, row.Kind
	file.CreatedAtNS, file.UpdatedAtNS, file.Note = row.CreatedAtNS, row.UpdatedAtNS, row.Note
}

func fileViews(rows []*fileRow) []*File {
	files := make([]*File, 0, len(rows))
	for _, row := range rows {
		files = append(files, row.file())
	}
	return files
}

func createFileRow(tx *gorm.DB, file *File) error {
	row := fileRowFromFile(file)
	if err := tx.Create(row).Error; err != nil {
		return err
	}
	row.apply(file)
	return nil
}

func saveFileRow(tx *gorm.DB, file *File) error {
	row := fileRowFromFile(file)
	if err := tx.Save(row).Error; err != nil {
		return err
	}
	row.apply(file)
	return nil
}
