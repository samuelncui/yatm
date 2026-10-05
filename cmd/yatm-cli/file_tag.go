package main

import (
	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

const trashFileID int64 = -1

type fileIDArgs struct {
	ID int64 `positional-arg-name:"ID" required:"yes"`
}
type fileIDsArgs struct {
	IDs []int64 `positional-arg-name:"ID" required:"yes"`
}

// positionIDsArgs lets a Media-scoped command omit explicit Position IDs.
type positionIDsArgs struct {
	IDs []int64 `positional-arg-name:"ID"`
}

type tagListCommand struct {
	runtime *runtime
	Prefix  *string `long:"prefix" description:"Tag prefix"`
	Limit   *int64  `long:"limit" description:"Maximum results in this page"`
	Cursor  *string `long:"cursor" description:"Page cursor"`
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

func (c *tagListCommand) Execute(_ []string) error {
	if err := validateLimit("tag limit", c.Limit); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListTagsRequest, entity.ListTagsResponse](
		ctx,
		c.runtime,
		entity.LibraryService_ListTags_FullMethodName,
		&entity.ListTagsRequest{Prefix: c.Prefix, Limit: c.Limit, Cursor: c.Cursor},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
