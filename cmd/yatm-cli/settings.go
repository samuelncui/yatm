package main

import (
	"context"
	"fmt"
	"io"
	"os"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type settingsLibraryCommand struct {
	runtime       *runtime
	Include       *string `long:"include-unbacked" choice:"true" choice:"false" description:"Set whether the default Library view includes files without versions"`
	ConfirmRemove *string `long:"confirm-remove" choice:"true" choice:"false" description:"Require browser confirmation before moving entries into Trash; CLI still requires explicit confirmation"`
}

type settingsPreviewCommand struct {
	runtime     *runtime
	PreviewJSON string `long:"preview-json" description:"Read PreviewSettings protobuf JSON from this file"`
}

type settingsJobCommand struct {
	runtime       *runtime
	ExecutionJSON string `long:"execution-json" description:"Read JobExecutionSettings protobuf JSON from this file"`
}

type settingsAccessCommand struct{ runtime *runtime }

type settingsBrowseCommand struct {
	runtime *runtime
	Path    string `long:"path" description:"Authorized absolute path, or a relative directory with location-id"`
	Cursor  string `long:"cursor" description:"Directory page cursor"`
	Limit   int32  `long:"limit" default:"100" description:"Maximum directories in this page"`
}

func registerSettingsCommands(root *flags.Command, rt *runtime) error {
	// Expose persisted preferences and administrator-constrained directory browsing.
	group, err := addGroup(root, "settings", "Library preferences and authorized directory browsing")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "library", description: "Read or update the default Library view", handler: &settingsLibraryCommand{runtime: rt}},
		commandSpec{name: "preview", description: "Read or update Preview generation preferences", handler: &settingsPreviewCommand{runtime: rt}},
		commandSpec{name: "job", description: "Read or update the Job pipeline preferences", handler: &settingsJobCommand{runtime: rt}},
		commandSpec{name: "access", description: "List administrator access ranges and migration results", handler: &settingsAccessCommand{runtime: rt}},
		commandSpec{name: "browse", description: "Browse authorized directories", handler: &settingsBrowseCommand{runtime: rt}},
	)
}

func (c *settingsLibraryCommand) Execute(_ []string) error {
	// Read the effective group before applying optional field updates.
	changing := c.Include != nil || c.ConfirmRemove != nil
	ctx, cancel := c.runtime.context()
	defer cancel()
	value, err := getSettings(ctx, c.runtime, entity.SettingsGroup_SETTINGS_GROUP_LIBRARY)
	if err != nil {
		return err
	}
	settings := value.GetLibrary()
	if settings == nil {
		return runtimeError("protocol", fmt.Errorf("Library settings reply is missing"))
	}
	if !changing {
		return writeProto(c.runtime.stdout, settings)
	}

	// Publish the explicit value without confusing false with an omitted option.
	if c.Include != nil {
		settings.IncludeUnbackedFiles = *c.Include == "true"
	}
	if c.ConfirmRemove != nil {
		settings.ConfirmRemove = *c.ConfirmRemove == "true"
	}
	reply, err := updateSettings(ctx, c.runtime, &entity.SettingsValue{Value: &entity.SettingsValue_Library{Library: settings}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply.GetLibrary())
}

func (c *settingsPreviewCommand) Execute(_ []string) error {
	// Read the effective group for display; replacement files carry the whole typed value.
	changing := c.PreviewJSON != ""
	ctx, cancel := c.runtime.context()
	defer cancel()
	value, err := getSettings(ctx, c.runtime, entity.SettingsGroup_SETTINGS_GROUP_PREVIEW)
	if err != nil {
		return err
	}
	settings := value.GetPreview()
	if settings == nil {
		return runtimeError("protocol", fmt.Errorf("Preview settings reply is missing"))
	}
	if !changing {
		return writeProto(c.runtime.stdout, settings)
	}

	// Replace the complete Preview group from the bounded input file.
	settings = &entity.PreviewSettings{}
	if err := readSettingsJSON(c.PreviewJSON, "Preview", settings); err != nil {
		return err
	}
	reply, err := updateSettings(ctx, c.runtime, &entity.SettingsValue{Value: &entity.SettingsValue_Preview{Preview: settings}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply.GetPreview())
}

func (c *settingsJobCommand) Execute(_ []string) error {
	changing := c.ExecutionJSON != ""
	ctx, cancel := c.runtime.context()
	defer cancel()
	value, err := getSettings(ctx, c.runtime, entity.SettingsGroup_SETTINGS_GROUP_JOB)
	if err != nil {
		return err
	}
	settings := value.GetJob()
	if settings == nil {
		return runtimeError("protocol", fmt.Errorf("Job settings reply is missing"))
	}
	if !changing {
		return writeProto(c.runtime.stdout, settings)
	}

	// Replace the execution limits while retaining the typed Job group.
	if settings.Execution == nil {
		settings.Execution = &entity.JobExecutionSettings{}
	}
	if err := readSettingsJSON(c.ExecutionJSON, "Job execution", settings.Execution); err != nil {
		return err
	}
	reply, err := updateSettings(ctx, c.runtime, &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: settings}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply.GetJob())
}

func getSettings(ctx context.Context, rt *runtime, group entity.SettingsGroup) (*entity.SettingsValue, error) {
	reply, err := callRPC[entity.GetSettingsRequest, entity.GetSettingsResponse](ctx, rt, entity.SettingsService_Get_FullMethodName,
		&entity.GetSettingsRequest{Group: group})
	if err != nil {
		return nil, err
	}
	return reply.GetValue(), nil
}

func updateSettings(ctx context.Context, rt *runtime, value *entity.SettingsValue) (*entity.SettingsValue, error) {
	reply, err := callRPC[entity.UpdateSettingsRequest, entity.UpdateSettingsResponse](ctx, rt, entity.SettingsService_Update_FullMethodName,
		&entity.UpdateSettingsRequest{Value: value})
	if err != nil {
		return nil, err
	}
	return reply.GetValue(), nil
}

// readSettingsJSON decodes one bounded settings file into a settings group.
func readSettingsJSON(path, label string, value proto.Message) error {
	file, err := os.Open(path)
	if err != nil {
		return runtimeError("io", err)
	}
	defer file.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return runtimeError("io", err)
	}
	if len(data) > limit {
		return usageError(fmt.Errorf("%s settings exceed 1 MiB", label))
	}
	if err := protojson.Unmarshal(data, value); err != nil {
		return usageError(fmt.Errorf("invalid %s settings: %w", label, err))
	}
	return nil
}

func (c *settingsAccessCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetLocationAccessRequest, entity.GetLocationAccessResponse](ctx, c.runtime, entity.LocationService_GetAccess_FullMethodName, &entity.GetLocationAccessRequest{})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *settingsBrowseCommand) Execute(_ []string) error {
	// Reject invalid page controls before directory access.
	if c.Limit < 1 || c.Limit > 500 {
		return usageError(fmt.Errorf("directory limit must be between 1 and 500"))
	}

	// Browse only server-authorized directories within the requested page.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.BrowsePathsRequest, entity.BrowsePathsResponse](ctx, c.runtime, entity.LocationService_BrowsePaths_FullMethodName, &entity.BrowsePathsRequest{Path: c.Path, Cursor: c.Cursor, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
