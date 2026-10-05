package main

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type entryOptions struct {
	FileID     *int64 `long:"file-id" description:"Library File ID; 0 is the Library root"`
	LocationID *int64 `long:"location-id" description:"Location ID; exclusive with file-id"`
	Path       string `long:"path" description:"Path within the Location; empty for root"`
}

func (o entryOptions) reference() (*entity.FileOperationRef, error) {
	// Initial references identify the source; Get supplies current guarded observations.
	if (o.FileID != nil) == (o.LocationID != nil) {
		return nil, usageError(fmt.Errorf("choose exactly one of file-id or location-id"))
	}
	if o.FileID != nil {
		if *o.FileID < 0 && *o.FileID != trashFileID {
			return nil, usageError(fmt.Errorf("invalid File ID"))
		}
		if o.Path != "" {
			return nil, usageError(fmt.Errorf("path requires location-id"))
		}
		return fileReference(*o.FileID), nil
	}
	if err := positiveID("Location ID", *o.LocationID); err != nil {
		return nil, err
	}
	return locationReference(*o.LocationID, o.Path), nil
}

func fileReference(id int64) *entity.FileOperationRef {
	return &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}
}

func locationReference(id int64, path string) *entity.FileOperationRef {
	return &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: id, Path: path}}}
}

func getFilesEntry(ctx context.Context, rt *runtime, ref *entity.FileOperationRef) (*entity.FilesDetail, error) {
	reply, err := callRPC[entity.GetFileRequest, entity.GetFileResponse](ctx, rt, entity.FilesService_Get_FullMethodName, &entity.GetFileRequest{Reference: ref})
	if err != nil {
		return nil, err
	}
	return reply.GetDetail(), nil
}

type filesGetCommand struct {
	runtime *runtime
	entryOptions
}
type filesListCommand struct {
	runtime *runtime
	entryOptions
	scopeOptions
	Args struct {
		Path *string `positional-arg-name:"PATH" description:"Library path or location://NAME/path; defaults to the Library root"`
	} `positional-args:"yes"`
	Query     string   `long:"query" description:"Shared Files search query; switches the command to Search"`
	Cursor    string   `long:"cursor" description:"Search page cursor; requires --query or --recursive"`
	Limit     *int32   `long:"limit" description:"Maximum results in one search page, 1 to 500 (default: 100); requires --query or --recursive"`
	Long      bool     `short:"l" long:"long" description:"Include size and modification time"`
	Status    bool     `long:"status" description:"Include original availability and backup coverage"`
	Include   []string `long:"include" choice:"attributes" choice:"status" choice:"operations" choice:"navigation" description:"Additional list data group; repeatable"`
	Recursive bool     `long:"recursive" description:"Search the Library subtree"`
}
type filesMetadataCommand struct {
	runtime *runtime
	selectionOptions
	AddTags    []string `long:"add-tag" description:"Tag to add; repeatable"`
	RemoveTags []string `long:"remove-tag" description:"Tag to remove; repeatable"`
	Note       *string  `long:"note" description:"Replacement note; an empty value clears it"`
}

func registerFilesCommands(root *flags.Command, rt *runtime) error {
	group, err := addGroup(root, "files", "Browse and manage entries from Library or Locations")
	if err != nil {
		return err
	}
	if err := addCommands(root,
		commandSpec{name: "ls", description: "List a directory or query page; minimal by default", handler: &filesListCommand{runtime: rt}},
		commandSpec{name: "du", description: "Measure all matching entries and directory contents", handler: &filesMeasureCommand{runtime: rt}},
	); err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "get", description: "Inspect one entry", handler: &filesGetCommand{runtime: rt}},
		commandSpec{name: "metadata", description: "Edit an entry's tags and note", handler: &filesMetadataCommand{runtime: rt}},
	)
}

func (c *filesGetCommand) Execute(_ []string) error {
	ref, err := c.reference()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	entry, err := getFilesEntry(ctx, c.runtime, ref)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, entry)
}

func (c *filesListCommand) searchLimit() (int32, error) {
	// A complete directory read has no cursor; paging applies to query or recursive Search.
	if c.Query == "" && !c.Recursive {
		if c.Cursor != "" || c.Limit != nil {
			return 0, usageError(fmt.Errorf("a directory listing is complete; a page size and a cursor apply to --query or --recursive"))
		}
		return 0, nil
	}

	// Reject invalid query bounds before resolving a positional path through the server.
	limit := int32(100)
	if c.Limit != nil {
		limit = *c.Limit
	}
	if limit < 1 || limit > 500 {
		return 0, usageError(fmt.Errorf("limit must be between 1 and 500"))
	}
	return limit, nil
}

