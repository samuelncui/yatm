package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/internal/config"
)

func runConfigurationPhase(ctx context.Context, phase string, conf *config.Config, filename, planFile string, stopped bool) error {
	// The supported installer holds admission closed before checking or applying reviewed inputs.
	if phase != "config-plan" && !stopped {
		return fmt.Errorf("%s requires -service-stopped", phase)
	}
	if planFile == "" {
		return fmt.Errorf("%s requires -plan-file", phase)
	}
	configPath, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	planPath, err := filepath.Abs(planFile)
	if err != nil {
		return err
	}
	if configPath == planPath {
		return fmt.Errorf("plan file must not replace config.yaml")
	}
	db, err := openMigrationDB(conf, phase != "config-apply")
	if err != nil {
		return err
	}
	if db != nil {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		defer sqlDB.Close()
	}

	// Review artifacts contain configuration values; create them exclusively with private permissions.
	if phase == "config-plan" {
		plan, err := planConfiguration(ctx, db, filename)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(planFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(append(data, '\n')); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}

	// Recompute the exact plan instead of trusting edited JSON or a stale confirmation.
	data, err := readConfigurationFile(planFile)
	if err != nil {
		return err
	}
	var plan configurationPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	if phase == "config-check" {
		return checkConfigurationPlan(ctx, db, filename, &plan)
	}
	return applyConfigurationPlan(ctx, db, filename, &plan)
}
