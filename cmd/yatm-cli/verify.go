package main

import (
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media"
)

type verifyCreateCommand struct {
	runtime  *runtime
	Priority int64      `long:"priority" default:"0" description:"Job priority"`
	Args     fileIDArgs `positional-args:"yes"`
}

type verifyEntriesCommand struct {
	runtime *runtime
	jobResultPageOptions
	Args fileIDArgs `positional-args:"yes"`
}

type verifyRunCommand struct {
	runtime *runtime
	UUID    string     `long:"uuid" description:"Mounted Volume UUID; mutually exclusive with device"`
	Device  string     `long:"device" description:"Tape device; mutually exclusive with uuid"`
	Args    fileIDArgs `positional-args:"yes"`
}

func registerVerifyCommands(root *flags.Command, rt *runtime) error {
	// This preset uses the common Scan pipeline with immutable recorded-copy baselines.
	group, err := addGroup(root, "verify", "Check archived Media content against recorded facts")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "create", description: "Prepare a complete Media integrity manifest by Media ID", handler: &verifyCreateCommand{runtime: rt}},
		commandSpec{name: "run", description: "Read the selected Media without repairing or rewriting it", handler: &verifyRunCommand{runtime: rt}},
		commandSpec{name: "entries", description: "List one page of integrity findings", handler: &verifyEntriesCommand{runtime: rt}},
	)
}

func (c *verifyCreateCommand) Execute(_ []string) error {
	// Reject malformed identities before creating a durable operation.
	if err := positiveID("Media ID", c.Args.ID); err != nil {
		return err
	}
	if c.Priority < 0 {
		return usageError(fmt.Errorf("Job priority must not be negative"))
	}

	// Creation freezes inventory; selecting and reading the physical Media is explicit.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateScanJobRequest, entity.CreateScanJobResponse](ctx, c.runtime,
		entity.ScanJobService_Create_FullMethodName,
		&entity.CreateScanJobRequest{Priority: c.Priority, Spec: &entity.ScanJobSpec{MediaId: c.Args.ID, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *verifyEntriesCommand) Execute(_ []string) error {
	// Result pagination is bounded and never starts device access.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := c.jobResultPageOptions.validate(); err != nil {
		return err
	}

	// Fetch one typed page; generic job progress/wait handles the shared lifecycle.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListScanJobEntriesRequest, entity.ListScanJobEntriesResponse](ctx, c.runtime,
		entity.ScanJobService_ListEntries_FullMethodName,
		&entity.ListScanJobEntriesRequest{
			Id: c.Args.ID, Limit: c.pageLimit(), Cursor: c.Cursor, Order: c.order(), Offset: c.Offset, IncludeTotal: c.IncludeTotal,
		})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *verifyRunCommand) Execute(_ []string) error {
	// A physical target is mandatory and mutually exclusive; no automatic device selection occurs.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if (c.UUID == "") == (strings.TrimSpace(c.Device) == "") {
		return usageError(fmt.Errorf("provide exactly one of --uuid and --device"))
	}
	var target *entity.ReadMediaTarget
	if c.UUID != "" {
		uuid, err := media.NormalizeVolumeUUID(c.UUID)
		if err != nil {
			return usageError(err)
		}
		target = (&entity.ReadVolumeTarget{Uuid: uuid}).Pack()
	} else {
		target = (&entity.ReadTapeTarget{Device: strings.TrimSpace(c.Device)}).Pack()
	}

	// The server supplies the immutable expected Media identity, not caller-controlled expectations.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ReadScanMediaRequest, entity.ReadScanMediaResponse](ctx, c.runtime,
		entity.ScanJobService_ReadMedia_FullMethodName, &entity.ReadScanMediaRequest{Id: c.Args.ID, Target: target})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
