package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/samuelncui/yatm/entity"
)

type filesMeasureCommand struct {
	runtime *runtime
	entryOptions
	scopeOptions
	Query     string `long:"query" description:"Match roots using the shared Files query; matching folders include their contents"`
	Recursive bool   `long:"recursive" description:"Find matching roots throughout the Library subtree"`
}

func (c *filesMeasureCommand) Execute(_ []string) error {
	// Use the same explicit source defaults as ls, without fetching a detail or list first.
	if c.FileID == nil && c.LocationID == nil && c.Path == "" {
		root := int64(0)
		c.FileID = &root
	}
	ref, err := c.reference()
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	client := connect.NewClient[entity.MeasureFilesRequest, entity.MeasureFilesResponse](c.runtime.httpClient,
		c.runtime.rpcURL(entity.FilesService_Measure_FullMethodName), connect.WithGRPCWeb())
	stream, err := client.CallServerStream(ctx, connect.NewRequest(&entity.MeasureFilesRequest{
		Directory: ref, Scope: c.fileScope(), Query: c.Query, Recursive: c.Recursive,
	}))
	if err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}
	defer stream.Close()

	// Emit bounded NDJSON outcomes as they arrive, preserving partial-result evidence on failure.
	var summary *entity.FilesMeasurement
	for stream.Receive() {
		update := stream.Msg()
		if err := writeProto(c.runtime.stdout, update); err != nil {
			return err
		}
		if update.GetSummary() != nil {
			summary = update.GetSummary()
		}
	}
	if err := stream.Err(); err != nil {
		return runtimeError(connect.CodeOf(err).String(), err)
	}
	if summary == nil || !summary.Complete {
		return runtimeError("incomplete", fmt.Errorf("size measurement incomplete: %s", summary.GetError()))
	}
	return nil
}
