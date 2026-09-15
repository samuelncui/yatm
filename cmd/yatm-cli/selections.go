package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

type scopeOptions struct {
	Scope string `long:"scope" default:"all" choice:"all" choice:"saved" choice:"default" description:"Library visibility: all, saved versions only, or stored preference"`
}

type selectionOptions struct {
	scopeOptions
	FileIDs   []int64  `long:"file-id" description:"Library File or directory ID; repeatable; 0 selects the root"`
	Locations []string `long:"location" value-name:"ID:PATH" description:"Location ID and source-relative file or directory path; repeatable"`
}

type filePageOptions struct {
	scopeOptions
	Cursor string `long:"cursor" description:"Stable directory page cursor"`
	Limit  int32  `long:"limit" default:"100" description:"Maximum direct children, 1 to 500"`
}

func (o scopeOptions) fileScope() entity.FileScope {
	// CLI defaults are explicit so scripts do not depend on a browser preference.
	switch o.Scope {
	case "default":
		return entity.FileScope_FILE_SCOPE_DEFAULT
	case "saved":
		return entity.FileScope_FILE_SCOPE_SAVED
	default:
		return entity.FileScope_FILE_SCOPE_ALL
	}
}

func (o selectionOptions) selections() ([]*entity.FileSelection, error) {
	// Preserve selected roots; the server expands and deduplicates the frozen manifest.
	result := make([]*entity.FileSelection, 0, len(o.FileIDs)+len(o.Locations))
	for _, id := range o.FileIDs {
		if id < 0 {
			return nil, usageError(fmt.Errorf("File ID must not be negative"))
		}
		result = append(result, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: o.fileScope()})
	}
	for _, value := range o.Locations {
		idText, path, ok := strings.Cut(value, ":")
		id, err := strconv.ParseInt(idText, 10, 64)
		if !ok || err != nil || id <= 0 {
			return nil, usageError(fmt.Errorf("location must be ID:PATH with a positive Location ID, value=%q", value))
		}
		result = append(result, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: id, Path: path}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	return result, nil
}
