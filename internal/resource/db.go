package resource

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func NewDBConn(dialect, dsn string) (*gorm.DB, error) {
	// Select the configured driver without publishing connection credentials.
	var dialector gorm.Dialector
	switch dialect {
	case "mysql":
		dialector = mysql.Open(dsn)
	case "sqlite":
		dialector = openSQLite(dsn)
	default:
		return nil, fmt.Errorf("unsupported database dialect %q", dialect)
	}

	// Connection errors retain their cause but never append the full DSN.
	db, err := gorm.Open(dialector)
	if err != nil {
		return nil, fmt.Errorf("open %s database failed, %w", dialect, err)
	}

	// Serialize SQLite writes to preserve the existing connection/locking contract.
	switch dialect {
	case "sqlite":
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("configure sqlite database failed, %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
	}
	return db, nil
}

func SQLEscape(str string) string {
	runes := []rune(str)
	result := make([]rune, 0, len(runes))

	var escape rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		escape = 0
		switch r {
		case 0: /* Must be escaped for 'mysql' */
			escape = '0'
		case '\n': /* Must be escaped for logs */
			escape = 'n'
		case '\r':
			escape = 'r'
		case '\\':
			escape = '\\'
		case '\'':
			escape = '\''
		case '"': /* Better safe than sorry */
			escape = '"'
		case '\032': // This gives problems on Win32
			escape = 'Z'
		}

		if escape != 0 {
			result = append(result, '\\', escape)
		} else {
			result = append(result, r)
		}
	}

	return string(result)
}

func OpenSQLite(filename string) (*gorm.DB, error) {
	return NewDBConn("sqlite", filename)
}

type GORMScope func(db *gorm.DB) *gorm.DB
