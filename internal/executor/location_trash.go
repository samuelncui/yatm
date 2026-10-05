package executor

import (
	"strings"

	"github.com/google/uuid"
)

// LocationTrashDirectory is the reserved, browsable recycle bin of a Location.
const LocationTrashDirectory = ".trash"

// LocationTrashMarker identifies a directory owned by YATM rather than user content.
const LocationTrashMarker = ".yatm-trash"

// IsLocationTrashPath identifies content excluded from collection and business selections.
// It must not be used as a filesystem access exclusion: browsing and moving out are allowed.
func IsLocationTrashPath(relative string) bool {
	return relative == LocationTrashDirectory || strings.HasPrefix(relative, LocationTrashDirectory+"/")
}

// IsLocationTrashContent distinguishes payload from the managed root and batch containers.
func IsLocationTrashContent(relative string) bool {
	parts := strings.SplitN(relative, "/", 3)
	if len(parts) != 3 || parts[0] != LocationTrashDirectory || parts[2] == "" {
		return false
	}
	_, err := uuid.Parse(parts[1])
	return err == nil
}
