package main

import (
	"fmt"
	"os"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

type locationConfigOptions struct {
	RestoreTarget bool   `long:"restore-target" description:"Recommend this Location in the Restore destination chooser"`
	Name          string `long:"name" required:"yes" description:"Location display name"`
	Root          string `long:"root" required:"yes" description:"Executor root directory"`
	IgnoreFile    string `long:"ignore-file" description:"Read verbatim gitignore rules from this local text file"`
	UseMmap       bool   `long:"use-mmap" description:"Read original content through memory mapping"`
}
type locationCreateCommand struct {
	runtime *runtime
	locationConfigOptions
}
type locationUpdateCommand struct {
	runtime *runtime
	locationConfigOptions
	Args fileIDArgs `positional-args:"yes"`
}
type locationGetCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}
type locationListCommand struct {
	runtime       *runtime
	Query         string  `long:"query" description:"Literal Location name or root path substring"`
	AfterID       int64   `long:"after-id" description:"Location ID cursor"`
	Limit         int32   `long:"limit" default:"100" description:"Maximum results in this page"`
	RestoreTarget *string `long:"restore-target" choice:"true" choice:"false" description:"Filter by Restore recommendation; omit for all Locations"`
}
type locationDeleteCommand struct {
	runtime *runtime
	DryRun  bool       `long:"dryrun" description:"Report the registration and its original associations without removing them"`
	Args    fileIDArgs `positional-args:"yes"`
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
	jobResultPageOptions
	Args fileIDArgs `positional-args:"yes"`
}

func registerLocationCommands(root *flags.Command, rt *runtime) error {
	// Every registered path supports indexing; Restore preference does not grant access.
	group, err := addGroup(root, "location", "Manage registered directories for originals and restore output")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "create", description: "Register a Location", handler: &locationCreateCommand{runtime: rt}},
		commandSpec{name: "list", description: "List registered Locations", handler: &locationListCommand{runtime: rt}},
		commandSpec{name: "get", description: "Inspect Location configuration and accessibility", handler: &locationGetCommand{runtime: rt}},
		commandSpec{name: "update", description: "Replace Location configuration", handler: &locationUpdateCommand{runtime: rt}},
		commandSpec{name: "delete", description: "Unregister a Location without deleting physical files", handler: &locationDeleteCommand{runtime: rt}},
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

func (c *locationCreateCommand) Execute(_ []string) error {
	location, err := c.location()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateLocationRequest, entity.CreateLocationResponse](ctx, c.runtime, entity.LocationService_Create_FullMethodName, &entity.CreateLocationRequest{Location: location})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *locationUpdateCommand) Execute(_ []string) error {
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}

	// Replace the complete name, root path, preference and Ignore configuration in one RPC.
	location, err := c.location()
	if err != nil {
		return err
	}
	location.Id = c.Args.ID
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.UpdateLocationRequest, entity.UpdateLocationResponse](ctx, c.runtime, entity.LocationService_Update_FullMethodName, &entity.UpdateLocationRequest{Location: location})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *locationGetCommand) Execute(_ []string) error {
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetLocationRequest, entity.GetLocationResponse](ctx, c.runtime, entity.LocationService_Get_FullMethodName, &entity.GetLocationRequest{Id: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *locationListCommand) Execute(_ []string) error {
	// Preserve the optional preference filter independently of literal catalog search.
	request := &entity.ListLocationsRequest{AfterId: c.AfterID, Limit: c.Limit, Query: c.Query}
	if c.RestoreTarget != nil {
		request.RestoreTarget = proto.Bool(*c.RestoreTarget == "true")
	}

	// Request one filtered catalog page without filesystem traversal.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListLocationsRequest, entity.ListLocationsResponse](ctx, c.runtime, entity.LocationService_List_FullMethodName, request)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *locationDeleteCommand) Execute(_ []string) error {
	// Unregistration is metadata-only; --dryrun reports it without removing the registration.
	if err := positiveID("Location ID", c.Args.ID); err != nil {
		return err
	}

	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.DeleteLocationRequest, entity.DeleteLocationResponse](ctx, c.runtime, entity.LocationService_Delete_FullMethodName, &entity.DeleteLocationRequest{Id: c.Args.ID, Dryrun: c.DryRun})
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
	mode := entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING
	switch c.Mode {
	case "basic":
		mode = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY
	case "force":
		mode = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateScanJobRequest, entity.CreateScanJobResponse](ctx, c.runtime, entity.ScanJobService_Create_FullMethodName, &entity.CreateScanJobRequest{Priority: c.Priority, Spec: &entity.ScanJobSpec{Selections: locationSelections(c.Args.ID, c.Paths), SignaturePolicy: mode, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, PreviewPolicy: preview}})
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
	reply, err := callRPC[entity.GetScanJobProgressRequest, entity.GetScanJobProgressResponse](ctx, c.runtime, entity.ScanJobService_GetProgress_FullMethodName, &entity.GetScanJobProgressRequest{Id: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *analyzeEntriesCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := c.jobResultPageOptions.validate(); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListScanJobEntriesRequest, entity.ListScanJobEntriesResponse](ctx, c.runtime, entity.ScanJobService_ListEntries_FullMethodName, &entity.ListScanJobEntriesRequest{
		Id: c.Args.ID, Limit: c.pageLimit(), Cursor: c.Cursor, Order: c.order(), Offset: c.Offset, IncludeTotal: c.IncludeTotal,
	})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *locationConfigOptions) location() (*entity.Location, error) {
	var text []byte
	if c.IgnoreFile != "" {
		var err error
		text, err = os.ReadFile(c.IgnoreFile)
		if err != nil {
			return nil, fmt.Errorf("read Ignore rules failed, %w", err)
		}
	}
	return &entity.Location{
		Name: c.Name, RootPath: c.Root, RestoreTarget: c.RestoreTarget,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: string(text)},
			UseMmap: c.UseMmap},
	}, nil
}
