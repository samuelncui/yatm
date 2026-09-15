package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestLoadIsReadOnlyAndDoesNotLogConfiguration(t *testing.T) {
	// Capture diagnostics while using a syntactically valid secret-bearing configuration.
	var logs bytes.Buffer
	previous := logrus.StandardLogger().Out
	logrus.SetOutput(&logs)
	t.Cleanup(func() { logrus.SetOutput(previous) })
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	data := []byte("database:\n  dialect: sqlite\n  dsn: private-credential-placeholder\npaths:\n  work: ./work\n")
	require.NoError(t, os.WriteFile(path, data, 0600))

	// Inspection and the service wrapper preserve values without creating referenced resources.
	conf, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "private-credential-placeholder", conf.Database.DSN)
	require.Equal(t, "./work", conf.Paths.Work)
	require.Equal(t, conf, GetConfig(path))
	require.NotContains(t, logs.String(), "private-credential-placeholder")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, actual)
}

func TestLoadReportsMissingAndInvalidConfiguration(t *testing.T) {
	// A missing input is an error, not a new empty installation.
	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := Load(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	// Decode errors are returned to offline tooling instead of panicking.
	require.NoError(t, os.WriteFile(path, []byte("database: ["), 0600))
	_, err = Load(path)
	require.ErrorContains(t, err, "decode config file failed")
}
