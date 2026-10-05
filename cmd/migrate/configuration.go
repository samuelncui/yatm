package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/config"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/migrate/legacy"
	"github.com/samuelncui/yatm/internal/preview"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type configurationPlan struct {
	Changed         bool                    `json:"changed"`
	Diff            string                  `json:"diff"`
	Settings        []string                `json:"settings"`
	OriginalHash    string                  `json:"original_hash"`
	StateHash       string                  `json:"state_hash"`
	Directory       string                  `json:"directory"`
	Filename        string                  `json:"filename"`
	YAML            string                  `json:"yaml"`
	Source          string                  `json:"source,omitempty"`
	Target          string                  `json:"target,omitempty"`
	ImportLocations bool                    `json:"import_locations"`
	Preview         *entity.PreviewSettings `json:"preview,omitempty"`
}

type configurationState struct {
	Library   configurationSetting[*entity.LibrarySettings] `json:"library"`
	Preview   configurationSetting[*entity.PreviewSettings] `json:"preview"`
	Job       configurationSetting[*entity.JobSettings]     `json:"job"`
	Locations []*library.Location                           `json:"locations"`
}

type configurationSetting[T proto.Message] struct {
	Present bool `json:"present"`
	Value   T    `json:"value,omitempty"`
}

func readConfigurationFile(filename string) ([]byte, error) {
	// Configuration and review files contain secrets and must remain bounded regular files.
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("configuration input must be a regular file: %s", filename)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("configuration input exceeds 4 MiB")
	}
	return data, nil
}

func configurationSnapshot(ctx context.Context, db *gorm.DB, source, target string) (*configurationState, error) {
	// Missing tables and absent rows both describe groups the operator has not saved.
	state := &configurationState{Locations: []*library.Location{}}
	if db == nil {
		return state, nil
	}
	db = db.WithContext(ctx)
	if db.Migrator().HasTable("settings") {
		// Read every group independently so one absent group never hides another saved value.
		module := configurationSettings(db)
		var err error
		if state.Library, err = readConfigurationSetting(ctx, module.Library); err != nil {
			return nil, err
		}
		if state.Preview, err = readConfigurationSetting(ctx, module.Preview); err != nil {
			return nil, err
		}
		if state.Job, err = readConfigurationSetting(ctx, module.Job); err != nil {
			return nil, err
		}
	}

	// Include only the legacy roots whose reviewed Location import may change.
	if (source != "" || target != "") && db.Migrator().HasTable(&library.Location{}) {
		if err := db.Select("id", "name", "executor_id", "root_path", "config", "restore_target").
			Where("executor_id = ? AND root_path IN ?", "local", []string{source, target}).Order("id").Find(&state.Locations).Error; err != nil {
			return nil, err
		}
	}
	return state, nil
}

func configurationSettings(db *gorm.DB) *settingspkg.Module {
	return settingspkg.New(db, settingspkg.PreviewDefinition{
		Default: func() (*entity.PreviewSettings, error) {
			return preview.SettingsFromConfig(preview.Config{})
		},
		Validate: preview.ValidateSettings,
	})
}

func readConfigurationSetting[T proto.Message](ctx context.Context, group settingspkg.Group[T]) (configurationSetting[T], error) {
	stored, err := group.Read(ctx)
	if err != nil {
		return configurationSetting[T]{}, err
	}
	if !stored.Present {
		return configurationSetting[T]{}, nil
	}
	return configurationSetting[T]{Present: true, Value: stored.Value}, nil
}

