package resource

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteBusyTimeoutOnReplacementConnections(t *testing.T) {
	// Job and temporary handles use OpenSQLite without the Catalog DSN policy.
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "job.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(db)) })
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxIdleConns(0)

	// Every replacement connection must retain the driver's bounded lock wait.
	for range 3 {
		var timeout int
		require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error)
		require.Equal(t, 5000, timeout)
	}
}

func TestSQLiteWaitsForAnotherHandleToFinishWriting(t *testing.T) {
	// A runner and a short-lived Job reader can hold separate handles to one file.
	path := filepath.Join(t.TempDir(), "job.db")
	first, err := OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(first)) })
	second, err := OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(second)) })
	require.NoError(t, first.Exec("CREATE TABLE example (value INTEGER)").Error)
	require.NoError(t, first.Exec("INSERT INTO example VALUES (1)").Error)
	tx := first.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("UPDATE example SET value = 2").Error)

	// The other write remains pending until the owner commits, then succeeds normally.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		done <- second.WithContext(ctx).Exec("UPDATE example SET value = 3").Error
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("write returned before the lock was released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	require.NoError(t, tx.Commit().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var value int
	require.NoError(t, first.Raw("SELECT value FROM example").Scan(&value).Error)
	require.Equal(t, 3, value)
}
