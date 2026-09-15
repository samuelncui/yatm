package main

import (
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type restoreCreateCommand struct {
	runtime *runtime
	selectionOptions
	restorePolicyOptions
	Priority           int64  `long:"priority" default:"1" description:"Job priority"`
	TargetLocation     int64  `long:"target-location" required:"yes" description:"Confirmed destination Location ID"`
	Directory          string `long:"directory" description:"Destination subdirectory relative to the Location root"`
	AllowDamagedCopies bool   `long:"allow-damaged-copies" description:"Permit damaged copies and retain completely read mismatched output without linking"`
	Args               struct {
		IDs []int64 `positional-arg-name:"VERSION_ID"`
	} `positional-args:"yes"`
}

type restoreMediaCommand struct {
	runtime  *runtime
	Statuses []string   `long:"status" description:"Copy status; repeatable"`
	Limit    *int32     `long:"limit" description:"Maximum results in this page"`
	Offset   *int64     `long:"offset" description:"Page offset"`
	Args     fileIDArgs `positional-args:"yes"`
}

type restoreFilesCommand struct {
	runtime  *runtime
	MediaID  int64      `long:"media-id" required:"yes" description:"Media ID"`
	Statuses []string   `long:"status" description:"Copy status; repeatable"`
	Limit    *int32     `long:"limit" description:"Maximum results in this page"`
	Offset   *int64     `long:"offset" description:"Page offset"`
	Args     fileIDArgs `positional-args:"yes"`
}

type restoreRunVolumeCommand struct {
	runtime *runtime
	UUID    string     `long:"uuid" required:"yes" description:"Mounted Volume UUID"`
	Args    fileIDArgs `positional-args:"yes"`
}

type restoreRunTapeCommand struct {
	runtime *runtime
	Device  string     `long:"device" required:"yes" description:"Server-side Tape device"`
	Args    fileIDArgs `positional-args:"yes"`
}

func registerRestoreCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "restore", "Create and run Restore Jobs")
	if err != nil {
		return err
	}
	if err := addCommands(
		group,
		commandSpec{name: "create", description: "Restore latest or dated file selections, with explicit version overrides", handler: &restoreCreateCommand{runtime: commandRuntime}},
		commandSpec{name: "media", description: "List one Restore Media page", handler: &restoreMediaCommand{runtime: commandRuntime}},
		commandSpec{name: "files", description: "List one Restore file page", handler: &restoreFilesCommand{runtime: commandRuntime}},
	); err != nil {
		return err
	}
	run, err := addGroup(group, "run", "Restore pending files from Media")
	if err != nil {
		return err
	}
	return addCommands(
		run,
		commandSpec{name: "volume", description: "Restore from a mounted Volume", handler: &restoreRunVolumeCommand{runtime: commandRuntime}},
		commandSpec{name: "tape", description: "Restore from a Tape device", handler: &restoreRunTapeCommand{runtime: commandRuntime}},
	)
}

func (c *restoreCreateCommand) Execute(_ []string) error {
	// Validate and normalize the complete Restore manifest before creating a Job.
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative, value=%d", c.Priority))
	}
	selections, err := c.selections()
	if err != nil {
		return err
	}
	for _, id := range c.Args.IDs {
		if err := positiveID("FileVersion ID", id); err != nil {
			return err
		}
	}
	if err := positiveID("target Location ID", c.TargetLocation); err != nil {
		return err
	}
	if len(c.Args.IDs)+len(selections) == 0 {
		return usageError(fmt.Errorf("at least one version or file selection is required"))
	}
	policy, err := c.policy(true)
	if err != nil {
		return err
	}

	// Create one Restore Job from explicitly selected archived versions.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateRestoreJobRequest, entity.CreateRestoreJobReply](
		ctx,
		c.runtime,
		entity.RestoreJobService_Create_FullMethodName,
		&entity.CreateRestoreJobRequest{
			Priority: c.Priority, Spec: &entity.RestoreJobSpec{
				FileVersionIds: c.Args.IDs, Selections: selections,
				AllowDamagedCopies: c.AllowDamagedCopies,
				VersionPolicy:      policy, SkipUnmatchedVersions: c.SkipUnmatchedVersions,
				Destination: &entity.RestoreDestination{LocationId: c.TargetLocation, Path: c.Directory},
			},
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *restoreMediaCommand) Execute(_ []string) error {
	// Validate and normalize one bounded Media page request.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateLimit32("Restore Media limit", c.Limit); err != nil {
		return err
	}
	if err := validateOffset("Restore Media offset", c.Offset); err != nil {
		return err
	}
	statuses, err := parseCopyStatuses(c.Statuses)
	if err != nil {
		return err
	}
	limit := int32(0)
	if c.Limit != nil {
		limit = *c.Limit
	}

	// Fetch exactly one Restore Media page.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListRestoreJobMediaRequest, entity.ListRestoreJobMediaReply](
		ctx,
		c.runtime,
		entity.RestoreJobService_ListMedia_FullMethodName,
		&entity.ListRestoreJobMediaRequest{
			Id: c.Args.ID, Limit: limit, Offset: c.Offset, FilterStatus: statuses,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *restoreFilesCommand) Execute(_ []string) error {
	// Validate and normalize one bounded File page request.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := positiveID("Media ID", c.MediaID); err != nil {
		return err
	}
	if err := validateLimit32("Restore file limit", c.Limit); err != nil {
		return err
	}
	if err := validateOffset("Restore file offset", c.Offset); err != nil {
		return err
	}
	statuses, err := parseCopyStatuses(c.Statuses)
	if err != nil {
		return err
	}
	limit := int32(0)
	if c.Limit != nil {
		limit = *c.Limit
	}

	// Fetch exactly one Restore File page for the selected Media.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListRestoreJobFilesRequest, entity.ListRestoreJobFilesReply](
		ctx,
		c.runtime,
		entity.RestoreJobService_ListFiles_FullMethodName,
		&entity.ListRestoreJobFilesRequest{
			Id: c.Args.ID, MediaId: c.MediaID, Limit: limit, Offset: c.Offset, FilterStatus: statuses,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *restoreRunVolumeCommand) Execute(_ []string) error {
	// Resolve the Job and mounted Volume identities before starting a Restore attempt.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	uuid := strings.TrimSpace(c.UUID)
	if uuid == "" {
		return usageError(fmt.Errorf("Volume UUID is empty"))
	}

	// Start one Restore attempt from the selected Volume.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.RestoreMediaRequest, entity.RestoreMediaReply](
		ctx,
		c.runtime,
		entity.RestoreJobService_RestoreMedia_FullMethodName,
		&entity.RestoreMediaRequest{
			Id: c.Args.ID, Target: (&entity.ReadVolumeTarget{Uuid: uuid}).Pack(),
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *restoreRunTapeCommand) Execute(_ []string) error {
	// Resolve the Job and server-side Tape device before starting a Restore attempt.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	device := strings.TrimSpace(c.Device)
	if device == "" {
		return usageError(fmt.Errorf("Tape device is empty"))
	}

	// Start one Restore attempt from the selected Tape device.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.RestoreMediaRequest, entity.RestoreMediaReply](
		ctx,
		c.runtime,
		entity.RestoreJobService_RestoreMedia_FullMethodName,
		&entity.RestoreMediaRequest{
			Id: c.Args.ID, Target: (&entity.ReadTapeTarget{Device: device}).Pack(),
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
