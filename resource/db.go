package resource

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func NewDBConn(dialect, dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch dialect {
	case "mysql":
		dialector = mysql.Open(dsn)
	case "sqlite":
		dialector = openSQLite(dsn)
	}

	db, err := gorm.Open(dialector)
	if err != nil {
		return nil, fmt.Errorf("new db conn fail, dialect= '%s' dsn= '%s', %w", dialect, dsn, err)
	}

	switch dialect {
	case "sqlite":
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("sqlite set config fail, dialect= '%s' dsn= '%s', %w", dialect, dsn, err)
		}

		// Prevent "database locked" errors
		sqlDB.SetMaxOpenConns(1)
	}

	return db, nil
}

func SQLEscape(sql string) string {
	dest := make([]byte, 0, 2*len(sql))
	var escape byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]

		escape = 0

		switch c {
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
		case '\032': //十进制26,八进制32,十六进制1a, /* This gives problems on Win32 */
			escape = 'Z'
		}

		if escape != 0 {
			dest = append(dest, '\\', escape)
		} else {
			dest = append(dest, c)
		}
	}

	return string(dest)
}
