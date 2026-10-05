package main

import (
	"fmt"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

type jobCreationCommand struct {
	runtime *runtime
	kind    entity.JobKind
	Args    fileIDArgs `positional-args:"yes"`
}

func registerJobCreationCommands(root *flags.Command, commandRuntime *runtime) error {
	// Register one read for each existing Job domain, using its kind directly without another Job lookup.
	for _, domain := range []struct {
		name string
		kind entity.JobKind
	}{
		{name: "archive", kind: entity.JobKind_JOB_KIND_ARCHIVE},
		{name: "restore", kind: entity.JobKind_JOB_KIND_RESTORE},
		{name: "scan", kind: entity.JobKind_JOB_KIND_SCAN},
	} {
		if err := addCommands(root.Find(domain.name), commandSpec{
			name: "creation", description: "Read retained creation inputs for review without creating a Job",
			handler: &jobCreationCommand{runtime: commandRuntime, kind: domain.kind},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *jobCreationCommand) Execute(_ []string) error {
	// Reject invalid IDs before making the single typed read under the invocation's deadline.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Return the complete typed envelope, including any explanation of incomplete original inputs.
	var reply proto.Message
	var err error
	switch c.kind {
	case entity.JobKind_JOB_KIND_ARCHIVE:
		reply, err = callRPC[entity.GetArchiveJobCreationRequest, entity.GetArchiveJobCreationResponse](
			ctx, c.runtime, entity.ArchiveJobService_GetCreation_FullMethodName,
			&entity.GetArchiveJobCreationRequest{Id: c.Args.ID},
		)
	case entity.JobKind_JOB_KIND_RESTORE:
		reply, err = callRPC[entity.GetRestoreJobCreationRequest, entity.GetRestoreJobCreationResponse](
			ctx, c.runtime, entity.RestoreJobService_GetCreation_FullMethodName,
			&entity.GetRestoreJobCreationRequest{Id: c.Args.ID},
		)
	case entity.JobKind_JOB_KIND_SCAN:
		reply, err = callRPC[entity.GetScanJobCreationRequest, entity.GetScanJobCreationResponse](
			ctx, c.runtime, entity.ScanJobService_GetCreation_FullMethodName,
			&entity.GetScanJobCreationRequest{Id: c.Args.ID},
		)
	default:
		return runtimeError("internal", fmt.Errorf("unsupported Job kind, kind=%s", c.kind))
	}
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
