package library

import (
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These fixtures describe already archived content, not implicit File identity.
func seedTestArchivedFacts(t *testing.T, db *gorm.DB, files ...*File) {
	t.Helper()
	for _, file := range files {
		if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
			if !file.ModTime.IsZero() {
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(file).UpdateColumn("updated_at", file.ModTime.UnixMilli()).Error)
			}
			continue
		}
		signature := file.Signature
		if len(signature) == 0 {
			signature = []byte(fmt.Sprintf("fixture-%d", file.ID))
		}
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: signature, Size: file.Size, Hash: file.Hash, Mode: file.Mode, MtimeNS: file.ModTime.UnixNano()}).Error)
	}
}
