package main

import (
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

// Trash is the reserved persisted File identity, accepted only by read-only browsing commands.
const trashFileID int64 = -1

type fileIDArgs struct {
	ID int64 `positional-arg-name:"ID" required:"yes"`
}

type fileIDsArgs struct {
	IDs []int64 `positional-arg-name:"ID" required:"yes"`
}

type fileGetCommand struct {
	runtime *runtime
	filePageOptions
	NeedSize bool       `long:"need-size" description:"Include aggregate direct-child size"`
	Args     fileIDArgs `positional-args:"yes"`
}

type fileListCommand struct {
	runtime *runtime
	filePageOptions
	NeedSize bool `long:"need-size" description:"Include aggregate direct-child size"`
	Args     struct {
		ParentID int64 `positional-arg-name:"PARENT_ID"`
	} `positional-args:"yes"`
}

type fileParentsCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type fileSearchCommand struct {
	runtime *runtime
	scopeOptions
	LocationID int64   `long:"location-id" description:"Search physical paths in this Location"`
	Revision   int64   `long:"revision" description:"Published Location revision, required with location-id"`
	Limit      *int64  `long:"limit" description:"Maximum results in this page"`
	Cursor     *string `long:"cursor" description:"Page cursor"`
	Args       struct {
		Query string `positional-arg-name:"QUERY" required:"yes"`
	} `positional-args:"yes"`
}

type fileEditCommand struct {
	runtime  *runtime
	Name     *string    `long:"name" description:"New file name"`
	ParentID *int64     `long:"parent-id" description:"New parent File ID"`
	Args     fileIDArgs `positional-args:"yes"`
}

type fileMetadataCommand struct {
	runtime    *runtime
	AddTags    []string    `long:"add-tag" description:"Tag to add; repeatable"`
	RemoveTags []string    `long:"remove-tag" description:"Tag to remove; repeatable"`
	Note       *string     `long:"note" description:"Replacement note"`
	ClearNote  bool        `long:"clear-note" description:"Replace the note with an empty value"`
	Args       fileIDsArgs `positional-args:"yes"`
}

type fileMkdirCommand struct {
	runtime *runtime
	Args    struct {
		ParentID int64  `positional-arg-name:"PARENT_ID" required:"yes"`
		Path     string `positional-arg-name:"PATH" required:"yes"`
	} `positional-args:"yes"`
}

type fileDeleteCommand struct {
	runtime *runtime
	Confirm bool        `long:"confirm" description:"Confirm deletion of the resolved Library Files"`
	Args    fileIDsArgs `positional-args:"yes"`
}

type tagListCommand struct {
	runtime *runtime
	Prefix  *string `long:"prefix" description:"Tag prefix"`
	Limit   *int64  `long:"limit" description:"Maximum results in this page"`
	Cursor  *string `long:"cursor" description:"Page cursor"`
}

func registerFileCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "file", "Inspect and edit Library Files")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "get", description: "Get one File and its direct children", handler: &fileGetCommand{runtime: commandRuntime}},
		commandSpec{name: "list", description: "List one directory's direct children", handler: &fileListCommand{runtime: commandRuntime}},
		commandSpec{name: "parents", description: "List a File's parent chain", handler: &fileParentsCommand{runtime: commandRuntime}},
		commandSpec{name: "search", description: "Search Library Files", handler: &fileSearchCommand{runtime: commandRuntime}},
		commandSpec{name: "edit", description: "Rename or move a Library File", handler: &fileEditCommand{runtime: commandRuntime}},
		commandSpec{name: "metadata", description: "Edit File tags and note", handler: &fileMetadataCommand{runtime: commandRuntime}},
		commandSpec{name: "mkdir", description: "Create a Library directory", handler: &fileMkdirCommand{runtime: commandRuntime}},
		commandSpec{name: "delete", description: "Delete Library File metadata", handler: &fileDeleteCommand{runtime: commandRuntime}},
	)
}

func registerTagCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "tag", "Inspect Library tags")
	if err != nil {
		return err
	}
	return addCommands(group, commandSpec{
		name: "list", description: "List one page of tags", handler: &tagListCommand{runtime: commandRuntime},
	})
}

func (c *fileGetCommand) Execute(_ []string) error {
	// Root browsing belongs to file list; this command accepts persisted identities, including Trash.
	if c.Args.ID <= 0 && c.Args.ID != trashFileID {
		return usageError(fmt.Errorf(
			"File ID must be positive or the reserved Trash ID (%d), value=%d", trashFileID, c.Args.ID,
		))
	}
	return c.get(c.Args.ID)
}

func (c *fileGetCommand) get(id int64) error {
	// Apply scope before the server selects this page.
	if c.Limit <= 0 || c.Limit > 500 {
		return usageError(fmt.Errorf("limit must be between 1 and 500"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.FileGetRequest, entity.FileGetReply](
		ctx,
		c.runtime,
		entity.Service_FileGet_FullMethodName,
		&entity.FileGetRequest{Id: id, NeedSize: &c.NeedSize, Scope: c.fileScope(), Cursor: c.Cursor, Limit: c.Limit},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *fileListCommand) Execute(_ []string) error {
	// A directory page may target the virtual root, an ordinary directory or the persisted Trash root.
	if c.Args.ParentID < 0 && c.Args.ParentID != trashFileID {
		return usageError(fmt.Errorf(
			"parent File ID must be nonnegative or the reserved Trash ID (%d), value=%d", trashFileID, c.Args.ParentID,
		))
	}
	return (&fileGetCommand{
		runtime: c.runtime, filePageOptions: c.filePageOptions, NeedSize: c.NeedSize, Args: fileIDArgs{ID: c.Args.ParentID},
	}).get(c.Args.ParentID)
}

func (c *fileParentsCommand) Execute(_ []string) error {
	// Resolve ancestry only for persisted File identities, including the reserved Trash root.
	if c.Args.ID <= 0 && c.Args.ID != trashFileID {
		return usageError(fmt.Errorf(
			"File ID must be positive or the reserved Trash ID (%d), value=%d", trashFileID, c.Args.ID,
		))
	}

	// Read and emit the complete server-provided parent chain without changing any Library metadata.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.FileListParentsRequest, entity.FileListParentsReply](
		ctx,
		c.runtime,
		entity.Service_FileListParents_FullMethodName,
		&entity.FileListParentsRequest{Id: c.Args.ID},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *fileSearchCommand) Execute(_ []string) error {
	if strings.TrimSpace(c.Args.Query) == "" {
		return usageError(fmt.Errorf("search query is empty"))
	}
	if err := validateLimit("search limit", c.Limit); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.FileSearchRequest, entity.FileSearchReply](
		ctx,
		c.runtime,
		entity.Service_FileSearch_FullMethodName,
		&entity.FileSearchRequest{Query: c.Args.Query, Limit: c.Limit, Cursor: c.Cursor, Scope: c.fileScope(), LocationId: c.LocationID, LocationRevision: c.Revision},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *fileEditCommand) Execute(_ []string) error {
	// Validate the logical selection and requested change before resolving its current parent.
	if err := positiveID("File ID", c.Args.ID); err != nil {
		return err
	}
	if c.Name == nil && c.ParentID == nil {
		return usageError(fmt.Errorf("file edit requires --name or --parent-id"))
	}
	if c.ParentID != nil && *c.ParentID < 0 {
		return usageError(fmt.Errorf("parent File ID must not be negative, value=%d", *c.ParentID))
	}
	if c.Name != nil && strings.TrimSpace(*c.Name) == "" {
		return usageError(fmt.Errorf("file name is empty"))
	}

	// Preserve the existing parent when this is a name-only edit.
	ctx, cancel := c.runtime.context()
	defer cancel()
	parent := c.ParentID
	if parent == nil {
		reply, err := callRPC[entity.FileGetRequest, entity.FileGetReply](ctx, c.runtime,
			entity.Service_FileGet_FullMethodName, &entity.FileGetRequest{Id: c.Args.ID})
		if err != nil {
			return err
		}
		if reply.File == nil {
			return runtimeError("not_found", fmt.Errorf("File does not exist, id=%d", c.Args.ID))
		}
		parent = &reply.File.ParentId
	}

	// The shared operation preserves nested logical paths and streams the affected identity.
	spec := &entity.FileOperationSpec{
		Kind:        entity.FileOperationKind_MOVE,
		Sources:     []*entity.FileOperationRef{{Target: &entity.FileOperationRef_FileId{FileId: c.Args.ID}}},
		Destination: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: *parent}},
	}
	if c.Name != nil {
		spec.Name = *c.Name
	}
	return executeFileOperation(ctx, c.runtime, &entity.ExecuteFileOperationRequest{Spec: spec})
}

func (c *fileMetadataCommand) Execute(_ []string) error {
	// Validate and normalize the complete metadata edit before sending it.
	ids, err := positiveIDs("File ID", c.Args.IDs)
	if err != nil {
		return err
	}
	if c.Note != nil && c.ClearNote {
		return usageError(fmt.Errorf("note and clear-note cannot be combined"))
	}
	note := c.Note
	if c.ClearNote {
		value := ""
		note = &value
	}
	if len(c.AddTags) == 0 && len(c.RemoveTags) == 0 && note == nil {
		return usageError(fmt.Errorf("file metadata requires at least one change"))
	}

	// Apply the resolved edit to all requested Files in one RPC.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.FileMetadataEditRequest, entity.FileMetadataEditReply](
		ctx,
		c.runtime,
		entity.Service_FileMetadataEdit_FullMethodName,
		&entity.FileMetadataEditRequest{
			Ids: ids, AddTags: c.AddTags, RemoveTags: c.RemoveTags, Note: note,
		},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *fileMkdirCommand) Execute(_ []string) error {
	// Keep the logical root and nested-directory command syntax without accepting invalid parents.
	if c.Args.ParentID < 0 {
		return usageError(fmt.Errorf("parent File ID must not be negative, value=%d", c.Args.ParentID))
	}
	if strings.TrimSpace(c.Args.Path) == "" {
		return usageError(fmt.Errorf("directory path is empty"))
	}

	// Directory creation uses the same execution stream as other organization operations.
	ctx, cancel := c.runtime.context()
	defer cancel()
	return executeFileOperation(ctx, c.runtime, &entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{
		Kind: entity.FileOperationKind_MAKE_DIRECTORY, Name: c.Args.Path,
		Destination: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: c.Args.ParentID}},
	}})
}

