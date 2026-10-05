package archive

import (
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestArchiveRejectsUnrepresentableCopyTimestamps(t *testing.T) {
	// Metadata from the successful transfer must be representable before a STAGED result is persisted.
	runner := &jobArchiveRunner{}
	item := &copyItem{mediaTarget: "target", item: &Item{ID: 1, TargetPath: "file", Data: &entity.ArchiveManifestFile{SourcePath: "source"}}}
	valid := time.Unix(1780000000, 123456)
	for _, year := range []int{1600, 2400} {
		for _, field := range []string{"mtime", "written"} {
			t.Run(fmt.Sprintf("%d/%s", year, field), func(t *testing.T) {
				result := acp.Result{Size: 7, Mode: 0644, ModTime: valid, WriteTime: valid, SHA256: make([]byte, 32), Targets: []acp.TargetResult{{Path: "target"}}}
				invalid := time.Date(year, 1, 1, 0, 0, 0, 123, time.UTC)
				if field == "mtime" {
					result.ModTime = invalid
				} else {
					result.WriteTime = invalid
				}
				outcome, err := runner.acceptCopyResult(item, result)
				require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
				require.Nil(t, outcome.copyResult)
			})
		}
	}
	outcome, err := runner.acceptCopyResult(item, acp.Result{Size: 7, Mode: 0644, ModTime: valid, WriteTime: valid.Add(time.Nanosecond), SHA256: make([]byte, 32), Targets: []acp.TargetResult{{Path: "target"}}})
	require.NoError(t, err)
	require.Equal(t, valid.UnixNano(), outcome.copyResult.ModTimeNs)
	require.Equal(t, valid.UnixNano()+1, outcome.copyResult.WriteTimeNs)
}