func (c *filesListCommand) Execute(_ []string) error {
	// Validate the listing mode before a path lookup can issue any request.
	limit, err := c.searchLimit()
	if err != nil {
		return err
	}

	// Resolve and read within one deadline; no client working directory selects server paths.
	ctx, cancel := c.runtime.context()
	defer cancel()
	ref, err := c.directory(ctx)
	if err != nil {
		return err
	}

	// Request only the data groups the operator selected.
	var include []entity.FilesInclude
	if c.Long {
		include = append(include, entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES)
	}
	if c.Status {
		include = append(include, entity.FilesInclude_FILES_INCLUDE_STATUS)
	}
	for _, name := range c.Include {
		value := map[string]entity.FilesInclude{
			"attributes": entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES,
			"status":     entity.FilesInclude_FILES_INCLUDE_STATUS,
			"operations": entity.FilesInclude_FILES_INCLUDE_OPERATIONS,
			"navigation": entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
		}[name]
		found := false
		for _, existing := range include {
			found = found || existing == value
		}
		if !found {
			include = append(include, value)
		}
	}

	// The resolved identity uses the same complete List or paged Search as an ID-selected directory.
	if limit == 0 {
		return c.listFiles(ctx, ref, include)
	}
	return c.searchFiles(ctx, ref, include, limit)
}

// listFiles reads one directory completely: the server enumerates it once and the command
// writes each batch, so a listing has no cursor and no page flags.
func (c *filesListCommand) listFiles(ctx context.Context, ref *entity.FileOperationRef, include []entity.FilesInclude) error {
	// Keep the stream open through every row so individual failures cannot hide usable siblings.
	client := connect.NewClient[entity.ListFilesRequest, entity.ListFilesResponse](
		c.runtime.httpClient,
		c.runtime.rpcURL(entity.FilesService_List_FullMethodName),
		connect.WithGRPCWeb(),
	)
	stream, err := client.CallServerStream(ctx, connect.NewRequest(&entity.ListFilesRequest{Directory: ref, Scope: c.fileScope(), Include: include}))
	if err != nil {
		return runtimeError(connect.CodeOf(err).String(), fmt.Errorf("call RPC failed, method=%q, %w", entity.FilesService_List_FullMethodName, err))
	}
	defer stream.Close()

	// Preserve complete NDJSON evidence before returning an incomplete-read exit status.
	failed := 0
	for stream.Receive() {
		if err := writeProto(c.runtime.stdout, stream.Msg()); err != nil {
			return err
		}
		for _, entry := range stream.Msg().Entries {
			if entry.GetError() != "" {
				failed++
			}
		}
	}

	// A transport failure retains its own code; settled child failures report an incomplete read.
	if err := stream.Err(); err != nil {
		return runtimeError(connect.CodeOf(err).String(), fmt.Errorf("call RPC failed, method=%q, %w", entity.FilesService_List_FullMethodName, err))
	}
	if failed > 0 {
		return runtimeError("incomplete", fmt.Errorf("directory listing contains %d unreadable entries", failed))
	}
	return nil
}

// searchFiles answers one bounded page of a query, which keeps its cursor and page size.
func (c *filesListCommand) searchFiles(ctx context.Context, ref *entity.FileOperationRef, include []entity.FilesInclude, limit int32) error {
	reply, err := callRPC[entity.SearchFilesRequest, entity.SearchFilesResponse](ctx, c.runtime, entity.FilesService_Search_FullMethodName,
		&entity.SearchFilesRequest{Directory: ref, Cursor: c.Cursor, Limit: limit, Scope: c.fileScope(), Query: c.Query, Recursive: c.Recursive, Include: include})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *filesMetadataCommand) Execute(_ []string) error {
	// Preserve every selected entry; saving establishes any necessary association server-side.
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if len(selections) == 0 {
		return usageError(fmt.Errorf("at least one entry is required"))
	}
	for _, selection := range selections {
		if source := selection.GetLibrary(); source != nil && source.FileId == 0 {
			return usageError(fmt.Errorf("metadata requires a File, not the Library root"))
		}
	}
	if len(c.AddTags) == 0 && len(c.RemoveTags) == 0 && c.Note == nil {
		return usageError(fmt.Errorf("at least one metadata change is required"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	// Resolve physical observations only; logical identities need no redundant detail read.
	refs := make([]*entity.FileOperationRef, 0, len(selections))
	for _, selection := range selections {
		if source := selection.GetLocation(); source != nil {
			ref, err := locationOperationRef(ctx, c.runtime, source.LocationId, source.Path)
			if err != nil {
				return err
			}
			refs = append(refs, &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: ref}})
			continue
		}
		refs = append(refs, fileReference(selection.GetLibrary().GetFileId()))
	}
	reply, err := callRPC[entity.UpdateFilesMetadataRequest, entity.UpdateFilesMetadataResponse](ctx, c.runtime, entity.FilesService_UpdateMetadata_FullMethodName, &entity.UpdateFilesMetadataRequest{References: refs, Note: c.Note, AddTags: c.AddTags, RemoveTags: c.RemoveTags})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
