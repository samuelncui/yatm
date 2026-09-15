package main

import (
	"encoding/hex"
	"fmt"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type fileStateCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}
type fileVersionCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}
type fileVersionsCommand struct {
	runtime *runtime
	AfterID int64      `long:"after-id" description:"FileVersion ID cursor"`
	Limit   int32      `long:"limit" default:"100" description:"Maximum page size"`
	Args    fileIDArgs `positional-args:"yes"`
}
type contentPageOptions struct {
	Signature string `long:"signature" required:"yes" description:"Opaque content signature, hexadecimal"`
	AfterID   int64  `long:"after-id" description:"Result ID cursor"`
	Limit     int32  `long:"limit" default:"100" description:"Maximum page size"`
}
type contentCopiesCommand struct {
	runtime *runtime
	contentPageOptions
}
type contentDuplicatesCommand struct {
	runtime *runtime
	contentPageOptions
}
type importPositionsCommand struct {
	runtime *runtime
	Args    fileIDsArgs `positional-args:"yes"`
}

type inspectSelectionCommand struct {
	runtime *runtime
	selectionOptions
	restorePolicyOptions
	VersionIDs         []int64 `long:"version-id" description:"Explicit saved version; repeatable"`
	Restore            bool    `long:"restore" description:"Inspect a Restore selection rather than current originals"`
	TargetLocation     int64   `long:"target-location" description:"Optional Restore destination for Ignore warnings"`
	Directory          string  `long:"directory" description:"Restore destination subdirectory"`
	AllowDamagedCopies bool    `long:"allow-damaged-copies" description:"Include damaged copies in Restore availability estimates"`
}

func registerCatalogCommands(root *flags.Command, rt *runtime) error {
	// Attach content queries to the existing File command tree.
	group := root.Find("file")
	if group == nil {
		return fmt.Errorf("File commands must be registered before the content catalog")
	}
	return addCommands(group,
		commandSpec{name: "state", description: "Inspect the original and current archive coverage", handler: &fileStateCommand{runtime: rt}},
		commandSpec{name: "locate-original", description: "Explicitly associate a live path with an existing File", handler: &locateOriginalCommand{runtime: rt}},
		commandSpec{name: "versions", description: "List one File's archived versions", handler: &fileVersionsCommand{runtime: rt}},
		commandSpec{name: "version", description: "Get a FileVersion by version ID", handler: &fileVersionCommand{runtime: rt}},
		commandSpec{name: "copies", description: "List copies of exact opaque content", handler: &contentCopiesCommand{runtime: rt}},
		commandSpec{name: "duplicates", description: "Find independent Files with matching content", handler: &contentDuplicatesCommand{runtime: rt}},
		commandSpec{name: "duplicate-groups", description: "Search grouped duplicate originals", handler: &duplicateGroupsCommand{runtime: rt}},
		commandSpec{name: "duplicate-members", description: "Page every original in one content group", handler: &duplicateMembersCommand{runtime: rt}},
		commandSpec{name: "import-positions", description: "Add selected archive inventory as independent Library Files", handler: &importPositionsCommand{runtime: rt}},
		commandSpec{name: "inspect-selection", description: "Resolve a selection's scope, count and missing content before creating a Job", handler: &inspectSelectionCommand{runtime: rt}},
	)
}

func (c *inspectSelectionCommand) Execute(_ []string) error {
	// Preserve selection roots and validate explicit versions before contacting the server.
	selections, err := c.selections()
	if err != nil {
		return err
	}
	if len(c.VersionIDs) > 0 && !c.Restore {
		return usageError(fmt.Errorf("version-id requires restore"))
	}
	for _, id := range c.VersionIDs {
		if err := positiveID("FileVersion ID", id); err != nil {
			return err
		}
	}
	if len(selections)+len(c.VersionIDs) == 0 {
		return usageError(fmt.Errorf("at least one selection or version is required"))
	}
	if c.TargetLocation < 0 {
		return usageError(fmt.Errorf("target-location must be positive"))
	}
	if !c.Restore && (c.TargetLocation != 0 || c.Directory != "" || c.AllowDamagedCopies) {
		return usageError(fmt.Errorf("destination and damaged copies options require restore"))
	}
	if c.Directory != "" && c.TargetLocation == 0 {
		return usageError(fmt.Errorf("directory requires target-location"))
	}
	policy, err := c.policy(c.Restore)
	if err != nil {
		return err
	}
	request := &entity.InspectSelectionRequest{Selections: selections, FileVersionIds: c.VersionIDs, Restore: c.Restore,
		AllowDamagedCopies: c.AllowDamagedCopies, VersionPolicy: policy, SkipUnmatchedVersions: c.SkipUnmatchedVersions}
	if c.TargetLocation != 0 {
		request.Destination = &entity.RestoreDestination{LocationId: c.TargetLocation, Path: c.Directory}
	}

	// Inspect with the same bounded expansion and scope rules used by Job creation.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.InspectSelectionRequest, entity.InspectSelectionReply](ctx, c.runtime,
		entity.FileCatalogService_InspectSelection_FullMethodName,
		request)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *fileStateCommand) Execute(_ []string) error {
	if err := positiveID("File ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetFileStateRequest, entity.FileStateReply](ctx, c.runtime, entity.FileCatalogService_GetState_FullMethodName, &entity.GetFileStateRequest{FileId: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
func (c *fileVersionCommand) Execute(_ []string) error {
	if err := positiveID("FileVersion ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetFileVersionRequest, entity.FileVersionReply](ctx, c.runtime, entity.FileCatalogService_GetVersion_FullMethodName, &entity.GetFileVersionRequest{Id: c.Args.ID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
func (c *fileVersionsCommand) Execute(_ []string) error {
	if err := positiveID("File ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListFileVersionsRequest, entity.ListFileVersionsReply](ctx, c.runtime, entity.FileCatalogService_ListVersions_FullMethodName, &entity.ListFileVersionsRequest{FileId: c.Args.ID, AfterId: c.AfterID, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
func (c *contentPageOptions) signature() ([]byte, error) {
	value, err := hex.DecodeString(c.Signature)
	if err != nil || len(value) == 0 {
		return nil, usageError(fmt.Errorf("signature must be nonempty hexadecimal"))
	}
	return value, nil
}
func (c *contentCopiesCommand) Execute(_ []string) error {
	signature, err := c.signature()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListContentCopiesRequest, entity.ListContentCopiesReply](ctx, c.runtime, entity.FileCatalogService_ListCopies_FullMethodName, &entity.ListContentCopiesRequest{Signature: signature, AfterId: c.AfterID, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
func (c *contentDuplicatesCommand) Execute(_ []string) error {
	signature, err := c.signature()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListContentDuplicatesRequest, entity.ListContentDuplicatesReply](ctx, c.runtime, entity.FileCatalogService_ListDuplicates_FullMethodName, &entity.ListContentDuplicatesRequest{Signature: signature, AfterFileId: c.AfterID, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
func (c *importPositionsCommand) Execute(_ []string) error {
	ids, err := positiveIDs("Position ID", c.Args.IDs)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ImportArchivePositionsRequest, entity.ImportArchivePositionsReply](ctx, c.runtime, entity.FileCatalogService_ImportPositions_FullMethodName, &entity.ImportArchivePositionsRequest{PositionIds: ids})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
