package main

import (
	"fmt"
	"strconv"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type identicalScopeOptions struct {
	Source string   `long:"source" choice:"library" choice:"locations" default:"library" description:"Content source"`
	Roots  []string `long:"root" description:"Location ID (repeatable)"`
}
type identicalCommand struct {
	runtime   *runtime
	operation string
	identicalScopeOptions
	Cursor        string  `long:"cursor" description:"Opaque page cursor"`
	Limit         int32   `long:"limit" description:"Maximum page size (rows default to 100; groups and members default to 20)"`
	Result        string  `long:"result" description:"Find result ID for paging without recomputing"`
	Offset        int64   `long:"offset" description:"Zero-based row offset"`
	IncludeHidden bool    `long:"include-hidden" description:"Include dot-name files in row listing"`
	SortKey       string  `long:"sort-key" choice:"file-id" choice:"name" choice:"size" default:"file-id" description:"Member order within each group"`
	Order         string  `long:"order" choice:"asc" choice:"desc" description:"Member sort direction (size defaults to desc; other keys to asc)"`
	FileIDs       []int64 `long:"file-id" description:"File ID to locate in a Find result (repeatable)"`
	Group         string  `long:"group" description:"Group ID from list"`
	Fingerprint   string  `long:"fingerprint" description:"Complete group fingerprint from list"`
	Target        int64   `long:"target-file" description:"Library merge target File ID"`
	KeepLocation  int64   `long:"keep-location" description:"Location containing the survivor"`
	KeepPath      string  `long:"keep-path" description:"Survivor's Location-relative path"`
	DryRun        bool    `long:"dryrun" description:"Report what the operation would change without changing it"`
}
type removeVersionCommand struct {
	runtime   *runtime
	FileID    int64 `long:"file-id" required:"yes" description:"Owning File ID"`
	VersionID int64 `long:"version-id" required:"yes" description:"Saved version to remove"`
	DryRun    bool  `long:"dryrun" description:"Report the record that would be removed without removing it"`
}

func registerIdenticalCommands(root *flags.Command, rt *runtime) error {
	// Keep independent-tool commands separate from ordinary file browsing.
	group, err := root.AddCommand("identical", "Find and process matching recorded content", "", &struct{}{})
	if err != nil {
		return err
	}
	for _, operation := range []string{"find", "rows", "positions", "close", "groups", "members", "keep", "merge"} {
		if _, err := group.AddCommand(operation, operation+" identical content", "", &identicalCommand{runtime: rt, operation: operation}); err != nil {
			return err
		}
	}
	_, err = root.Find("files").AddCommand("remove-version", "Remove one saved-version record without deleting archive bytes", "", &removeVersionCommand{runtime: rt})
	return err
}

func (c *identicalScopeOptions) scope() (*entity.IdenticalScope, error) {
	// A Location scope is explicit; absent roots never means all Locations.
	scope := &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}
	if c.Source == "library" {
		if len(c.Roots) != 0 {
			return nil, usageError(fmt.Errorf("Library does not accept Location roots"))
		}
		return scope, nil
	}
	scope.Source = entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS
	if len(c.Roots) == 0 {
		return nil, usageError(fmt.Errorf("Locations requires at least one --root ID"))
	}
	for _, value := range c.Roots {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, usageError(fmt.Errorf("invalid Location root %q; expected ID", value))
		}
		scope.Roots = append(scope.Roots, &entity.IdenticalRoot{LocationId: id})
	}
	return scope, nil
}

func (c *identicalCommand) sortKey() entity.IdenticalSortKey {
	switch c.SortKey {
	case "name":
		return entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME
	case "size":
		return entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE
	default:
		return entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID
	}
}

func (c *identicalCommand) sortOrder() entity.IdenticalSortOrder {
	switch c.Order {
	case "asc":
		return entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC
	case "desc":
		return entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC
	default:
		if c.sortKey() == entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE {
			return entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC
		}
		return entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED
	}
}

