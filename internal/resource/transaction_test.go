package resource

import (
	"errors"
	"path/filepath"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWriteTransactionRepeatsOnlyAbortedOutermostTransactions(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, CloseDB(db)) })
	deadlock := &mysqldriver.MySQLError{Number: mysqlDeadlock}
	timeout := &mysqldriver.MySQLError{Number: mysqlLockWaitTimeout}

	// A deadlock or lock wait timeout repeats the whole transaction until it succeeds.
	attempts := 0
	require.NoError(t, WriteTransaction(db, func(*gorm.DB) error {
		attempts++
		if attempts == 1 {
			return deadlock
		}
		if attempts == 2 {
			return timeout
		}
		return nil
	}))
	require.Equal(t, 3, attempts)

	// Repetition is bounded, and other errors return after one attempt.
	attempts = 0
	require.ErrorIs(t, WriteTransaction(db, func(*gorm.DB) error { attempts++; return deadlock }), deadlock)
	require.Equal(t, writeRetries+1, attempts)
	other := errors.New("other")
	attempts = 0
	require.ErrorIs(t, WriteTransaction(db, func(*gorm.DB) error { attempts++; return other }), other)
	require.Equal(t, 1, attempts)

	// A nested transaction cannot repeat alone; its enclosing transaction owns the retry.
	attempts = 0
	inner := 0
	require.NoError(t, WriteTransaction(db, func(tx *gorm.DB) error {
		attempts++
		err := WriteTransaction(tx, func(*gorm.DB) error {
			inner++
			if attempts == 1 {
				return deadlock
			}
			return nil
		})
		return err
	}))
	require.Equal(t, 2, attempts)
	require.Equal(t, 2, inner)
}
