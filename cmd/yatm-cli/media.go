package main

import (
	"context"
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type mediaListCommand struct {
	runtime *runtime
	Kinds   []string `long:"kind" choice:"tape" choice:"volume" description:"Media kind; repeatable"`
	Query   string   `long:"query" description:"Literal name, barcode or UUID substring"`
	Limit   *int64   `long:"limit" description:"Maximum results in this page"`
	Offset  *int64   `long:"offset" description:"Page offset"`
	AfterID *int64   `long:"after-id" description:"ID-ascending cursor; use 0 for the first page; exclusive with offset"`
}

type mediaGetCommand struct {
	runtime *runtime
	Args    fileIDsArgs `positional-args:"yes"`
}

type mediaPositionsCommand struct {
	runtime   *runtime
	Directory string     `long:"directory" description:"Immediate-child directory path"`
	Limit     *int64     `long:"limit" description:"Maximum results in this page"`
	AfterPath *string    `long:"after-path" description:"Stable physical path cursor"`
	Args      fileIDArgs `positional-args:"yes"`
}

type mediaInspectTapeCommand struct {
	runtime  *runtime
	Device   string  `long:"device" required:"yes" description:"Server-side Tape device"`
	Identity *string `long:"identity" value-name:"BARCODE" description:"Barcode hint when the device cannot read one"`
}

type mediaInspectVolumeCommand struct {
	runtime *runtime
	UUID    string `long:"uuid" required:"yes" description:"Volume UUID"`
}

type mediaDeleteCommand struct {
	runtime *runtime
	DryRun  bool        `long:"dryrun" description:"Report the resolved Media and its recorded Positions without deleting them"`
	Args    fileIDsArgs `positional-args:"yes"`
}

type volumeInitializeCommand struct {
	runtime      *runtime
	Name         string `long:"name" required:"yes" description:"Library display name"`
	Type         string `long:"type" required:"yes" choice:"hdd" choice:"hm-smr" description:"Volume access type"`
	SerialNumber string `long:"serial-number" description:"Optional physical serial number; read from the device when omitted"`
	Args         struct {
		MountPoint string `positional-arg-name:"MOUNT_POINT" required:"yes"`
	} `positional-args:"yes"`
}

type volumeRegisterCommand struct {
	runtime *runtime
	Name    string `long:"name" required:"yes" description:"Library display name"`
	Args    struct {
		MountPoint string `positional-arg-name:"MOUNT_POINT" required:"yes"`
	} `positional-args:"yes"`
}

type volumeCandidatesCommand struct {
	runtime *runtime
}

func (c *volumeCandidatesCommand) Execute(_ []string) error {
	// List configured discovery candidates without registering or initializing anything.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListVolumeCandidatesRequest, entity.ListVolumeCandidatesResponse](
		ctx, c.runtime, entity.MediaService_ListVolumeCandidates_FullMethodName, &entity.ListVolumeCandidatesRequest{})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

type tapeDeviceListCommand struct {
	runtime *runtime
}

func registerMediaCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "media", "Inspect and manage Library Media")
	if err != nil {
		return err
	}
	if err := addCommands(
		group,
		commandSpec{name: "list", description: "List one page of Media", handler: &mediaListCommand{runtime: commandRuntime}},
		commandSpec{name: "get", description: "Get Media by ID", handler: &mediaGetCommand{runtime: commandRuntime}},
		commandSpec{name: "positions", description: "List physical Positions", handler: &mediaPositionsCommand{runtime: commandRuntime}},
		commandSpec{name: "delete", description: "Delete Media and Position metadata", handler: &mediaDeleteCommand{runtime: commandRuntime}},
	); err != nil {
		return err
	}
	inspect, err := addGroup(group, "inspect", "Inspect a physical Media target")
	if err != nil {
		return err
	}
	return addCommands(
		inspect,
		commandSpec{name: "tape", description: "Inspect a Tape device", handler: &mediaInspectTapeCommand{runtime: commandRuntime}},
		commandSpec{name: "volume", description: "Inspect a mounted Volume", handler: &mediaInspectVolumeCommand{runtime: commandRuntime}},
	)
}

func registerVolumeCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "volume", "Initialize and register mounted Volumes")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "initialize", description: "Create a Volume marker and Library Media", handler: &volumeInitializeCommand{runtime: commandRuntime}},
		commandSpec{name: "register", description: "Register an existing Volume marker", handler: &volumeRegisterCommand{runtime: commandRuntime}},
		commandSpec{name: "candidates", description: "List mounted Volume discovery candidates", handler: &volumeCandidatesCommand{runtime: commandRuntime}},
	)
}

func registerTapeCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "tape", "Inspect Tape resources")
	if err != nil {
		return err
	}
	device, err := addGroup(group, "device", "Inspect configured Tape devices")
	if err != nil {
		return err
	}
	return addCommands(device, commandSpec{
		name: "list", description: "List available Tape devices", handler: &tapeDeviceListCommand{runtime: commandRuntime},
	})
}