func (c *identicalCommand) Execute(_ []string) error {
	// Resolve the operation before any live observation; a dry run mutates nothing.
	mutation := c.operation == "keep" || c.operation == "merge"
	scope, err := c.scope()
	if err != nil {
		return err
	}
	if (c.operation == "members" || mutation) && c.Group == "" {
		return usageError(fmt.Errorf("--group is required"))
	}
	if (c.operation == "groups" || c.operation == "members") && c.Cursor != "" && c.Result == "" {
		return usageError(fmt.Errorf("--result is required with --cursor"))
	}
	if (c.operation == "rows" || c.operation == "positions" || c.operation == "close") && c.Result == "" {
		return usageError(fmt.Errorf("--result is required"))
	}
	if c.operation == "positions" && len(c.FileIDs) == 0 {
		return usageError(fmt.Errorf("at least one --file-id is required"))
	}
	if mutation && c.Fingerprint == "" {
		return usageError(fmt.Errorf("--fingerprint is required"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Preserve server cursors and authoritative membership rather than assembling groups locally.
	switch c.operation {
	case "find":
		reply, err := callRPC[entity.FindIdenticalRequest, entity.FindIdenticalResponse](ctx, c.runtime, entity.FilesService_FindIdentical_FullMethodName, &entity.FindIdenticalRequest{Scope: scope})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "rows":
		reply, err := callRPC[entity.ListIdenticalRowsRequest, entity.ListIdenticalRowsResponse](ctx, c.runtime, entity.FilesService_ListIdenticalRows_FullMethodName, &entity.ListIdenticalRowsRequest{ResultId: c.Result, Offset: c.Offset, Limit: c.Limit, IncludeHidden: c.IncludeHidden, SortKey: c.sortKey(), SortOrder: c.sortOrder()})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "positions":
		reply, err := callRPC[entity.LookupIdenticalPositionsRequest, entity.LookupIdenticalPositionsResponse](ctx, c.runtime, entity.FilesService_LookupIdenticalPositions_FullMethodName, &entity.LookupIdenticalPositionsRequest{ResultId: c.Result, FileIds: c.FileIDs, SortKey: c.sortKey(), SortOrder: c.sortOrder()})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "close":
		reply, err := callRPC[entity.CloseIdenticalResultRequest, entity.CloseIdenticalResultResponse](ctx, c.runtime, entity.FilesService_CloseIdenticalResult_FullMethodName, &entity.CloseIdenticalResultRequest{ResultId: c.Result})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "groups":
		limit := c.Limit
		if limit == 0 {
			limit = 20
		}
		reply, err := callRPC[entity.ListIdenticalGroupsRequest, entity.ListIdenticalGroupsResponse](ctx, c.runtime, entity.FilesService_ListIdenticalGroups_FullMethodName, &entity.ListIdenticalGroupsRequest{Scope: scope, Cursor: c.Cursor, Limit: limit, ResultId: c.Result})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "members":
		limit := c.Limit
		if limit == 0 {
			limit = 20
		}
		reply, err := callRPC[entity.ListIdenticalMembersRequest, entity.ListIdenticalMembersResponse](ctx, c.runtime, entity.FilesService_ListIdenticalMembers_FullMethodName, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: c.Group, Cursor: c.Cursor, Limit: limit, ResultId: c.Result})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case "merge":
		if scope.Source != entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY || c.Target <= 0 {
			return usageError(fmt.Errorf("merge requires Library and --target-file"))
		}
		reply, err := callRPC[entity.MergeIdenticalRequest, entity.MergeIdenticalResponse](ctx, c.runtime, entity.FilesService_MergeIdentical_FullMethodName, &entity.MergeIdenticalRequest{Scope: scope, GroupId: c.Group, Fingerprint: c.Fingerprint, TargetFileId: c.Target, Dryrun: c.DryRun})
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	default:
		if scope.Source != entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS || c.KeepLocation <= 0 || c.KeepPath == "" {
			return usageError(fmt.Errorf("keep requires Locations, --keep-location and --keep-path"))
		}
		ref, err := locationOperationRef(ctx, c.runtime, c.KeepLocation, c.KeepPath)
		if err != nil {
			return err
		}
		return executeFileOperation(ctx, c.runtime, entity.FilesService_KeepIdentical_FullMethodName,
			&entity.KeepIdenticalRequest{Scope: scope, GroupId: c.Group, Fingerprint: c.Fingerprint, Keep: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: ref}}, Dryrun: c.DryRun},
			(*entity.KeepIdenticalResponse).GetResult)
	}
}

func (c *removeVersionCommand) Execute(_ []string) error {
	// Removing a catalog version never authorizes deleting physical copies.
	if err := positiveID("File ID", c.FileID); err != nil {
		return err
	}
	if err := positiveID("version ID", c.VersionID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.RemoveFileVersionRequest, entity.RemoveFileVersionResponse](ctx, c.runtime, entity.FilesService_RemoveVersion_FullMethodName, &entity.RemoveFileVersionRequest{FileId: c.FileID, VersionId: c.VersionID, Dryrun: c.DryRun})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
