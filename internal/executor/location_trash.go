package executor

import (
	"strings"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/internal/library"
)

// LocationTrashMarker identifies a directory owned by YATM rather than user content.
const LocationTrashMarker = ".yatm-trash"

// IsLocationTrashContent distinguishes payload from the managed root and batch containers.
func IsLocationTrashContent(relative string) bool {
	parts := strings.SplitN(relative, "/", 3)
	if len(parts) != 3 || parts[0] != library.LocationTrashDirectory || parts[2] == "" {
		return false
	}
	_, err := uuid.Parse(parts[1])
	return err == nil
}
