package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type previewCreateCommand struct {
	runtime *runtime
	selectionOptions
	Priority      int64  `long:"priority" default:"1" description:"Job priority"`
	ForceRehash   bool   `long:"force-rehash" description:"Hash source content without using cached signatures"`
	PreviewPolicy string `long:"preview-policy" choice:"none" choice:"missing-only" choice:"regenerate-all" default:"missing-only" description:"Preview generation policy"`
}

type scanCreateCommand struct {
	runtime       *runtime
	defaultResult entity.ScanResultPolicy
	selectionOptions
	Priority       int64    `long:"priority" default:"0" description:"Job priority"`
	MediaID        int64    `long:"media-id" description:"Media to scan; exclusive with Location or Library selections"`
	LocationID     int64    `long:"location-id" description:"Location to scan; exclusive with Media or selected entries"`
	Paths          []string `long:"path" description:"Relative root within --location-id; repeatable"`
	Signature      string   `long:"signature" choice:"known-only" choice:"fill-missing" choice:"force-read" default:"fill-missing" description:"Content fact policy"`
	Result         string   `long:"result" choice:"report" choice:"originals" choice:"inventory" choice:"verify" description:"Result publication policy (default: report; scan media: inventory)"`
	CompareLibrary bool     `long:"compare-library" description:"Find matching archived content"`
	PreviewPolicy  string   `long:"preview-policy" choice:"none" choice:"missing-only" choice:"regenerate-all" default:"none" description:"Preview generation policy"`
	ForceRehash    bool     `long:"force-rehash" description:"Use force-read signature policy"`
	Args           struct {
		MediaID int64 `positional-arg-name:"MEDIA_ID"`
	} `positional-args:"yes"`
}

type scanProgressCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type scanResultsCommand struct {
	runtime *runtime
	Limit   *int32     `long:"limit" description:"Maximum results in this page"`
	AfterID *int64     `long:"after-id" description:"Stable entry ID cursor"`
	Args    fileIDArgs `positional-args:"yes"`
}

type libraryExportCommand struct {
	runtime *runtime
	Output  string `long:"output" value-name:"FILE" required:"yes" description:"Local destination or - for stdout"`
}

type libraryImportCommand struct {
	runtime *runtime
	Input   string `long:"input" value-name:"FILE" required:"yes" description:"Local source or - for stdin"`
	Confirm bool   `long:"confirm" description:"Confirm importing Library data"`
}

type libraryTrimCommand struct {
	runtime   *runtime
	Positions bool `long:"positions" description:"Trim invalid Positions"`
	Files     bool `long:"files" description:"Trim Files without originals or version history"`
	Confirm   bool `long:"confirm" description:"Confirm trimming Library metadata"`
}

func registerPreviewCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "preview", "Generate Previews")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "create", description: "Create a Scan with previews", handler: &previewCreateCommand{runtime: commandRuntime}},
	)
}

func registerScanCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "scan", "Scan originals or Media using one content pipeline")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "create", description: "Create a Scan with explicit content and result policies", handler: &scanCreateCommand{runtime: commandRuntime}},
		commandSpec{name: "media", description: "Scan Media inventory", handler: &scanCreateCommand{runtime: commandRuntime, defaultResult: entity.ScanResultPolicy_PUBLISH_INVENTORY}},
		commandSpec{name: "run", description: "Read an explicitly selected Volume or Tape", handler: &verifyRunCommand{runtime: commandRuntime}},
		commandSpec{name: "scopes", description: "List completed or failed observation scopes", handler: &scanScopesCommand{runtime: commandRuntime}},
		commandSpec{name: "progress", description: "Get Scan progress and counts", handler: &scanProgressCommand{runtime: commandRuntime}},
		commandSpec{name: "results", description: "List one Location or Media scan result page", handler: &scanResultsCommand{runtime: commandRuntime}},
	)
}

func registerLibraryCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "library", "Transfer and trim Library metadata")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "export", description: "Export a JSON Lines Library snapshot", handler: &libraryExportCommand{runtime: commandRuntime}},
		commandSpec{name: "import", description: "Import a Library snapshot", handler: &libraryImportCommand{runtime: commandRuntime}},
		commandSpec{name: "trim", description: "Trim invalid Library metadata", handler: &libraryTrimCommand{runtime: commandRuntime}},
	)
}

