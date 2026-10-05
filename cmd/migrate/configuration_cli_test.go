package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestConfigurationCLIReviewCheckAndApply(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "restore")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, os.Mkdir(target, 0o755))
	catalog := filepath.Join(root, "catalog.db")
	db, err := resource.OpenSQLite(catalog)
	require.NoError(t, err)
	require.NoError(t, library.New(db).AutoMigrate())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	configPath := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  dialect: sqlite\n  dsn: "+catalog+"\npaths:\n  source: "+source+"\n  target: "+target+"\n"), 0o640))
	planPath := filepath.Join(root, "review.json")
	migrator := buildConfigurationMigrator(t)
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(context.Background(), migrator, append([]string{"-config", configPath}, args...)...)
		return command.CombinedOutput()
	}

	// A review artifact is private and exclusive: repeat planning never overwrites an approved plan.
	output, err := run("-phase", "config-plan", "-plan-file", planPath)
	require.NoError(t, err, string(output))
	var initial configurationPlan
	require.NoError(t, json.Unmarshal(output, &initial))
	require.True(t, initial.Changed)
	info, err := os.Stat(planPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = run("-phase", "config-plan", "-plan-file", planPath)
	require.Error(t, err)

	// Mutating phases require both a stopped service and explicit confirmation.
	_, err = run("-phase", "config-check", "-plan-file", planPath)
	require.Error(t, err)
	_, err = run("-phase", "config-apply", "-service-stopped", "-plan-file", planPath)
	require.Error(t, err)
	output, err = run("-phase", "config-apply", "-service-stopped", "--confirm", "-plan-file", planPath)
	require.NoError(t, err, string(output))
	converted, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(converted), "source:")
	require.NotContains(t, string(converted), "target:")

	// A post-apply plan is unchanged, but its review cannot authorize a subsequently edited configuration.
	unchangedPlan := filepath.Join(root, "unchanged.json")
	output, err = run("-phase", "config-plan", "-plan-file", unchangedPlan)
	require.NoError(t, err, string(output))
	var plan configurationPlan
	require.NoError(t, json.Unmarshal(output, &plan))
	require.False(t, plan.Changed)
	require.NoError(t, os.WriteFile(configPath, append(converted, []byte("unknown: changed\n")...), 0o640))
	_, err = run("-phase", "config-check", "-service-stopped", "-plan-file", unchangedPlan)
	require.Error(t, err)
}

func buildConfigurationMigrator(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "yatm-migrate")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = "."
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return binary
}
