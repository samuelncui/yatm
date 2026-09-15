package main

import (
	"context"
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type archiveCreateCommand struct {
	runtime *runtime
	selectionOptions
	Priority      int64  `long:"priority" default:"1" description:"Job priority"`
	PreviewPolicy string `long:"preview-policy" choice:"none" choice:"missing-only" choice:"regenerate-all" default:"none" description:"Companion Scan Preview generation policy"`
	ForceRehash   bool   `long:"force-rehash" description:"Hash source content without using cached signatures"`
}

type archiveFilesCommand struct {
	runtime  *runtime
	Statuses []string   `long:"status" description:"Copy status; repeatable"`
	Limit    *int32     `long:"limit" description:"Maximum results in this page"`
	Offset   *int64     `long:"offset" description:"Page offset"`
	Args     fileIDArgs `positional-args:"yes"`
}

type archiveWriteVolumeCommand struct {
	runtime *runtime
	UUID    string     `long:"uuid" required:"yes" description:"Mounted Volume UUID"`
	Args    fileIDArgs `positional-args:"yes"`
}

type archiveWriteTapeAppendCommand struct {
	runtime *runtime
	Device  string     `long:"device" required:"yes" description:"Server-side Tape device"`
	Barcode string     `long:"barcode" required:"yes" description:"Expected Tape barcode"`
	Args    fileIDArgs `positional-args:"yes"`
}

type archiveWriteTapeFormatCommand struct {
	runtime       *runtime
	Device        string     `long:"device" required:"yes" description:"Server-side Tape device"`
	Barcode       string     `long:"barcode" required:"yes" description:"Expected Tape barcode"`
	Name          string     `long:"name" required:"yes" description:"Library display name"`
	ConfirmFormat string     `long:"confirm-format" required:"yes" value-name:"BARCODE" description:"Exact inspected barcode confirmation"`
	Args          fileIDArgs `positional-args:"yes"`
}

func registerArchiveCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "archive", "Create and run Archive Jobs")
	if err != nil {
		return err
	}
	if err := addCommands(
		group,
		commandSpec{name: "create", description: "Create an Archive Job", handler: &archiveCreateCommand{runtime: commandRuntime}},
		commandSpec{name: "files", description: "List one Archive manifest page", handler: &archiveFilesCommand{runtime: commandRuntime}},
	); err != nil {
		return err
	}
	write, err := addGroup(group, "write", "Write pending Archive files to Media")
	if err != nil {
		return err
	}
	if err := addCommands(write, commandSpec{
		name: "volume", description: "Write to a mounted Volume", handler: &archiveWriteVolumeCommand{runtime: commandRuntime},
	}); err != nil {
		return err
	}
	tape, err := addGroup(write, "tape", "Write to a Tape device")
	if err != nil {
		return err
	}
	return addCommands(
		tape,
		commandSpec{name: "append", description: "Append to a compatible Tape", handler: &archiveWriteTapeAppendCommand{runtime: commandRuntime}},
		commandSpec{name: "format", description: "Format and write a new Tape", handler: &archiveWriteTapeFormatCommand{runtime: commandRuntime}},
	)
}

