package main

import (
	"encoding/hex"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type duplicatePageOptions struct {
	Query  string `long:"query" description:"Library query selecting matching groups"`
	Cursor string `long:"cursor" description:"Opaque page cursor"`
	Limit  int32  `long:"limit" default:"20" description:"Maximum page size (1-500)"`
}

type duplicateGroupsCommand struct {
	runtime *runtime
	duplicatePageOptions
}

type duplicateMembersCommand struct {
	runtime *runtime
	duplicatePageOptions
	Signature string `long:"signature" required:"yes" description:"Exact opaque group signature, hexadecimal"`
}

func (c *duplicateGroupsCommand) Execute(_ []string) error {
	// Preserve the server's query-bound cursor and independent group pagination.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListDuplicateGroupsRequest, entity.ListDuplicateGroupsReply](
		ctx, c.runtime, entity.FileCatalogService_ListDuplicateGroups_FullMethodName,
		&entity.ListDuplicateGroupsRequest{Query: c.Query, Cursor: c.Cursor, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *duplicateMembersCommand) Execute(_ []string) error {
	// Decode only the CLI transport representation, without imposing a signature content format.
	signature, err := hex.DecodeString(c.Signature)
	if err != nil || len(signature) == 0 {
		return usageError(fmt.Errorf("signature must be nonempty hexadecimal"))
	}

	// Members retain their full group even when only one File matches the query.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListDuplicateMembersRequest, entity.ListDuplicateMembersReply](
		ctx, c.runtime, entity.FileCatalogService_ListDuplicateMembers_FullMethodName,
		&entity.ListDuplicateMembersRequest{Signature: signature, Query: c.Query, Cursor: c.Cursor, Limit: c.Limit})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