func (c *fileDeleteCommand) Execute(_ []string) error {
	// Require confirmation and a valid target set before any remote reads.
	if !c.Confirm {
		return safetyError(fmt.Errorf("file delete requires --confirm"))
	}
	ids, err := positiveIDs("File ID", c.Args.IDs)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Resolve every File immediately before deleting its Library metadata.
	for _, id := range ids {
		reply, err := callRPC[entity.FileGetRequest, entity.FileGetReply](
			ctx,
			c.runtime,
			entity.Service_FileGet_FullMethodName,
			&entity.FileGetRequest{Id: id},
		)
		if err != nil {
			return err
		}
		if reply.File == nil {
			return runtimeError("not_found", fmt.Errorf("File does not exist, id=%d", id))
		}
	}

	// Submit one logical deletion; Library Trash semantics never become a physical deletion.
	sources := make([]*entity.FileOperationRef, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}})
	}
	return executeFileOperation(ctx, c.runtime, &entity.ExecuteFileOperationRequest{
		Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: sources}, ConfirmDelete: true,
	})
}

func (c *tagListCommand) Execute(_ []string) error {
	if err := validateLimit("tag limit", c.Limit); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.TagListRequest, entity.TagListReply](
		ctx,
		c.runtime,
		entity.Service_TagList_FullMethodName,
		&entity.TagListRequest{Prefix: c.Prefix, Limit: c.Limit, Cursor: c.Cursor},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
