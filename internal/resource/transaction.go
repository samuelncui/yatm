package resource

import (
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

const (
	writeRetries = 3

	mysqlDeadlock        = 1213
	mysqlLockWaitTimeout = 1205
)

// WriteTransaction runs one Catalog write transaction. A MySQL deadlock or lock wait timeout
// aborts the whole transaction, so it runs again from the start, at most writeRetries more times.
// fn must derive every result from what it reads through tx and reset captured outputs on entry.
// Inside an enclosing transaction it runs once: only the outermost transaction can be repeated.
// SQLite never reports these errors and keeps its single attempt.
func WriteTransaction(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	_, nested := db.Statement.ConnPool.(gorm.TxCommitter)
	for attempt := 0; ; attempt++ {
		err := db.Transaction(fn)
		if nested || attempt == writeRetries || !retryableWrite(err) {
			return err
		}
	}
}

func retryableWrite(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == mysqlDeadlock || mysqlErr.Number == mysqlLockWaitTimeout
}
