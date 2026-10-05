package main

import (
	"github.com/samuelncui/yatm/entity"
)

type locateOriginalCommand struct {
	runtime    *runtime
	LocationID int64      `long:"location-id" required:"yes" description:"Location containing the original"`
	Path       string     `long:"path" required:"yes" description:"Ordinary file path relative to the Location"`
	DryRun     bool       `long:"dryrun" description:"Report the association this would replace without replacing it"`
	Args       fileIDArgs `positional-args:"yes"`
}

func (c *locateOriginalCommand) Execute(_ []string) error {
	// Resolve the chosen target; existing target ownership remains a server conflict.
	if err := positiveID("File ID", c.Args.ID); err != nil {
		return err
	}
	if err := positiveID("Location ID", c.LocationID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	entry, err := locationOperationRef(ctx, c.runtime, c.LocationID, c.Path)
	if err != nil {
		return err
	}
	reply, err := callRPC[entity.RelocateOriginalRequest, entity.RelocateOriginalResponse](ctx, c.runtime, entity.FilesService_RelocateOriginal_FullMethodName,
		&entity.RelocateOriginalRequest{FileId: c.Args.ID, Reference: entry, Dryrun: c.DryRun})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply.GetDetail())
}
