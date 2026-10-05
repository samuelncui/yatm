package library

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileListRejectsOtherOperationCursors(t *testing.T) {
	// Equal bound text must not let another operation's cursor act as a directory continuation.
	_, l := newTestLibrary(t)
	for _, kind := range []byte{searchCursorKind, tagCursorKind, duplicateGroupCursorKind, duplicateMemberCursorKind} {
		cursor := encodePageCursor(kind, "0/1", "61", 0)
		_, err := l.ListFiles(context.Background(), 0, entity.FileScope_FILE_SCOPE_ALL, cursor, 1)
		require.Error(t, err, "cursor kind %d", kind)
	}
}