func (c *archiveCreateCommand) Execute(_ []string) error {
	// Validate Preview policy combinations before resolving any server paths.
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative, value=%d", c.Priority))
	}
	preview, err := parsePreviewPolicy(c.PreviewPolicy)
	if err != nil {
		return err
	}
	if preview == entity.PreviewPolicy_PREVIEW_NONE && c.ForceRehash {
		return usageError(fmt.Errorf("force-rehash requires a Preview generation policy"))
	}

	// Send explicit selection roots; the server freezes their scope and content.
	ctx, cancel := c.runtime.context()
	defer cancel()
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if len(selections) == 0 {
		return usageError(fmt.Errorf("at least one file-id or location selection is required"))
	}

	// Create one Archive Job from the resolved Source identities.
	reply, err := callRPC[entity.CreateArchiveJobRequest, entity.CreateArchiveJobReply](
		ctx,
		c.runtime,
		entity.ArchiveJobService_Create_FullMethodName,
		&entity.CreateArchiveJobRequest{
			Priority:      c.Priority,
			Spec:          &entity.ArchiveJobSpec{Selections: selections},
			PreviewPolicy: preview,
			ForceRehash:   c.ForceRehash,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *archiveFilesCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateLimit32("Archive file limit", c.Limit); err != nil {
		return err
	}
	if err := validateOffset("Archive file offset", c.Offset); err != nil {
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
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListArchiveJobFilesRequest, entity.ListArchiveJobFilesReply](
		ctx,
		c.runtime,
		entity.ArchiveJobService_ListFiles_FullMethodName,
		&entity.ListArchiveJobFilesRequest{
			Id: c.Args.ID, Limit: limit, Offset: c.Offset, FilterStatus: statuses,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *archiveWriteVolumeCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	uuid := strings.TrimSpace(c.UUID)
	if uuid == "" {
		return usageError(fmt.Errorf("Volume UUID is empty"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.WriteArchiveMediaRequest, entity.WriteArchiveMediaReply](
		ctx,
		c.runtime,
		entity.ArchiveJobService_WriteMedia_FullMethodName,
		&entity.WriteArchiveMediaRequest{
			Id: c.Args.ID, Target: (&entity.ArchiveVolumeTarget{Uuid: uuid}).Pack(),
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *archiveWriteTapeAppendCommand) Execute(_ []string) error {
	// Validate the Job and inspect the actual Tape identity and Library profile.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	device := strings.TrimSpace(c.Device)
	inspected, err := inspectExpectedTape(ctx, c.runtime, device, c.Barcode)
	if err != nil {
		return err
	}
	if inspected.Media == nil {
		return runtimeError("not_found", fmt.Errorf("Tape is not registered in the Library, barcode=%q", inspected.Identity))
	}
	profile := inspected.Media.GetProfile().GetTape()
	if profile == nil || profile.Format != "ltfs_v1" {
		return safetyError(fmt.Errorf("Tape does not support append, barcode=%q", inspected.Identity))
	}

	// Start one append attempt against the inspected compatible Tape.
	return writeArchiveTape(
		ctx,
		c.runtime,
		c.Args.ID,
		&entity.ArchiveTapeTarget{
			Device: device, Barcode: inspected.Identity, Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
		},
	)
}

func (c *archiveWriteTapeFormatCommand) Execute(_ []string) error {
	// Validate the Job and inspect the actual Tape before evaluating confirmation.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if strings.TrimSpace(c.Name) == "" {
		return usageError(fmt.Errorf("Tape name is empty"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	device := strings.TrimSpace(c.Device)
	inspected, err := inspectExpectedTape(ctx, c.runtime, device, c.Barcode)
	if err != nil {
		return err
	}
	if c.ConfirmFormat != inspected.Identity {
		return safetyError(fmt.Errorf(
			"confirm-format must exactly match the inspected barcode, inspected=%q",
			inspected.Identity,
		))
	}
	if inspected.Media != nil {
		return safetyError(fmt.Errorf(
			"Tape already exists in the Library; delete its Media metadata explicitly before formatting, barcode=%q",
			inspected.Identity,
		))
	}

	// Start one format attempt using the exact inspected identity.
	return writeArchiveTape(
		ctx,
		c.runtime,
		c.Args.ID,
		&entity.ArchiveTapeTarget{
			Device:  device,
			Barcode: inspected.Identity,
			Name:    c.Name,
			Mode:    entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		},
	)
}

func inspectExpectedTape(
	ctx context.Context,
	commandRuntime *runtime,
	device, barcode string,
) (*entity.MediaInspectReply, error) {
	device = strings.TrimSpace(device)
	barcode = strings.TrimSpace(barcode)
	if device == "" {
		return nil, usageError(fmt.Errorf("Tape device is empty"))
	}
	if barcode == "" {
		return nil, usageError(fmt.Errorf("Tape barcode is empty"))
	}
	inspected, err := inspectTape(ctx, commandRuntime, device, &barcode)
	if err != nil {
		return nil, err
	}
	if inspected.Identity != barcode {
		return nil, safetyError(fmt.Errorf(
			"Tape barcode does not match the inspected identity, requested=%q inspected=%q",
			barcode,
			inspected.Identity,
		))
	}
	return inspected, nil
}

func writeArchiveTape(
	ctx context.Context,
	commandRuntime *runtime,
	id int64,
	target *entity.ArchiveTapeTarget,
) error {
	reply, err := callRPC[entity.WriteArchiveMediaRequest, entity.WriteArchiveMediaReply](
		ctx,
		commandRuntime,
		entity.ArchiveJobService_WriteMedia_FullMethodName,
		&entity.WriteArchiveMediaRequest{Id: id, Target: target.Pack()},
	)
	if err != nil {
		return err
	}
	return writeProto(commandRuntime.stdout, reply)
}
