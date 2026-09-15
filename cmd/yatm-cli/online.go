package main

import (
	"fmt"
	"os"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

type onlineConfigOptions struct {
	RestoreTarget     bool   `long:"restore-target" description:"Recommend this Location in the Restore destination chooser"`
	Name              string `long:"name" required:"yes" description:"Location display name"`
	Root              string `long:"root" required:"yes" description:"Executor root directory"`
	IgnoreFile        string `long:"ignore-file" description:"Read verbatim gitignore rules from this local text file"`
	WriteTrackingUUID bool   `long:"write-tracking-uuid" description:"Allow create-only tracking UUID xattrs"`
}
type onlineCreateCommand struct {
	runtime *runtime
	onlineConfigOptions
}
type onlineUpdateCommand struct {
	runtime *runtime
	onlineConfigOptions
	Revision int64      `long:"revision" required:"yes" description:"Expected Location revision from location get"`
	Args     fileIDArgs `positional-args:"yes"`
}
type onlineGetCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}
type onlineListCommand struct {
	runtime       *runtime
	Query         string  `long:"query" description:"Literal Location name or root path substring"`
	AfterID       int64   `long:"after-id" description:"Location ID cursor"`
	Limit         int32   `long:"limit" default:"100" description:"Maximum results in this page"`
	RestoreTarget *string `long:"restore-target" choice:"true" choice:"false" description:"Filter by Restore recommendation; omit for all Locations"`
}
type onlineConfirmCommand struct {
	runtime  *runtime
	Revision int64      `long:"revision" required:"yes" description:"Expected Location revision"`
	Args     fileIDArgs `positional-args:"yes"`
}
type onlineDeleteCommand struct {
	runtime  *runtime
	Revision int64      `long:"revision" required:"yes" description:"Expected Location revision"`
	Confirm  bool       `long:"confirm" description:"Confirm metadata-only unregistration"`
	Args     fileIDArgs `positional-args:"yes"`
}
type onlinePositionsCommand struct {
	runtime    *runtime
	Parent     string     `long:"parent" description:"Physical parent path, including trailing /; empty for root"`
	Cursor     string     `long:"cursor" description:"Live directory observation cursor"`
	NameFilter string     `long:"name" description:"Case-insensitive name filter in this directory"`
	Limit      int32      `long:"limit" default:"100" description:"Maximum results in this page"`
	Args       fileIDArgs `positional-args:"yes"`
}
type analyzeCreateCommand struct {
	runtime       *runtime
	Priority      int64      `long:"priority" default:"0" description:"Job priority"`
	Mode          string     `long:"mode" choice:"incremental" choice:"basic" choice:"force" default:"incremental" description:"Content analysis policy"`
	Paths         []string   `long:"path" description:"Selected relative file or directory; repeat for multiple roots"`
	PreviewPolicy string     `long:"preview-policy" choice:"none" choice:"missing-only" choice:"regenerate-all" default:"none" description:"Preview generation policy"`
	Args          fileIDArgs `positional-args:"yes"`
}
type analyzeProgressCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}
type analyzeEntriesCommand struct {
	runtime *runtime
	AfterID *int64     `long:"after-id" description:"Result entry cursor"`
	Limit   int32      `long:"limit" default:"100" description:"Maximum results in this page"`
	Args    fileIDArgs `positional-args:"yes"`
}

func registerOnlineCommands(root *flags.Command, rt *runtime) error {
	// Every registered path supports indexing; Restore preference does not grant access.
	group, err := addGroup(root, "location", "Manage registered directories for originals and restore output")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "create", description: "Register a Location", handler: &onlineCreateCommand{runtime: rt}},
		commandSpec{name: "list", description: "List registered Locations", handler: &onlineListCommand{runtime: rt}},
		commandSpec{name: "get", description: "Inspect Location binding and accessibility", handler: &onlineGetCommand{runtime: rt}},
		commandSpec{name: "update", description: "Replace Location configuration", handler: &onlineUpdateCommand{runtime: rt}},
		commandSpec{name: "confirm", description: "Confirm an imported local binding", handler: &onlineConfirmCommand{runtime: rt}},
		commandSpec{name: "delete", description: "Unregister a Location without deleting physical files", handler: &onlineDeleteCommand{runtime: rt}},
		commandSpec{name: "entries", description: "Browse one live physical page", handler: &onlinePositionsCommand{runtime: rt}},
		commandSpec{name: "entry", description: "Inspect a live path, including unadmitted files", handler: &liveEntryCommand{runtime: rt}},
		commandSpec{name: "admit", description: "Add a live file to Library", handler: &liveAdmitCommand{runtime: rt}},
	)
}

func registerAnalyzeCommands(root *flags.Command, rt *runtime) error {
	group, err := addGroup(root, "analyze", "Collect metadata or analyze content in a Location")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "create", description: "Analyze selected Location files or directories", handler: &analyzeCreateCommand{runtime: rt}},
		commandSpec{name: "progress", description: "Get analysis progress and scope results", handler: &analyzeProgressCommand{runtime: rt}},
		commandSpec{name: "entries", description: "List an analysis result page", handler: &analyzeEntriesCommand{runtime: rt}},
	)
}

