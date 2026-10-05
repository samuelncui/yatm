package legacy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyLTFSIndexRoot(t *testing.T) {
	// Resolve both legacy LTFS option forms and retain the conventional fallback for dynamic scripts.
	root := t.TempDir()
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "explicit capture directory",
			script: `ltfs -o work_directory=/work -o capture_index="/captured/indexes" /mnt/ltfs`,
			want:   "/captured/indexes",
		},
		{
			name:   "work directory",
			script: `ltfs -o work_directory='/captured/work' -o capture_index /mnt/ltfs`,
			want:   "/captured/work",
		},
		{
			name:   "relative capture directory",
			script: `ltfs -o capture_index=indices /mnt/ltfs`,
			want:   filepath.Join(root, "indices"),
		},
		{
			name:   "commented option",
			script: "# ltfs -o capture_index=/old\nltfs -o capture_index=/current /mnt/ltfs",
			want:   "/current",
		},
		{
			name:   "runtime Tape directory",
			script: `ltfs -o work_directory="${TAPE_DIR}" -o capture_index="${TAPE_DIR}" /mnt/ltfs`,
			want:   filepath.Join(root, legacyLTFSIndexDirectory),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "mount")
			require.NoError(t, os.WriteFile(filename, []byte(test.script), 0o755))
			require.Equal(t, test.want, LegacyLTFSIndexRoot(filename, root))
		})
	}
}
