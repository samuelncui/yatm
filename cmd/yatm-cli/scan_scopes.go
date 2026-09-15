package main

import (
	"fmt"
	"github.com/samuelncui/yatm/entity"
)

type scanScopesCommand struct {
	runtime *runtime
	Limit   int32      `long:"limit" default:"100" description:"Maximum scope results"`
	AfterID *int64     `long:"after-id" description:"Scope result cursor"`
	Args    fileIDArgs `positional-args:"yes"`
}

func (c *scanScopesCommand) Execute(_ []string) error {
	// Scope pagination does not access storage or trigger work.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if c.Limit <= 0 || c.Limit > 1000 {
		return usageError(fmt.Errorf("limit must be between 1 and 1000"))
	}
	if c.AfterID != nil && *c.AfterID < 0 {
		return usageError(fmt.Errorf("after-id must not be negative"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListScanJobScopesRequest, entity.ListScanJobScopesReply](ctx, c.runtime, entity.ScanJobService_ListScopes_FullMethodName, &entity.ListScanJobScopesRequest{Id: c.Args.ID, Limit: c.Limit, AfterId: c.AfterID})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
