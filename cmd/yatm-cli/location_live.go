package main

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type liveEntryCommand struct {
	runtime *runtime
	Path    string     `long:"path" description:"Location-relative path; empty for root"`
	Args    fileIDArgs `positional-args:"yes"`
}

type liveAdmitCommand struct {
	runtime *runtime
	Path    string     `long:"path" required:"yes" description:"Ordinary file path relative to the Location"`
	Args    fileIDArgs `positional-args:"yes"`
}

type locateOriginalCommand struct {
	runtime    *runtime
	LocationID int64      `long:"location-id" required:"yes" description:"Location containing the original"`
	Path       string     `long:"path" required:"yes" description:"Ordinary file path relative to the Location"`
	Confirm    bool       `long:"confirm" description:"Confirm replacing the current original association without moving bytes"`
	Args       fileIDArgs `positional-args:"yes"`
}

func (c *locateOriginalCommand) Execute(_ []string) error {
	// Freeze both sides of the explicit relink; existing target ownership remains a server conflict.
	if err := positiveID("File ID", c.Args.ID); err != nil {
		return err
	}
	if !c.Confirm {
		return safetyError(fmt.Errorf("locate-original requires --confirm; no physical files are moved"))
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	state, err := callRPC[entity.GetFileStateRequest, entity.FileStateReply](ctx, c.runtime, entity.FileCatalogService_GetState_FullMethodName, &entity.GetFileStateRequest{FileId: c.Args.ID})
	if err != nil {
		return err
	}
	entry, err := requestLiveEntry(ctx, c.runtime, c.LocationID, c.Path)
	if err != nil {
		return err
	}
	reply, err := callRPC[entity.RelocateOriginalRequest, entity.FileStateReply](ctx, c.runtime, entity.FileCatalogService_RelocateOriginal_FullMethodName,
		&entity.RelocateOriginalRequest{FileId: c.Args.ID, Reference: entry.Reference, ExpectedOriginal: state.Original})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func requestLiveEntry(ctx context.Context, rt *runtime, id int64, path string) (*entity.LocationEntry, error) {
	// Resolve actual object facts through the public API, never synthesize an expected identity.
	if err := positiveID("Location ID", id); err != nil {
		return nil, err
	}
	return callRPC[entity.GetLocationEntryRequest, entity.LocationEntry](ctx, rt, entity.LocationService_GetEntry_FullMethodName,
		&entity.GetLocationEntryRequest{LocationId: id, Path: path})
}

func (c *liveEntryCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	entry, err := requestLiveEntry(ctx, c.runtime, c.Args.ID, c.Path)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, entry)
}

func (c *liveAdmitCommand) Execute(_ []string) error {
	// Admission revalidates the inspected object and returns a real File association.
	ctx, cancel := c.runtime.context()
	defer cancel()
	entry, err := requestLiveEntry(ctx, c.runtime, c.Args.ID, c.Path)
	if err != nil {
		return err
	}
	result, err := callRPC[entity.LocationEntryRef, entity.LocationEntry](ctx, c.runtime, entity.LocationService_Admit_FullMethodName, entry.Reference)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, result)
}