func (c *previewCreateCommand) Execute(_ []string) error {
	// Validate priority before resolving registered file selections.
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative, value=%d", c.Priority))
	}
	preview, err := parsePreviewPolicy(c.PreviewPolicy)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if len(selections) == 0 {
		return usageError(fmt.Errorf("at least one file-id or location selection is required"))
	}

	// Preview is one stage in the common Scan, not another Job kind.
	policy := entity.ScanSignaturePolicy_FILL_MISSING
	if c.ForceRehash {
		policy = entity.ScanSignaturePolicy_FORCE_READ
	}
	reply, err := callRPC[entity.CreateScanJobRequest, entity.CreateScanJobReply](
		ctx,
		c.runtime,
		entity.ScanJobService_Create_FullMethodName,
		&entity.CreateScanJobRequest{
			Priority: c.Priority,
			Spec: &entity.ScanJobSpec{
				Selections:      selections,
				SignaturePolicy: policy,
				ResultPolicy:    entity.ScanResultPolicy_PUBLISH_ORIGINALS,
				PreviewPolicy:   preview,
			},
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *scanCreateCommand) Execute(_ []string) error {
	// Require one explicit source; a content policy never silently adds other sources.
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if c.MediaID != 0 && c.Args.MediaID != 0 {
		return usageError(fmt.Errorf("Media ID was specified twice"))
	}
	mediaID := c.MediaID
	if mediaID == 0 {
		mediaID = c.Args.MediaID
	}
	sources := 0
	if mediaID != 0 {
		sources++
	}
	if c.LocationID != 0 {
		sources++
	}
	if len(selections) > 0 {
		sources++
	}
	if sources != 1 || mediaID < 0 || c.LocationID < 0 {
		return usageError(fmt.Errorf("choose one Media, Location, or set of file selections"))
	}
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative"))
	}
	if len(c.Paths) > 0 && c.LocationID == 0 {
		return usageError(fmt.Errorf("path requires location-id"))
	}
	preview, err := parsePreviewPolicy(c.PreviewPolicy)
	if err != nil {
		return err
	}

	// Validate the policy matrix before creating the durable Scan.
	signatures := map[string]entity.ScanSignaturePolicy{"known-only": entity.ScanSignaturePolicy_KNOWN_ONLY, "fill-missing": entity.ScanSignaturePolicy_FILL_MISSING, "force-read": entity.ScanSignaturePolicy_FORCE_READ}
	results := map[string]entity.ScanResultPolicy{"report": entity.ScanResultPolicy_REPORT_ONLY, "originals": entity.ScanResultPolicy_PUBLISH_ORIGINALS, "inventory": entity.ScanResultPolicy_PUBLISH_INVENTORY, "verify": entity.ScanResultPolicy_VERIFY_COPIES}
	policy, result := signatures[c.Signature], results[c.Result]
	if c.Result == "" {
		result = c.defaultResult
	}
	if c.ForceRehash {
		policy = entity.ScanSignaturePolicy_FORCE_READ
	}
	if mediaID == 0 && (result == entity.ScanResultPolicy_PUBLISH_INVENTORY || result == entity.ScanResultPolicy_VERIFY_COPIES) {
		return usageError(fmt.Errorf("inventory and verify require Media"))
	}
	if mediaID != 0 && result == entity.ScanResultPolicy_PUBLISH_ORIGINALS {
		return usageError(fmt.Errorf("originals requires Location or Library selections"))
	}
	if result == entity.ScanResultPolicy_VERIFY_COPIES {
		policy = entity.ScanSignaturePolicy_FORCE_READ
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateScanJobRequest, entity.CreateScanJobReply](ctx, c.runtime, entity.ScanJobService_Create_FullMethodName,
		&entity.CreateScanJobRequest{Priority: c.Priority, Spec: &entity.ScanJobSpec{MediaId: mediaID, LocationId: c.LocationID, Paths: c.Paths, Selections: selections, SignaturePolicy: policy, ResultPolicy: result, CompareLibrary: c.CompareLibrary, PreviewPolicy: preview}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *scanProgressCommand) Execute(args []string) error {
	return (&jobProgressCommand{runtime: c.runtime, Args: c.Args}).Execute(args)
}

func parsePreviewPolicy(value string) (entity.PreviewPolicy, error) {
	switch value {
	case "none":
		return entity.PreviewPolicy_PREVIEW_NONE, nil
	case "missing-only":
		return entity.PreviewPolicy_PREVIEW_MISSING_ONLY, nil
	case "regenerate-all":
		return entity.PreviewPolicy_PREVIEW_REGENERATE_ALL, nil
	default:
		return entity.PreviewPolicy_PREVIEW_NONE, usageError(fmt.Errorf("invalid Preview policy %q", value))
	}
}

func (c *scanResultsCommand) Execute(_ []string) error {
	// Validate page controls before selecting the typed result service.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateLimit32("Scan Diff limit", c.Limit); err != nil {
		return err
	}
	if c.AfterID != nil && *c.AfterID < 0 {
		return usageError(fmt.Errorf("after-id must not be negative"))
	}
	limit := int32(0)
	if c.Limit != nil {
		limit = *c.Limit
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	job, err := getJob(ctx, c.runtime, c.Args.ID)
	if err != nil {
		return err
	}
	if job.Job == nil {
		return runtimeError("internal", fmt.Errorf("Job lookup returned no Job"))
	}

	if job.Job.Kind != entity.JobKind_SCAN {
		return usageError(fmt.Errorf("Job is not a Location or Media scan"))
	}
	reply, err := callRPC[entity.ListScanJobEntriesRequest, entity.ListScanJobEntriesReply](
		ctx,
		c.runtime,
		entity.ScanJobService_ListEntries_FullMethodName,
		&entity.ListScanJobEntriesRequest{Id: c.Args.ID, Limit: limit, AfterId: c.AfterID},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *libraryExportCommand) Execute(_ []string) error {
	// Apply one request deadline to either supported export destination.
	ctx, cancel := c.runtime.context()
	defer cancel()
	if c.Output != "-" {
		return c.runtime.saveLibraryExport(ctx, c.Output)
	}

	// Preserve the server's bounded JSON Lines stream on stdout.
	response, err := c.runtime.request(ctx, http.MethodGet, "/library/_export", nil)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(c.runtime.stdout, response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return runtimeError("io", fmt.Errorf("stream Library export failed, %w", err))
	}
	return nil
}

func (c *libraryImportCommand) Execute(_ []string) error {
	if !c.Confirm {
		return safetyError(fmt.Errorf("library import requires --confirm"))
	}

	// Open the local input before checking the remote mutation target.
	input := io.Reader(c.runtime.stdin)
	var file *os.File
	if c.Input != "-" {
		var err error
		file, err = os.Open(c.Input)
		if err != nil {
			return runtimeError("io", fmt.Errorf("open Library import failed, path=%q, %w", c.Input, err))
		}
		defer file.Close()
		input = file
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	if err := c.runtime.checkStatus(ctx); err != nil {
		return err
	}

	// Stream the confirmed snapshot into the server once.
	response, err := c.runtime.request(ctx, http.MethodPost, "/library/_import", input)
	if err != nil {
		return err
	}
	var result struct {
		Result string `json:"result"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&result)
	closeErr := response.Body.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return runtimeError("http", fmt.Errorf("read Library import response failed, %w", err))
	}
	return writeJSON(c.runtime.stdout, result)
}

func (c *libraryTrimCommand) Execute(_ []string) error {
	// Resolve the confirmed trim scope before any remote checks.
	if !c.Confirm {
		return safetyError(fmt.Errorf("library trim requires --confirm"))
	}
	if !c.Positions && !c.Files {
		return usageError(fmt.Errorf("library trim requires --positions or --files"))
	}

	// Verify both server paths before changing Library metadata.
	ctx, cancel := c.runtime.context()
	defer cancel()
	if err := c.runtime.checkStatus(ctx); err != nil {
		return err
	}

	// Apply the selected trims in one RPC.
	reply, err := callRPC[entity.LibraryTrimRequest, entity.LibraryTrimReply](
		ctx,
		c.runtime,
		entity.Service_LibraryTrim_FullMethodName,
		&entity.LibraryTrimRequest{TrimPosition: c.Positions, TrimFile: c.Files},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