func (c *mediaListCommand) Execute(_ []string) error {
	// Reject ambiguous pagination before making a request.
	if err := validateLimit("Media limit", c.Limit); err != nil {
		return err
	}
	if err := validateOffset("Media offset", c.Offset); err != nil {
		return err
	}
	if err := validateOffset("Media after ID", c.AfterID); err != nil {
		return err
	}
	if c.Offset != nil && c.AfterID != nil {
		return usageError(fmt.Errorf("Media after ID and offset are mutually exclusive"))
	}
	kinds, err := parseMediaKinds(c.Kinds)
	if err != nil {
		return err
	}

	// Keep search and cursor constraints on the server-side catalog page.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListMediaRequest, entity.ListMediaResponse](
		ctx,
		c.runtime,
		entity.MediaService_List_FullMethodName,
		(&entity.MediaFilter{Kinds: kinds, Query: c.Query, Limit: c.Limit, Offset: c.Offset, AfterId: c.AfterID}).Pack(),
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *mediaGetCommand) Execute(_ []string) error {
	ids, err := positiveIDs("Media ID", c.Args.IDs)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := getMedia(ctx, c.runtime, ids)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func getMedia(ctx context.Context, commandRuntime *runtime, ids []int64) (*entity.ListMediaResponse, error) {
	return callRPC[entity.ListMediaRequest, entity.ListMediaResponse](
		ctx,
		commandRuntime,
		entity.MediaService_List_FullMethodName,
		(&entity.MediaIds{Ids: ids}).Pack(),
	)
}

func (c *mediaPositionsCommand) Execute(_ []string) error {
	if err := positiveID("Media ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateLimit("Media Position limit", c.Limit); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListMediaPositionsRequest, entity.ListMediaPositionsResponse](
		ctx,
		c.runtime,
		entity.MediaService_ListPositions_FullMethodName,
		&entity.ListMediaPositionsRequest{
			Id: c.Args.ID, Directory: c.Directory, Limit: c.Limit, AfterPath: c.AfterPath,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *mediaInspectTapeCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := inspectTape(ctx, c.runtime, c.Device, c.Identity)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func inspectTape(
	ctx context.Context,
	commandRuntime *runtime,
	device string,
	identity *string,
) (*entity.InspectMediaResponse, error) {
	device = strings.TrimSpace(device)
	if device == "" {
		return nil, usageError(fmt.Errorf("Tape device is empty"))
	}
	if identity != nil {
		value := strings.TrimSpace(*identity)
		if value == "" {
			return nil, usageError(fmt.Errorf("Tape identity is empty"))
		}
		identity = &value
	}
	request := (&entity.InspectMediaTapeTarget{Device: device}).Pack()
	request.Identity = identity
	return callRPC[entity.InspectMediaRequest, entity.InspectMediaResponse](
		ctx,
		commandRuntime,
		entity.MediaService_Inspect_FullMethodName,
		request,
	)
}

func (c *mediaInspectVolumeCommand) Execute(_ []string) error {
	uuid := strings.TrimSpace(c.UUID)
	if uuid == "" {
		return usageError(fmt.Errorf("Volume UUID is empty"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.InspectMediaRequest, entity.InspectMediaResponse](
		ctx,
		c.runtime,
		entity.MediaService_Inspect_FullMethodName,
		(&entity.InspectMediaVolumeTarget{Uuid: uuid}).Pack(),
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *mediaDeleteCommand) Execute(_ []string) error {
	// Validate the target set before any remote reads.
	ids, err := positiveIDs("Media ID", c.Args.IDs)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Resolve the complete Media set immediately before deleting its metadata.
	resolved, err := getMedia(ctx, c.runtime, ids)
	if err != nil {
		return err
	}
	found := make(map[int64]struct{}, len(resolved.Media))
	for _, media := range resolved.Media {
		if media == nil {
			continue
		}
		found[media.Id] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return runtimeError("not_found", fmt.Errorf("Media does not exist, id=%d", id))
		}
	}

	// Delete the exact validated identifier set once.
	reply, err := callRPC[entity.DeleteMediaRequest, entity.DeleteMediaResponse](
		ctx,
		c.runtime,
		entity.MediaService_Delete_FullMethodName,
		&entity.DeleteMediaRequest{Ids: ids, Dryrun: c.DryRun},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *volumeInitializeCommand) Execute(_ []string) error {
	volumeType, err := parseVolumeType(c.Type)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Args.MountPoint) == "" {
		return usageError(fmt.Errorf("Volume mount point is empty"))
	}
	if strings.TrimSpace(c.Name) == "" {
		return usageError(fmt.Errorf("Volume name is empty"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.InitializeVolumeRequest, entity.InitializeVolumeResponse](
		ctx,
		c.runtime,
		entity.MediaService_InitializeVolume_FullMethodName,
		&entity.InitializeVolumeRequest{
			MountPoint: c.Args.MountPoint,
			Name:       c.Name,
			Profile: &entity.VolumeMediaProfile{
				SerialNumber: c.SerialNumber,
				Type:         volumeType,
			},
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *volumeRegisterCommand) Execute(_ []string) error {
	if strings.TrimSpace(c.Args.MountPoint) == "" {
		return usageError(fmt.Errorf("Volume mount point is empty"))
	}
	if strings.TrimSpace(c.Name) == "" {
		return usageError(fmt.Errorf("Volume name is empty"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.RegisterVolumeRequest, entity.RegisterVolumeResponse](
		ctx,
		c.runtime,
		entity.MediaService_RegisterVolume_FullMethodName,
		&entity.RegisterVolumeRequest{MountPoint: c.Args.MountPoint, Name: c.Name},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *tapeDeviceListCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListDevicesRequest, entity.ListDevicesResponse](
		ctx,
		c.runtime,
		entity.MediaService_ListDevices_FullMethodName,
		&entity.ListDevicesRequest{},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
