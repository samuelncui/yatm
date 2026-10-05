package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestExportLibraryReleasesResources(t *testing.T) {
	for _, name := range []string{"success", "invalid catalog", "invalid types", "output failure", "canceled"} {
		t.Run(name, func(t *testing.T) {
			// Keep command globals and all created data inside this invocation's fixture.
			root := t.TempDir()
			t.Chdir(root)
			hooks := logrus.StandardLogger().ReplaceHooks(logrus.LevelHooks{})
			t.Cleanup(func() { logrus.StandardLogger().ReplaceHooks(hooks) })
			gc := debug.SetGCPercent(-1)
			t.Cleanup(func() { debug.SetGCPercent(gc) })
			database := filepath.Join(root, "library.db")
			configuration := filepath.Join(root, "config.yaml")
			require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(
				"database:\n  dialect: sqlite\n  dsn: %q\npaths:\n  work: %q\n", database, root,
			)), 0600))
			if name == "invalid catalog" {
				db, err := resource.OpenSQLite(database)
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				defer sqlDB.Close()
				require.NoError(t, db.Exec("CREATE TABLE legacy (id INTEGER)").Error)
				require.NoError(t, sqlDB.Close())
			}

			// Exercise exits both before and after acquiring an output handle.
			types, output := "file", filepath.Join(root, "export.jsonl")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "invalid types":
				types = "unknown"
			case "output failure":
				output = filepath.Join(root, "missing", "export.jsonl")
			case "canceled":
				cancel()
			}
			before, err := os.ReadDir("/dev/fd")
			if err != nil {
				t.Skip("descriptor inventory unavailable")
			}
			err = exportLibrary(ctx, configuration, types, output)
			if name == "success" {
				require.NoError(t, err)
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				require.Contains(t, string(data), "yatm-library-backup")
			} else {
				require.Error(t, err)
				if name == "canceled" {
					require.ErrorIs(t, err, context.Canceled)
				}
			}

			// SQLite, the rotating log and output close before returning, without finalizer help.
			after, err := os.ReadDir("/dev/fd")
			require.NoError(t, err)
			require.LessOrEqual(t, len(after), len(before))
		})
	}
}