func (c *onlineCreateCommand) Execute(_ []string) error {
	location, err := c.location()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateLocationRequest, entity.LocationReply](ctx, c.runtime, entity.LocationService_Create_FullMethodName, &entity.CreateLocationRequest{Location: location})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineUpdateCommand) Execute(_ []string) error {
	// Explicit revisions prevent silently overwriting configuration viewed before a synchronization.
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	if c.Revision <= 0 {
		return usageError(fmt.Errorf("Location revision must be positive"))
	}

	// Replace the complete named binding and Ignore configuration in one RPC.
	location, err := c.location()
	if err != nil {
		return err
	}
	location.Id, location.Revision = c.Args.ID, c.Revision
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.UpdateLocationRequest, entity.LocationReply](ctx, c.runtime, entity.LocationService_Update_FullMethodName, &entity.UpdateLocationRequest{Location: location})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineGetCommand) Execute(_ []string) error {
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.LocationRef, entity.LocationReply](ctx, c.runtime, entity.LocationService_Get_FullMethodName, &entity.LocationRef{Id: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineListCommand) Execute(_ []string) error {
	// Preserve the optional preference filter independently of literal catalog search.
	request := &entity.ListLocationsRequest{AfterId: c.AfterID, Limit: c.Limit, Query: c.Query}
	if c.RestoreTarget != nil {
		request.RestoreTarget = proto.Bool(*c.RestoreTarget == "true")
	}

	// Request one filtered catalog page without filesystem traversal.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListLocationsRequest, entity.ListLocationsReply](ctx, c.runtime, entity.LocationService_List_FullMethodName, request)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineConfirmCommand) Execute(_ []string) error {
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.LocationRef, entity.LocationReply](ctx, c.runtime, entity.LocationService_Confirm_FullMethodName, &entity.LocationRef{Id: c.Args.ID, Revision: c.Revision})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineDeleteCommand) Execute(_ []string) error {
	// Unregistration is metadata-only but still requires explicit removal confirmation.
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	if !c.Confirm {
		return safetyError(fmt.Errorf("location delete requires --confirm; original files are retained"))
	}

	// Delete only the selected source at the revision the caller inspected.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.LocationRef, entity.DeleteLocationReply](ctx, c.runtime, entity.LocationService_Delete_FullMethodName, &entity.LocationRef{Id: c.Args.ID, Revision: c.Revision})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlinePositionsCommand) Execute(_ []string) error {
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListLocationEntriesRequest, entity.ListLocationEntriesReply](ctx, c.runtime, entity.LocationService_ListEntries_FullMethodName, &entity.ListLocationEntriesRequest{LocationId: c.Args.ID, ParentPath: c.Parent, Cursor: c.Cursor, NameFilter: c.NameFilter, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *analyzeCreateCommand) Execute(_ []string) error {
	// Validate optional policies locally before creating an asynchronous Job.
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative"))
	}
	preview, err := parsePreviewPolicy(c.PreviewPolicy)
	if err != nil {
		return err
	}

	// Freeze the selected roots and one explicit analysis policy in the managed Job.
	mode := entity.ScanSignaturePolicy_FILL_MISSING
	switch c.Mode {
	case "basic":
		mode = entity.ScanSignaturePolicy_KNOWN_ONLY
	case "force":
		mode = entity.ScanSignaturePolicy_FORCE_READ
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateScanJobRequest, entity.CreateScanJobReply](ctx, c.runtime, entity.ScanJobService_Create_FullMethodName, &entity.CreateScanJobRequest{Priority: c.Priority, Spec: &entity.ScanJobSpec{LocationId: c.Args.ID, SignaturePolicy: mode, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, Paths: c.Paths, PreviewPolicy: preview}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *analyzeProgressCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetScanJobProgressRequest, entity.GetScanJobProgressReply](ctx, c.runtime, entity.ScanJobService_GetProgress_FullMethodName, &entity.GetScanJobProgressRequest{Id: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *analyzeEntriesCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateLimit32("Scan result limit", &c.Limit); err != nil {
		return err
	}
	if c.AfterID != nil && *c.AfterID < 0 {
		return usageError(fmt.Errorf("after-id must not be negative"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListScanJobEntriesRequest, entity.ListScanJobEntriesReply](ctx, c.runtime, entity.ScanJobService_ListEntries_FullMethodName, &entity.ListScanJobEntriesRequest{Id: c.Args.ID, AfterId: c.AfterID, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *onlineConfigOptions) location() (*entity.Location, error) {
	var text []byte
	if c.IgnoreFile != "" {
		var err error
		text, err = os.ReadFile(c.IgnoreFile)
		if err != nil {
			return nil, fmt.Errorf("read Ignore rules failed, %w", err)
		}
	}
	return &entity.Location{
		Name: c.Name, RootPath: c.Root, Ignore: &entity.IgnoreRules{Format: "gitignore", Text: string(text)},
		WriteTrackingUuid: c.WriteTrackingUUID, RestoreTarget: c.RestoreTarget,
	}, nil
}
