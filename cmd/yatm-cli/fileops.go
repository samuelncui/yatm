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
	runtime       *runtime
	Kind          string   `long:"kind" required:"true" choice:"move" choice:"mkdir" choice:"delete" description:"Organization operation"`
	Library       bool     `long:"library" description:"Organize Library metadata instead of physical files"`
	LocationID    *int64   `long:"location" description:"Location ID; mutually exclusive with --library"`
	Sources       []string `long:"source" description:"Library File ID or Location-relative path; repeat for multiple selections"`
	Destination   *string  `long:"destination" description:"Existing target directory: Library File ID (0 for root) or Location path (. for root)"`
	Name          string   `long:"name" description:"New directory or single-source replacement name"`
	ConfirmDelete bool     `long:"confirm-delete" description:"Confirm deletion; Location deletion permanently removes physical files"`
}

func registerFileOperationCommands(root *flags.Command, rt *runtime) error {
	// Ordinary file operations finish within the request and stream bounded JSON Lines results.
	group, err := addGroup(root, "fileops", "Organize Library metadata or real files within one Location")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "run", description: "Execute a move, mkdir or deletion; emit JSON Lines until complete (no Job)", handler: &fileOperationRunCommand{runtime: rt}},
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
	if c.Kind == "delete" && !c.ConfirmDelete {
		return usageError(fmt.Errorf("deletion requires --confirm-delete"))
	}
	kinds := map[string]entity.FileOperationKind{"move": entity.FileOperationKind_MOVE, "mkdir": entity.FileOperationKind_MAKE_DIRECTORY, "delete": entity.FileOperationKind_DELETE}
	kind, ok := kinds[c.Kind]
	if !ok {
		return usageError(fmt.Errorf("unknown file operation %q", c.Kind))
	}
	spec := &entity.FileOperationSpec{Kind: kind, Name: c.Name}
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

	return executeFileOperation(ctx, c.runtime, &entity.ExecuteFileOperationRequest{Spec: spec, ConfirmDelete: c.ConfirmDelete})
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

func executeFileOperation(ctx context.Context, rt *runtime, request *entity.ExecuteFileOperationRequest) error {
	// The common stream owns execution; disconnecting cancels pending work instead of leaving a Job.
	client := connect.NewClient[entity.ExecuteFileOperationRequest, entity.FileOperationUpdate](
		rt.httpClient, rt.rpcURL(entity.FileOperationService_Execute_FullMethodName), connect.WithGRPCWeb())
	stream, err := client.CallServerStream(ctx, connect.NewRequest(request))
	if err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}
	defer stream.Close()
	var result *entity.FileOperationSummary
	for stream.Receive() {
		update := stream.Msg()
		if err := writeProto(rt.stdout, update); err != nil {
			return err
		}
		if update.GetSummary().GetCompleted() {
			result = update.Summary
		}
	}
	if err := stream.Err(); err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}

	// A settled stream can contain partial failures, which must remain failures for automation.
	if result == nil {
		return runtimeError("incomplete", fmt.Errorf("file operation ended without a final result"))
	}
	if result.Failed > 0 || result.Unprocessed > 0 || result.PublicationPending > 0 {
		return runtimeError("incomplete", fmt.Errorf("file operation incomplete: %d failed, %d unprocessed, %d Library updates pending", result.Failed, result.Unprocessed, result.PublicationPending))
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
	if reply.GetReference().GetLocation() == nil {
		return nil, runtimeError("internal", fmt.Errorf("Location returned no live object observation"))
	}
	return reply.Reference.GetLocation(), nil
}
