package main

import (
	"context"
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
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
	Statuses []string `long:"status" description:"Copy status; repeatable"`
	jobResultPageOptions
	Args fileIDArgs `positional-args:"yes"`
}

type archiveWriteVolumeCommand struct {
	runtime *runtime
	UUID    string     `long:"uuid" required:"yes" description:"Mounted Volume UUID"`
	Args    fileIDArgs `positional-args:"yes"`
}

type archiveWriteTapeAppendCommand struct {
	runtime *runtime
	tapeWriteOptions
	Device  string     `long:"device" required:"yes" description:"Server-side Tape device"`
	Barcode string     `long:"barcode" required:"yes" description:"Expected Tape barcode"`
	Args    fileIDArgs `positional-args:"yes"`
}

type archiveWriteTapeFormatCommand struct {
	runtime *runtime
	tapeWriteOptions
	Device        string     `long:"device" required:"yes" description:"Server-side Tape device"`
	Barcode       string     `long:"barcode" required:"yes" description:"Tape barcode; supplied when the cartridge memory barcode is empty"`
	Name          string     `long:"name" required:"yes" description:"Library display name"`
	ConfirmFormat string     `long:"confirm-format" required:"yes" value-name:"BARCODE" description:"Exact Tape barcode confirmation"`
	Args          fileIDArgs `positional-args:"yes"`
}

type tapeWriteOptions struct {
	Format              *string `long:"format" choice:"ltfs_v1" choice:"tar_pax_v1" description:"FORMAT override; APPEND must match the registered profile"`
	RecordMode          *string `long:"record-mode" choice:"fixed" choice:"variable" description:"Tar record policy; immutable on APPEND"`
	RecordBytes         *uint32 `long:"record-bytes" description:"Tar record size in bytes, aligned to 512; FORMAT omission uses the device default or 524288 (512 KiB)"`
	HardwareCompression *string `long:"hardware-compression" choice:"true" choice:"false" description:"Tar write compression; the selected drive is rechecked before writing"`
}

func (c tapeWriteOptions) options() (*entity.TarWriteOptions, error) {
	// An omitted option must remain absent, including optional false values.
	if c.RecordMode == nil && c.RecordBytes == nil && c.HardwareCompression == nil {
		return nil, nil
	}
	options := &entity.TarWriteOptions{RecordBytes: c.RecordBytes}
	if c.RecordMode != nil {
		mode := entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED
		if *c.RecordMode == "variable" {
			mode = entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE
		}
		options.RecordMode = &mode
	}
	if c.HardwareCompression != nil {
		value := *c.HardwareCompression == "true"
		options.HardwareCompression = &value
	}

	// Reject malformed relationships before device access without changing explicit choices.
	if err := settingspkg.ValidateTarWriteOptions(options); err != nil {
		return nil, usageError(err)
	}
	return options, nil
}

func resolveTapeFormat(ctx context.Context, rt *runtime, device string, explicit *string) (string, error) {
	// Explicit overrides bypass saved-preference reads.
	if explicit != nil {
		return *explicit, nil
	}

	// Resolve the configured entry's preference through the existing typed Settings RPC.
	value, err := getSettings(ctx, rt, entity.SettingsGroup_SETTINGS_GROUP_DEVICE)
	if err != nil {
		return "", err
	}
	if value.GetDevice() == nil {
		return "", runtimeError("protocol", fmt.Errorf("Device settings reply is missing"))
	}
	return settingspkg.TapeDefaultFormat(settingspkg.TapeDefaults(value.GetDevice(), device)), nil
}

func registerArchiveCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "archive", "Create and run Archive Jobs")
	if err != nil {
		return err
	}
	if err := addCommands(
		group,
		commandSpec{name: "estimate", description: "Estimate Archive selections", handler: &inspectSelectionCommand{runtime: commandRuntime}},
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
	if preview == entity.PreviewPolicy_PREVIEW_POLICY_NONE && c.ForceRehash {
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

	// Create one Archive Job from the selected roots.
	reply, err := callRPC[entity.CreateArchiveJobRequest, entity.CreateArchiveJobResponse](
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
	if err := c.jobResultPageOptions.validate(); err != nil {
		return err
	}
	statuses, err := parseCopyStatuses(c.Statuses)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListArchiveJobFilesRequest, entity.ListArchiveJobFilesResponse](
		ctx,
		c.runtime,
		entity.ArchiveJobService_ListFiles_FullMethodName,
		&entity.ListArchiveJobFilesRequest{
			Id: c.Args.ID, Limit: c.pageLimit(), Cursor: c.Cursor, Order: c.order(), Offset: c.Offset,
			IncludeTotal: c.IncludeTotal, FilterStatus: statuses,
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
	reply, err := callRPC[entity.WriteArchiveMediaRequest, entity.WriteArchiveMediaResponse](
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
	// Resolve the registered format before choosing a physical adapter.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	options, err := c.options()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	device := strings.TrimSpace(c.Device)
	if device == "" {
		return usageError(fmt.Errorf("Tape device is empty"))
	}
	barcode := strings.ToUpper(strings.TrimSpace(c.Barcode))
	registered, err := findTapeMedia(ctx, c.runtime, barcode)
	if err != nil {
		return err
	}
	if registered == nil {
		return runtimeError("not_found", fmt.Errorf("Tape is not registered in the Library, barcode=%q", barcode))
	}
	format := registered.GetProfile().GetTape().GetFormat()
	if c.Format != nil && *c.Format != format {
		return safetyError(fmt.Errorf("format conflicts with registered Tape profile, format=%q", format))
	}
	if format == "ltfs_v1" && options != nil {
		return usageError(fmt.Errorf("tar options are not accepted for LTFS"))
	}

	// LTFS retains its identity adapter; raw APPEND verifies the immutable label under the lease.
	switch format {
	case "ltfs_v1":
		inspected, err := inspectExpectedTape(ctx, c.runtime, device, barcode)
		if err != nil {
			return err
		}
		if inspected.Media == nil || inspected.Media.Id != registered.Id {
			return safetyError(fmt.Errorf("inspected Tape does not match its registered Media"))
		}
	case "tar_pax_v1":
		if _, err := inspectRawTapeDevice(ctx, c.runtime, device); err != nil {
			return err
		}
	default:
		return safetyError(fmt.Errorf("Tape does not support append, format=%q", format))
	}

	// Only explicitly supplied APPEND options are checked against the immutable stored profile.
	return writeArchiveTape(ctx, c.runtime, c.Args.ID, &entity.ArchiveTapeTarget{
		Device: device, Barcode: barcode, Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
		Format: &format, TarOptions: options,
	})
}

func (c *archiveWriteTapeFormatCommand) Execute(_ []string) error {
	// Validate the explicit FORMAT inputs before inspecting the Tape.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if strings.TrimSpace(c.Name) == "" {
		return usageError(fmt.Errorf("Tape name is empty"))
	}
	barcode := strings.ToUpper(strings.TrimSpace(c.Barcode))
	if len(barcode) != 6 {
		return usageError(fmt.Errorf("Tape barcode must contain 6 characters"))
	}
	for _, character := range barcode {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return usageError(fmt.Errorf("Tape barcode contains an invalid character, barcode=%q", barcode))
		}
	}

	// Confirm destructive initialization before format resolution or physical inspection.
	if c.ConfirmFormat != barcode {
		return safetyError(fmt.Errorf("confirm-format must exactly match the Tape barcode, barcode=%q", barcode))
	}
	options, err := c.options()
	if err != nil {
		return err
	}

	// Resolve the selected device's FORMAT preference before selecting its physical adapter.
	ctx, cancel := c.runtime.context()
	defer cancel()
	device := strings.TrimSpace(c.Device)
	if device == "" {
		return usageError(fmt.Errorf("Tape device is empty"))
	}
	format, err := resolveTapeFormat(ctx, c.runtime, device, c.Format)
	if err != nil {
		return err
	}
	switch format {
	case "ltfs_v1":
		if options != nil {
			return usageError(fmt.Errorf("tar options are not accepted for LTFS"))
		}
		inspected, err := inspectTape(ctx, c.runtime, device, &barcode)
		if err != nil {
			return err
		}
		if inspected.Identity != "" && inspected.Identity != barcode {
			return safetyError(fmt.Errorf("Tape barcode does not match the inspected identity, requested=%q inspected=%q", barcode, inspected.Identity))
		}
		if inspected.Media != nil {
			return safetyError(fmt.Errorf("Tape already exists in the Library; delete its Media metadata explicitly before formatting, barcode=%q", barcode))
		}
	case "tar_pax_v1":
		registered, err := findTapeMedia(ctx, c.runtime, barcode)
		if err != nil {
			return err
		}
		if registered != nil {
			return safetyError(fmt.Errorf("Tape already exists in the Library; delete its Media metadata explicitly before formatting, barcode=%q", barcode))
		}
		if _, err := inspectRawTapeDevice(ctx, c.runtime, device); err != nil {
			return err
		}
	default:
		return usageError(fmt.Errorf("unsupported device default format %q; choose --format ltfs_v1 or tar_pax_v1", format))
	}

	// Start one FORMAT using the confirmed barcode, including when MAM reports none.
	return writeArchiveTape(
		ctx,
		c.runtime,
		c.Args.ID,
		&entity.ArchiveTapeTarget{
			Device:  device,
			Barcode: barcode,
			Name:    c.Name,
			Mode:    entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
			Format:  &format, TarOptions: options,
		},
	)
}

func inspectExpectedTape(
	ctx context.Context,
	commandRuntime *runtime,
	device, barcode string,
) (*entity.InspectMediaResponse, error) {
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
	reply, err := callRPC[entity.WriteArchiveMediaRequest, entity.WriteArchiveMediaResponse](
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
