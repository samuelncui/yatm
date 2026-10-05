package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type fileOperationRunCommand struct {
	runtime     *runtime
	kind        entity.FileOperationKind
	Library     bool     `long:"library" description:"Organize Library metadata instead of physical files"`
	LocationID  *int64   `long:"location" description:"Location ID; mutually exclusive with --library"`
	Sources     []string `long:"source" description:"Library File ID or Location-relative path; repeat for multiple selections"`
	Destination *string  `long:"destination" description:"Existing target directory: Library File ID (0 for root) or Location path (. for root)"`
	Name        string   `long:"name" description:"New directory or single-source replacement name"`
	DryRun      bool     `long:"dryrun" description:"Report the resolved plan without changing files or Library metadata"`
}

func registerFileOperationCommands(root *flags.Command, rt *runtime) error {
	// Ordinary file operations finish within the request and stream bounded JSON Lines results.
	return addCommands(root,
		commandSpec{name: "mv", description: "Move or rename selected entries", handler: &fileOperationRunCommand{runtime: rt, kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE}},
		commandSpec{name: "mkdir", description: "Create a directory", handler: &fileOperationRunCommand{runtime: rt, kind: entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR}},
		commandSpec{name: "rm", description: "Move selected entries into Trash", handler: &fileOperationRunCommand{runtime: rt, kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE}},
	)
}

func (c *fileOperationRunCommand) Execute(_ []string) error {
	// Choose one namespace before resolving any targets or sending mutation requests.
	if c.Library == (c.LocationID != nil) {
		return usageError(fmt.Errorf("choose exactly one of --library or --location"))
	}
	if c.LocationID != nil {
		if err := positiveID("Location ID", *c.LocationID); err != nil {
			return err
		}
	}
	if c.kind == entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR {
		if len(c.Sources) > 0 || c.Destination == nil || c.Name == "" {
			return usageError(fmt.Errorf("mkdir requires destination and name, without sources"))
		}
	} else if len(c.Sources) == 0 {
		return usageError(fmt.Errorf("at least one source is required"))
	}
	if c.kind == entity.FileOperationKind_FILE_OPERATION_KIND_MOVE && c.Destination == nil {
		return usageError(fmt.Errorf("mv requires destination"))
	}
	if c.kind == entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE && (c.Destination != nil || c.Name != "") {
		return usageError(fmt.Errorf("rm does not accept destination or name"))
	}
	if c.kind == entity.FileOperationKind_FILE_OPERATION_KIND_MOVE && c.Name != "" && len(c.Sources) != 1 {
		return usageError(fmt.Errorf("name requires exactly one source"))
	}

	// Keep the submitted name literal while resolving the operation's references.
	spec := &entity.FileOperationSpec{Kind: c.kind, Name: c.Name}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Build the same typed operation regardless of whether references name logical or physical objects.
	for _, source := range c.Sources {
		ref, err := c.reference(ctx, source, false)
		if err != nil {
			return err
		}
		spec.Sources = append(spec.Sources, ref)
	}
	if c.Destination != nil {
		ref, err := c.reference(ctx, *c.Destination, true)
		if err != nil {
			return err
		}
		spec.Destination = ref
	}

	// Run the selected mutation through its bounded result stream.
	switch c.kind {
	case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE:
		return executeFileOperation(ctx, c.runtime, entity.FilesService_Move_FullMethodName,
			&entity.MoveFilesRequest{Sources: spec.Sources, Destination: spec.Destination, Name: spec.Name, Dryrun: c.DryRun},
			(*entity.MoveFilesResponse).GetResult)
	case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		return executeFileOperation(ctx, c.runtime, entity.FilesService_Mkdir_FullMethodName,
			&entity.MkdirFilesRequest{Destination: spec.Destination, Name: spec.Name, Dryrun: c.DryRun},
			(*entity.MkdirFilesResponse).GetResult)
	default:
		return executeFileOperation(ctx, c.runtime, entity.FilesService_Remove_FullMethodName,
			&entity.RemoveFilesRequest{Sources: spec.Sources, Dryrun: c.DryRun},
			(*entity.RemoveFilesResponse).GetResult)
	}
}

func (c *fileOperationRunCommand) reference(ctx context.Context, value string, destination bool) (*entity.FileOperationRef, error) {
	// Physical selections require a fresh object observation within the registered Location.
	if !c.Library {
		ref, err := locationOperationRef(ctx, c.runtime, *c.LocationID, value)
		if err != nil {
			return nil, err
		}
		return &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: ref}}, nil
	}

	// The Library root is an explicit destination, never a mutable source.
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, usageError(fmt.Errorf("invalid Library File ID %q, %w", value, err))
	}
	if id < 0 || (id == 0 && !destination) {
		return nil, usageError(fmt.Errorf("Library File ID must be positive (or 0 for a destination), value=%d", id))
	}
	return &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}, nil
}

func executeFileOperation[T, R any](ctx context.Context, rt *runtime, method string, request *T, resultOf func(*R) *entity.FileOperationResult) error {
	// The common stream owns execution; disconnecting cancels pending work instead of leaving a Job.
	client := connect.NewClient[T, R](
		rt.httpClient, rt.rpcURL(method), connect.WithGRPCWeb())
	stream, err := client.CallServerStream(ctx, connect.NewRequest(request))
	if err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}
	defer stream.Close()
	var result *entity.FileOperationSummary
	var planError string
	for stream.Receive() {
		update := resultOf(stream.Msg())
		if err := writeProto(rt.stdout, update); err != nil {
			return err
		}
		if update.GetEntry().GetError() != "" {
			planError = update.Entry.Error
		}
		if update.GetSummary().GetCompleted() {
			result = update.Summary
		}
	}
	if err := stream.Err(); err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}

	// A dry run leaves planned entries unprocessed; real execution must settle every selected item.
	if result == nil {
		return runtimeError("incomplete", fmt.Errorf("file operation ended without a final result"))
	}
	if result.Dryrun && planError != "" {
		return runtimeError("incomplete", fmt.Errorf("file operation plan failed: %s", planError))
	}
	if result.FailedCount > 0 || result.PublicationPendingCount > 0 || (!result.Dryrun && result.UnprocessedCount > 0) {
		return runtimeError("incomplete", fmt.Errorf("file operation incomplete: %d failed, %d unprocessed, %d Library updates pending", result.FailedCount, result.UnprocessedCount, result.PublicationPendingCount))
	}
	return nil
}

func locationOperationRef(ctx context.Context, rt *runtime, id int64, relative string) (*entity.LocationEntryRef, error) {
	// The root has an explicit empty relative path; other names remain exact user selections.
	if relative == "." {
		relative = ""
	}
	if strings.ContainsRune(relative, 0) {
		return nil, usageError(fmt.Errorf("path contains NUL"))
	}
	reply, err := getFilesEntry(ctx, rt, locationReference(id, relative))
	if err != nil {
		return nil, err
	}
	if reply.GetEntry().GetReference().GetLocation() == nil {
		return nil, runtimeError("internal", fmt.Errorf("Location returned no live object observation"))
	}
	return reply.GetEntry().GetReference().GetLocation(), nil
}
