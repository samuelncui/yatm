package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestIdleRunnerFilesReleaseDescriptorsWithoutReplacingHandles(t *testing.T) {
	exe := New(nil, nil, nil, Paths{Work: t.TempDir()}, Scripts{}, nil)
	log, err := exe.NewLogWriter(context.Background(), 1)
	require.NoError(t, err)
	defer func() { require.NoError(t, log.Close()) }()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "job.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, closeGORMDB(db)) }()
	connections, err := db.DB()
	require.NoError(t, err)

	// Active work retains one connection and lazily opens one append-only log file.
	require.NoError(t, SetRunnerFilesActive(db, log, true))
	require.NoError(t, db.Exec("CREATE TABLE counter (value INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO counter VALUES (7)").Error)
	_, err = log.Write([]byte("active\n"))
	require.NoError(t, err)
	require.NotNil(t, log.file)
	require.Equal(t, 1, connections.Stats().OpenConnections)

	// A borrowed connection is safe across settlement and closes when its reader returns.
	conn, err := connections.Conn(context.Background())
	require.NoError(t, err)
	require.NoError(t, SetRunnerFilesActive(db, log, false))
	require.Nil(t, log.file)
	var value int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT value FROM counter").Scan(&value))
	require.Equal(t, 7, value)
	require.NoError(t, conn.Close())
	require.Zero(t, connections.Stats().OpenConnections)

	// Idle reads and diagnostics use the same objects and leave no descriptors behind.
	require.NoError(t, db.Raw("SELECT value FROM counter").Scan(&value).Error)
	require.Equal(t, 7, value)
	require.Zero(t, connections.Stats().OpenConnections)
	_, err = log.Write([]byte("idle\n"))
	require.NoError(t, err)
	require.Nil(t, log.file)
	require.NoError(t, SetRunnerFilesActive(db, log, true))
	require.NoError(t, db.Raw("SELECT value FROM counter").Scan(&value).Error)
	require.Equal(t, 1, connections.Stats().OpenConnections)
}

func TestJobLogWriterSerializesWritesAndActivityChanges(t *testing.T) {
	exe := New(nil, nil, nil, Paths{Work: t.TempDir()}, Scripts{}, nil)
	writer, err := exe.NewLogWriter(context.Background(), 1)
	require.NoError(t, err)
	var workers sync.WaitGroup
	failures := make(chan error, 3)
	for i := 0; i < 2; i++ {
		workers.Go(func() {
			for j := 0; j < 100; j++ {
				if _, err := writer.Write([]byte("entry\n")); err != nil {
					failures <- err
					return
				}
			}
		})
	}
	workers.Go(func() {
		for j := 0; j < 100; j++ {
			if err := writer.setActive(j%2 == 0); err != nil {
				failures <- err
				return
			}
		}
	})
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	content, err := os.ReadFile(exe.logPath(1))
	require.NoError(t, err)
	require.Len(t, content, 200*len("entry\n"))
	_, err = writer.Write([]byte("closed"))
	require.ErrorIs(t, err, os.ErrClosed)
}
