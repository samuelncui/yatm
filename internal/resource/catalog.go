package resource

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gorm.io/gorm"
)

const catalogReaders = 4

// Catalog owns the configured database connections. Job and temporary databases
// retain their independent, single-connection lifetime.
type Catalog struct {
	Write *gorm.DB
	Read  *gorm.DB
}

// OpenCatalog validates the existing format before changing journal mode. The
// validator also owns application-specific checks, such as Job bundle admission.
func OpenCatalog(dialect, dsn string, wal bool, validate func(*gorm.DB) error) (_ *Catalog, rerr error) {
	// Reject contradictory configuration before opening or changing any database.
	if validate == nil {
		return nil, errors.New("Catalog format validation is required")
	}
	if wal && dialect != "sqlite" {
		return nil, errors.New("database.sqlite_wal requires a file-backed SQLite Catalog")
	}
	var memory bool
	if dialect == "sqlite" {
		var err error
		dsn, memory, err = catalogSQLiteDSN(dsn, wal)
		if err != nil {
			return nil, err
		}
	}

	// Keep partial construction owned until both the writer and readers are ready.
	write, err := NewDBConn(dialect, dsn)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{Write: write, Read: write}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, catalog.Close())
		}
	}()
	if err := validate(write); err != nil {
		return nil, err
	}
	if dialect != "sqlite" {
		return catalog, nil
	}

	// Journal mode persists across restarts, so false must actively restore DELETE.
	mode := "DELETE"
	if wal {
		mode = "WAL"
	}
	var actual string
	if err := write.Raw("PRAGMA journal_mode=" + mode).Scan(&actual).Error; err != nil {
		return nil, fmt.Errorf("set Catalog journal mode to %s failed, %w", mode, err)
	}
	if !strings.EqualFold(actual, mode) && !(memory && actual == "memory") {
		return nil, fmt.Errorf("Catalog journal mode is %s, expected %s", actual, mode)
	}
	if !wal {
		return catalog, nil
	}

	// Separate read-only connections permit reads during the sole writer's transaction.
	base, query, _ := strings.Cut(dsn, "?")
	options, err := url.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("parse Catalog connection options failed, %w", err)
	}
	options.Set("mode", "ro")
	read, err := NewDBConn("sqlite", base+"?"+options.Encode())
	if err != nil {
		return nil, fmt.Errorf("open Catalog readers failed, %w", err)
	}
	catalog.Read = read
	connections, err := read.DB()
	if err != nil {
		return nil, err
	}
	connections.SetMaxOpenConns(catalogReaders)
	connections.SetMaxIdleConns(catalogReaders)
	return catalog, nil
}

// Close releases readers before the writer so SQLite owns final checkpoint cleanup.
func (c *Catalog) Close() error {
	var result error
	if c.Read != nil && c.Read != c.Write {
		result = errors.Join(result, closeDB(c.Read))
	}
	if c.Write != nil {
		result = errors.Join(result, closeDB(c.Write))
	}
	return result
}

func closeDB(db *gorm.DB) error {
	connections, err := db.DB()
	if err != nil {
		return err
	}
	return connections.Close()
}

func catalogSQLiteDSN(dsn string, wal bool) (string, bool, error) {
	// A URI is needed for read-only readers; preserve caller-supplied URI options.
	base, query, _ := strings.Cut(dsn, "?")
	options, err := url.ParseQuery(query)
	if err != nil {
		return "", false, errors.New("invalid SQLite Catalog connection options")
	}
	memory := base == "" || base == ":memory:" || base == "file::memory:" || options.Get("mode") == "memory"
	if wal && memory {
		return "", false, errors.New("database.sqlite_wal requires a file-backed SQLite Catalog")
	}
	if mode := options.Get("mode"); mode == "ro" || options.Get("immutable") == "1" {
		return "", false, errors.New("the service Catalog must be writable")
	}

	// Journal configuration belongs to YAML and is applied only after format admission.
	wanted := "delete"
	if wal {
		wanted = "wal"
	}
	for key, values := range options {
		switch strings.ToLower(key) {
		case "_journal", "_journal_mode":
			for _, value := range values {
				if !strings.EqualFold(value, wanted) {
					return "", false, errors.New("SQLite DSN journal mode conflicts with database.sqlite_wal")
				}
			}
			delete(options, key)
		case "_sync", "_synchronous":
			for _, value := range values {
				if !strings.EqualFold(value, "FULL") && value != "2" {
					return "", false, errors.New("the Catalog requires SQLite synchronous=FULL")
				}
			}
			delete(options, key)
		case "_busy_timeout", "_timeout":
			delete(options, key)
		case "_pragma":
			kept := make([]string, 0, len(values))
			for _, value := range values {
				parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return r == '(' || r == ')' || r == '=' || r == ' ' })
				if len(parts) == 0 {
					return "", false, errors.New("empty SQLite Catalog pragma")
				}
				switch parts[0] {
				case "journal_mode":
					if len(parts) != 2 || parts[1] != wanted {
						return "", false, errors.New("SQLite DSN journal mode conflicts with database.sqlite_wal")
					}
				case "synchronous":
					if len(parts) != 2 || (parts[1] != "full" && parts[1] != "2") {
						return "", false, errors.New("the Catalog requires SQLite synchronous=FULL")
					}
				case "busy_timeout":
					// One bounded wait policy applies to every replacement connection.
				default:
					kept = append(kept, value)
				}
			}
			options[key] = kept
		}
	}

	// Connection-local settings must also apply when database/sql opens a replacement.
	for key, values := range sqliteConnectionOptions() {
		options[key] = append(options[key], values...)
	}
	// Keep slash-absolute paths in the URI path and relative names directly after file:.
	if base != "" && base != ":memory:" && !strings.HasPrefix(base, "file:") {
		base = (&url.URL{Scheme: "file", OmitHost: !strings.HasPrefix(base, "/"), Path: base}).String()
	}
	return base + "?" + options.Encode(), memory, nil
}
