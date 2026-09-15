package executor

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUtil_DB(t *testing.T) {
	// Only explicitly identified published bundles may open a Job database.
	tempDir := t.TempDir()
	exe := &Executor{paths: Paths{Work: tempDir}}
	require.NoError(t, os.MkdirAll(path.Join(tempDir, "jobs", "1"), 0o755))
	require.NoError(t, os.WriteFile(path.Join(tempDir, "jobs", "1", "state.db"), nil, 0o644))
	metadata, err := json.Marshal(dataformat.NewBundle(1, time.Unix(1, 0)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path.Join(tempDir, "jobs", "1", "job.json"), metadata, 0o644))

	// Valid metadata admits the existing state without altering its identity.
	db, err := exe.NewStateDB(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, db)
	require.NoError(t, closeGORMDB(db))
}

func TestUtil_Key(t *testing.T) {
	tempDir := t.TempDir()
	exe := &Executor{paths: Paths{Work: tempDir}}

	enc, keyPath, recycle, err := exe.NewKey()
	assert.NoError(t, err)
	assert.NotEmpty(t, enc)
	assert.NotEmpty(t, keyPath)

	cmd := exe.MakeEncryptCmd(context.Background(), "/dev/nst0", keyPath, "BARCODE", "NAME")
	assert.NotNil(t, cmd)

	keyPath2, recycle2, err := exe.RestoreKey("v1:test")
	assert.NoError(t, err)
	assert.NotEmpty(t, keyPath2)
	recycle2()
	recycle()
}

func TestUtil_Log(t *testing.T) {
	// Create the Job root without any legacy log subdirectory.
	tempDir := t.TempDir()
	exe := &Executor{paths: Paths{Work: tempDir}}

	os.MkdirAll(path.Join(tempDir, "jobs", "1"), 0755)

	// Append the log directly under the Job root.
	w, err := exe.NewLogWriter(context.Background(), 1)
	assert.NoError(t, err)
	w.Write([]byte("test log"))
	w.Close()
	assert.FileExists(t, path.Join(tempDir, "jobs", "1", "job.log"))
	assert.NoDirExists(t, path.Join(tempDir, "jobs", "1", "logs"))

	// Read the same root-level log through the public API.
	r, err := exe.NewLogReader(context.Background(), 1)
	assert.NoError(t, err)
	buf := make([]byte, 100)
	n, _ := r.Read(buf)
	assert.Equal(t, "test log", string(buf[:n]))
	r.Close()
}

func TestUtil_Progress(t *testing.T) {
	// Seed a durable baseline and one deterministic active transfer.
	now := time.Unix(100, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }
	p.SetGlobalTotal(200, 10)
	p.SetGlobalCopied(20, 1)
	p.StartSession()
	p.UpdateSessionCurrent(100, 5)
	now = now.Add(2 * time.Second)

	// Report current-attempt and observed-history averages from transfer time only.
	ent := p.ToEntity()
	assert.Equal(t, int64(200), ent.TotalBytes)
	assert.Equal(t, int64(10), ent.TotalFiles)
	assert.Equal(t, int64(120), ent.CopiedBytes)
	assert.Equal(t, int64(6), ent.CopiedFiles)
	assert.Equal(t, int64(50), ent.AverageSpeed)
	assert.Equal(t, int64(50), ent.HistoricalAverageSpeed)

	// Keep completed-session throughput available while resetting current speed.
	p.CommitSession()
	ent = p.ToEntity()
	assert.Zero(t, ent.AverageSpeed)
	assert.Equal(t, int64(50), ent.HistoricalAverageSpeed)
}

func TestWriteReport(t *testing.T) {
	// Create an otherwise empty Job root for report publication.
	ctx := context.Background()
	exe := &Executor{paths: Paths{Work: t.TempDir()}}
	require.NoError(t, os.MkdirAll(path.Join(exe.paths.Work, "jobs", "1"), 0o755))

	// Publish the report under its normalized per-Tape path.
	expected := []byte(`{"tape":"ABC001"}`)
	require.NoError(t, exe.WriteReport(ctx, 1, " abc001 ", expected))
	actual, err := os.ReadFile(path.Join(exe.paths.Work, "jobs", "1", "tapes", "ABC001", "yatm-report.json"))
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	// Barcodes cannot escape the per-Job tapes directory.
	for _, barcode := range []string{"", ".", "../ABC001", "A/B", "ABC-01", "ABC0017"} {
		require.Error(t, exe.WriteReport(ctx, 1, barcode, expected))
	}
}
