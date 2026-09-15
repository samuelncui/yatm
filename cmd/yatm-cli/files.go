package main

import (
	"context"
	"fmt"

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

func getFilesEntry(ctx context.Context, rt *runtime, ref *entity.FileOperationRef) (*entity.FilesEntry, error) {
	return callRPC[entity.GetFilesEntryRequest, entity.FilesEntry](ctx, rt, entity.FilesService_Get_FullMethodName, &entity.GetFilesEntryRequest{Reference: ref})
}

type filesGetCommand struct {
	runtime *runtime
	entryOptions
}
type filesListCommand struct {
	runtime *runtime
	entryOptions
	filePageOptions
	Name     string `long:"name" description:"Name filter"`
	Query    string `long:"query" description:"Shared Files search query"`
	NeedSize bool   `long:"need-size" description:"Include recursive Library directory usage; unsupported for live Locations"`
}
type filesSelectionCommand struct {
	runtime *runtime
	selectionOptions
	collect   bool
	Automatic bool `long:"automatic" description:"Respect automatic collection preference and Ignore"`
}
type filesMetadataCommand struct {
	runtime *runtime
	entryOptions
	AddTags    []string `long:"add-tag" description:"Tag to add; repeatable"`
	RemoveTags []string `long:"remove-tag" description:"Tag to remove; repeatable"`
	Note       *string  `long:"note" description:"Replacement note; an empty value clears it"`
}

func registerFilesCommands(root *flags.Command, rt *runtime) error {
	group, err := addGroup(root, "files", "Browse and manage entries from Library or Locations")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "get", description: "Inspect one entry", handler: &filesGetCommand{runtime: rt}},
		commandSpec{name: "list", description: "List one directory or query page", handler: &filesListCommand{runtime: rt}},
		commandSpec{name: "inspect", description: "Check selected entries without hashing", handler: &filesSelectionCommand{runtime: rt}},
		commandSpec{name: "collect", description: "Collect selected entries in Library", handler: &filesSelectionCommand{runtime: rt, collect: true}},
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

func (c *filesListCommand) Execute(_ []string) error {
	// List is pure: browsing never calls Collect implicitly.
	ref, err := c.reference()
	if err != nil {
		return err
	}
	if c.Limit < 1 || c.Limit > 500 {
		return usageError(fmt.Errorf("limit must be between 1 and 500"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListFilesRequest, entity.ListFilesReply](ctx, c.runtime, entity.FilesService_List_FullMethodName, &entity.ListFilesRequest{Directory: ref, Cursor: c.Cursor, Limit: c.Limit, Scope: c.fileScope(), NameFilter: c.Name, Query: c.Query, NeedSize: c.NeedSize})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *filesSelectionCommand) Execute(_ []string) error {
	// Resolve selected roots through the common entry API before observation or admission.
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if len(selections) == 0 {
		return usageError(fmt.Errorf("at least one entry is required"))
	}
	if c.Automatic && !c.collect {
		return usageError(fmt.Errorf("automatic is only valid for collect"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	refs := make([]*entity.FileOperationRef, 0, len(selections))
	for _, selection := range selections {
		ref := fileReference(selection.GetLibrary().GetFileId())
		if source := selection.GetLocation(); source != nil {
			ref = locationReference(source.LocationId, source.Path)
		}
		entry, err := getFilesEntry(ctx, c.runtime, ref)
		if err != nil {
			return err
		}
		refs = append(refs, entry.Reference)
	}
	if c.collect {
		reply, err := callRPC[entity.CollectFilesRequest, entity.CollectFilesReply](ctx, c.runtime, entity.FilesService_Collect_FullMethodName, &entity.CollectFilesRequest{References: refs, Automatic: c.Automatic})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	}
	reply, err := callRPC[entity.InspectFilesRequest, entity.InspectFilesReply](ctx, c.runtime, entity.FilesService_Inspect_FullMethodName, &entity.InspectFilesRequest{References: refs})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *filesMetadataCommand) Execute(_ []string) error {
	// Metadata changes collect unadmitted files through the same server-side association path.
	ref, err := c.reference()
	if err != nil {
		return err
	}
	if len(c.AddTags) == 0 && len(c.RemoveTags) == 0 && c.Note == nil {
		return usageError(fmt.Errorf("at least one metadata change is required"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	entry, err := getFilesEntry(ctx, c.runtime, ref)
	if err != nil {
		return err
	}
	reply, err := callRPC[entity.UpdateFilesMetadataRequest, entity.FilesEntry](ctx, c.runtime, entity.FilesService_UpdateMetadata_FullMethodName, &entity.UpdateFilesMetadataRequest{Reference: entry.Reference, Note: c.Note, AddTags: c.AddTags, RemoveTags: c.RemoveTags})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
