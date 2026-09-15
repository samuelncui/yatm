package main

import (
	"fmt"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type settingsLibraryCommand struct {
	runtime       *runtime
	Include       *string `long:"include-unbacked" choice:"true" choice:"false" description:"Set whether the default Library view includes files without versions"`
	AutoCollect   *string `long:"auto-collect" choice:"true" choice:"false" description:"Automatically collect Location files without hashing"`
	ConfirmDelete *string `long:"confirm-delete" choice:"true" choice:"false" description:"Require browser confirmation before permanent deletion; CLI still requires explicit confirmation"`
	Revision      *int64  `long:"revision" description:"Expected settings revision; required when changing the preference"`
}

type settingsAccessCommand struct{ runtime *runtime }

type settingsBrowseCommand struct {
	runtime    *runtime
	Path       string `long:"path" description:"Authorized absolute path, or a relative directory with location-id"`
	LocationID int64  `long:"location-id" description:"Constrain browsing to this locally confirmed Location"`
	Cursor     string `long:"cursor" description:"Directory page cursor"`
	Limit      int32  `long:"limit" default:"100" description:"Maximum directories in this page"`
}

func registerSettingsCommands(root *flags.Command, rt *runtime) error {
	// Expose persisted preferences and administrator-constrained directory browsing.
	group, err := addGroup(root, "settings", "Library preferences and authorized directory browsing")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "library", description: "Read or update the default Library view", handler: &settingsLibraryCommand{runtime: rt}},
		commandSpec{name: "access", description: "List administrator access ranges and migration results", handler: &settingsAccessCommand{runtime: rt}},
		commandSpec{name: "browse", description: "Browse authorized directories", handler: &settingsBrowseCommand{runtime: rt}},
	)
}

func (c *settingsLibraryCommand) Execute(_ []string) error {
	// Require the inspected revision for mutations; ordinary reads have no side effects.
	changing := c.Include != nil || c.AutoCollect != nil || c.ConfirmDelete != nil
	if changing != (c.Revision != nil) {
		return usageError(fmt.Errorf("a preference and revision must be supplied together"))
	}
	if c.Revision != nil && *c.Revision < 0 {
		return usageError(fmt.Errorf("settings revision must not be negative"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	settings, err := callRPC[entity.GetLibrarySettingsRequest, entity.LibrarySettings](ctx, c.runtime, entity.SettingsService_GetLibrary_FullMethodName, &entity.GetLibrarySettingsRequest{})
	if err != nil {
		return err
	}
	if !changing {
		return writeProto(c.runtime.stdout, settings)
	}
	// Publish the explicit value without confusing false with an omitted option.
	if c.Include != nil {
		settings.IncludeUnbackedFiles = *c.Include == "true"
	}
	if c.AutoCollect != nil {
		settings.AutoCollectFiles = *c.AutoCollect == "true"
	}
	if c.ConfirmDelete != nil {
		settings.ConfirmPermanentDelete = *c.ConfirmDelete == "true"
	}
	settings.Revision = *c.Revision
	reply, err := callRPC[entity.UpdateLibrarySettingsRequest, entity.UpdateLibrarySettingsReply](ctx, c.runtime, entity.SettingsService_UpdateLibrary_FullMethodName, &entity.UpdateLibrarySettingsRequest{Settings: settings})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *settingsAccessCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetAccessRequest, entity.GetAccessReply](ctx, c.runtime, entity.SettingsService_GetAccess_FullMethodName, &entity.GetAccessRequest{})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *settingsBrowseCommand) Execute(_ []string) error {
	// Reject invalid page controls before directory access.
	if c.LocationID < 0 {
		return usageError(fmt.Errorf("Location ID must not be negative"))
	}
	if c.Limit < 1 || c.Limit > 500 {
		return usageError(fmt.Errorf("directory limit must be between 1 and 500"))
	}

	// Browse only server-authorized directories within the requested page.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.BrowsePathsRequest, entity.BrowsePathsReply](ctx, c.runtime, entity.SettingsService_BrowsePaths_FullMethodName, &entity.BrowsePathsRequest{Path: c.Path, LocationId: c.LocationID, Cursor: c.Cursor, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