func configurationStateHash(state *configurationState) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func planConfiguration(ctx context.Context, db *gorm.DB, filename string) (*configurationPlan, error) {
	// Bind the preview to the exact file and original service working directory.
	original, err := readConfigurationFile(filename)
	if err != nil {
		return nil, err
	}
	conf, err := config.Load(filename)
	if err != nil {
		return nil, err
	}
	plan := &configurationPlan{OriginalHash: fmt.Sprintf("%x", sha256.Sum256(original)), Settings: []string{}}
	plan.Filename, err = filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	plan.Directory, err = os.Getwd()
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		input  string
		output *string
	}{{conf.Paths.Source, &plan.Source}, {conf.Paths.Target, &plan.Target}} {
		if item.input == "" {
			continue
		}
		*item.output, err = executor.CanonicalConfiguredPath(item.input)
		if err != nil {
			return nil, err
		}
	}
	converted, err := convertConfiguration(original, conf, plan.Source, plan.Target)
	if err != nil {
		return nil, err
	}
	plan.YAML = string(converted)
	plan.Diff, err = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(original)), B: difflib.SplitLines(plan.YAML), FromFile: "config.yaml (current)", ToFile: "config.yaml (converted)", Context: 3,
	})
	if err != nil {
		return nil, err
	}
	plan.Changed = !bytes.Equal(original, converted)

	// Existing preferences and completed imports remain authoritative, including deleted roots.
	state, err := configurationSnapshot(ctx, db, plan.Source, plan.Target)
	if err != nil {
		return nil, err
	}
	plan.StateHash, err = configurationStateHash(state)
	if err != nil {
		return nil, err
	}
	plan.ImportLocations = (plan.Source != "" || plan.Target != "")
	if plan.ImportLocations {
		for _, root := range []string{plan.Source, plan.Target} {
			if root == "" || root == plan.Target && root == plan.Source && len(plan.Settings) > 0 {
				continue
			}
			message := fmt.Sprintf("Register Location %q (preferred restore target: %t).", root, root == plan.Target)
			for _, location := range state.Locations {
				if location.RootPath == root {
					message = fmt.Sprintf("Preserve existing Location %q and its settings.", location.Name)
				}
			}
			plan.Settings = append(plan.Settings, message)
		}
		plan.Changed = true
	} else if plan.Source != "" || plan.Target != "" {
		plan.Settings = append(plan.Settings, "Preserve previously migrated Locations, including user edits and removals.")
	}
	// A group nobody wrote yet is the one the deployment configuration may seed.
	if !state.Preview.Present {
		plan.Preview, err = preview.SettingsFromConfig(conf.Preview)
		if err != nil {
			return nil, err
		}
		values, err := json.Marshal(plan.Preview)
		if err != nil {
			return nil, err
		}
		plan.Settings = append(plan.Settings, "Import Preview preferences (including resolved defaults): "+string(values))
		plan.Changed = true
	} else {
		plan.Settings = append(plan.Settings, "Preserve saved Preview preferences.")
	}
	return plan, nil
}

func checkConfigurationPlan(ctx context.Context, db *gorm.DB, filename string, plan *configurationPlan) error {
	current, err := planConfiguration(ctx, db, filename)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(current)
	if err != nil {
		return err
	}
	expected, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("configuration or Settings changed since review; run config-plan and review again")
	}
	return nil
}

func applyConfigurationPlan(ctx context.Context, db *gorm.DB, filename string, plan *configurationPlan) error {
	// Validate the review before schema writes; the installer holds service admission closed.
	if err := checkConfigurationPlan(ctx, db, filename, plan); err != nil {
		return err
	}
	if !plan.Changed {
		return nil
	}
	schema, err := installedSchema(db)
	if err != nil {
		return err
	}
	if schema != legacy.SchemaCurrent {
		return fmt.Errorf("complete catalog migration before applying configuration")
	}
	module := configurationSettings(db)
	if err := db.AutoMigrate(&library.Location{}); err != nil {
		return err
	}
	if err := module.AutoMigrate(); err != nil {
		return err
	}

	// Publish all imported preferences together, without file I/O inside the transaction.
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := configurationSnapshot(ctx, tx, plan.Source, plan.Target)
		if err != nil {
			return err
		}
		hash, err := configurationStateHash(state)
		if err != nil {
			return err
		}
		if hash != plan.StateHash {
			return fmt.Errorf("Settings changed since review")
		}
		transactionSettings := module.WithDB(tx)
		lib := library.NewWithSettings(tx, transactionSettings)
		if plan.ImportLocations {
			if err := lib.ImportLocationPaths(ctx, "local", plan.Source, plan.Target); err != nil {
				return err
			}
		}
		if plan.Preview != nil {
			_, err = transactionSettings.Preview.Save(ctx, plan.Preview)
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	// Preserve the existing inode's permissions, ACLs and xattrs; full backup covers interrupted writes.
	original, err := readConfigurationFile(filename)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(original)) != plan.OriginalHash {
		return fmt.Errorf("configuration changed before write; Settings were applied, keep service stopped and recover the backup")
	}
	if string(original) == plan.YAML {
		return nil
	}
	return os.WriteFile(filename, []byte(plan.YAML), 0600)
}
