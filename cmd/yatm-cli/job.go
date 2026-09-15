package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type jobListCommand struct {
	runtime *runtime
	jobQueryOptions
	Limit            *int64 `long:"limit" description:"Maximum results in this page"`
	BeforeID         *int64 `long:"before-id" description:"Return Jobs older than this ID"`
	Offset           *int64 `long:"offset" description:"Snapshot page offset"`
	SnapshotRevision *int64 `long:"snapshot-revision" description:"Frozen catalog revision"`
}

type jobQueryOptions struct {
	LocationID *int64 `long:"location-id" description:"Only Jobs associated with this Location"`
	MediaID    *int64 `long:"media-id" description:"Only Jobs associated with this Media"`
	Kind       string `long:"kind" description:"ARCHIVE, RESTORE, PREVIEW, SCAN, ANALYZE, or VERIFY"`
	Status     string `long:"status" description:"INDEXING, PENDING, or COMPLETED; changes include departures"`
}

type jobChangesCommand struct {
	runtime *runtime
	jobQueryOptions
	AfterRevision int64  `long:"after-revision" required:"yes" description:"Return changes after this catalog revision"`
	Limit         *int64 `long:"limit" description:"Maximum results in this page"`
}

type jobGetCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type jobProgressCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type jobWaitCommand struct {
	runtime      *runtime
	WaitTimeout  time.Duration `long:"wait-timeout" default:"10m" description:"Maximum total wait duration"`
	PollInterval time.Duration `long:"poll-interval" default:"1s" description:"Time between Job observations"`
	Args         fileIDArgs    `positional-args:"yes"`
}

type jobLogCommand struct {
	runtime *runtime
	Offset  *int64     `long:"offset" description:"Log byte offset"`
	Args    fileIDArgs `positional-args:"yes"`
}

type jobCancelCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type jobRetryIndexCommand struct {
	runtime *runtime
	Args    fileIDArgs `positional-args:"yes"`
}

type jobDeleteCommand struct {
	runtime *runtime
	Confirm bool        `long:"confirm" description:"Confirm deletion of the resolved Jobs"`
	Args    fileIDsArgs `positional-args:"yes"`
}

func registerJobCommands(root *flags.Command, commandRuntime *runtime) error {
	group, err := addGroup(root, "job", "Inspect and manage Jobs")
	if err != nil {
		return err
	}
	return addCommands(
		group,
		commandSpec{name: "list", description: "List one Job snapshot page", handler: &jobListCommand{runtime: commandRuntime}},
		commandSpec{name: "changes", description: "List Jobs changed after a revision", handler: &jobChangesCommand{runtime: commandRuntime}},
		commandSpec{name: "get", description: "Get one Job", handler: &jobGetCommand{runtime: commandRuntime}},
		commandSpec{name: "progress", description: "Get typed progress for one Job", handler: &jobProgressCommand{runtime: commandRuntime}},
		commandSpec{name: "wait", description: "Wait for completion or required intervention; never run follow-up actions", handler: &jobWaitCommand{runtime: commandRuntime}},
		commandSpec{name: "log", description: "Read one bounded Job log page", handler: &jobLogCommand{runtime: commandRuntime}},
		commandSpec{name: "cancel", description: "Cancel the current Job attempt", handler: &jobCancelCommand{runtime: commandRuntime}},
		commandSpec{name: "retry-index", description: "Retry Job manifest indexing", handler: &jobRetryIndexCommand{runtime: commandRuntime}},
		commandSpec{name: "delete", description: "Delete Jobs and their retained state", handler: &jobDeleteCommand{runtime: commandRuntime}},
	)
}

func (c *jobListCommand) Execute(_ []string) error {
	// Validate the mutually exclusive snapshot page controls before querying.
	if err := validateLimit("Job limit", c.Limit); err != nil {
		return err
	}
	if c.BeforeID != nil {
		if err := positiveID("before Job ID", *c.BeforeID); err != nil {
			return err
		}
	}
	if err := validateOffset("Job offset", c.Offset); err != nil {
		return err
	}
	if c.BeforeID != nil && c.Offset != nil {
		return usageError(fmt.Errorf("before-id and offset cannot be combined"))
	}
	if c.SnapshotRevision != nil && *c.SnapshotRevision < 0 {
		return usageError(fmt.Errorf("snapshot revision must not be negative, value=%d", *c.SnapshotRevision))
	}
	// Fetch exactly one Job catalog page with the resolved controls.
	filter, err := c.filter()
	if err != nil {
		return err
	}
	filter.Limit, filter.BeforeId, filter.Offset, filter.SnapshotRevision = c.Limit, c.BeforeID, c.Offset, c.SnapshotRevision

	// Query one snapshot page after validating every filter.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListJobsRequest, entity.ListJobsReply](
		ctx,
		c.runtime,
		entity.JobService_List_FullMethodName,
		&entity.ListJobsRequest{Filter: filter},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *jobChangesCommand) Execute(_ []string) error {
	if c.AfterRevision < 0 {
		return usageError(fmt.Errorf("after revision must not be negative, value=%d", c.AfterRevision))
	}
	if err := validateLimit("Job limit", c.Limit); err != nil {
		return err
	}
	filter, err := c.filter()
	if err != nil {
		return err
	}
	filter.ChangedAfterRevision, filter.Limit = &c.AfterRevision, c.Limit
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListJobsRequest, entity.ListJobsReply](
		ctx,
		c.runtime,
		entity.JobService_List_FullMethodName,
		&entity.ListJobsRequest{Filter: filter},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *jobQueryOptions) filter() (*entity.JobFilter, error) {
	// Snapshot and changefeed use identical resource identities and enum validation.
	if c.LocationID != nil {
		if err := positiveID("Location ID", *c.LocationID); err != nil {
			return nil, err
		}
	}
	if c.MediaID != nil {
		if err := positiveID("Media ID", *c.MediaID); err != nil {
			return nil, err
		}
	}
	filter := &entity.JobFilter{LocationId: c.LocationID, MediaId: c.MediaID}
	if c.Kind != "" {
		value, ok := entity.JobKind_value[strings.ToUpper(c.Kind)]
		if !ok || value == 0 {
			return nil, usageError(fmt.Errorf("invalid Job kind %q", c.Kind))
		}
		kind := entity.JobKind(value)
		filter.Kind = &kind
	}
	if c.Status != "" {
		value, ok := entity.JobStatus_value[strings.ToUpper(c.Status)]
		if !ok || value == 0 {
			return nil, usageError(fmt.Errorf("invalid Job status %q", c.Status))
		}
		status := entity.JobStatus(value)
		filter.Status = &status
	}
	return filter, nil
}

func (c *jobGetCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := getJob(ctx, c.runtime, c.Args.ID)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func getJob(ctx context.Context, commandRuntime *runtime, id int64) (*entity.GetJobReply, error) {
	return callRPC[entity.GetJobRequest, entity.GetJobReply](
		ctx,
		commandRuntime,
		entity.JobService_Get_FullMethodName,
		&entity.GetJobRequest{Id: id},
	)
}

func (c *jobWaitCommand) Execute(_ []string) error {
	// Bound both the complete wait and each network observation independently.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if c.WaitTimeout <= 0 || c.PollInterval < 100*time.Millisecond {
		return usageError(fmt.Errorf("wait-timeout must be positive and poll-interval at least 100ms"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(c.PollInterval)
	defer ticker.Stop()
	var last *entity.GetJobReply
	for {
		requestCtx, requestCancel := context.WithCancel(ctx)
		if c.runtime.options.Timeout > 0 {
			requestCancel()
			requestCtx, requestCancel = context.WithTimeout(ctx, c.runtime.options.Timeout)
		}
		reply, err := getJob(requestCtx, c.runtime, c.Args.ID)
		requestCancel()
		if err != nil {
			if last != nil {
				if writeErr := writeProto(c.runtime.stdout, last); writeErr != nil {
					return writeErr
				}
			}
			if ctx.Err() != nil {
				return runtimeError("deadline_exceeded", ctx.Err())
			}
			return err
		}
		if reply.Job == nil {
			return runtimeError("internal", fmt.Errorf("Job lookup returned no Job"))
		}
		last = reply

		// Emit the last observation before returning a terminal or actionable result.
		if reply.Job.Status == entity.JobStatus_COMPLETED && reply.Job.Phase == entity.JobPhase_JOB_PHASE_COMPLETED {
			return writeProto(c.runtime.stdout, reply)
		}
		switch reply.Job.Phase {
		case entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA:
			if err := writeProto(c.runtime.stdout, reply); err != nil {
				return err
			}
			return runtimeError("action_required", fmt.Errorf("Job requires intervention: %s", reply.Job.Phase))
		}
		select {
		case <-ctx.Done():
			if err := writeProto(c.runtime.stdout, reply); err != nil {
				return err
			}
			return runtimeError("deadline_exceeded", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *jobProgressCommand) Execute(_ []string) error {
	// Resolve the common Job record that selects the typed progress service.
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	job, err := getJob(ctx, c.runtime, c.Args.ID)
	if err != nil {
		return err
	}
	if job.Job == nil {
		return runtimeError("internal", fmt.Errorf("Job lookup returned no Job, id=%d", c.Args.ID))
	}

	// Dispatch the progress request once from the common Job kind.
	switch job.Job.Kind {
	case entity.JobKind_ARCHIVE:
		reply, err := callRPC[entity.GetArchiveJobProgressRequest, entity.GetArchiveJobProgressReply](
			ctx, c.runtime, entity.ArchiveJobService_GetProgress_FullMethodName,
			&entity.GetArchiveJobProgressRequest{Id: c.Args.ID},
		)
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case entity.JobKind_RESTORE:
		reply, err := callRPC[entity.GetRestoreJobProgressRequest, entity.GetRestoreJobProgressReply](
			ctx, c.runtime, entity.RestoreJobService_GetProgress_FullMethodName,
			&entity.GetRestoreJobProgressRequest{Id: c.Args.ID},
		)
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	case entity.JobKind_SCAN:
		reply, err := callRPC[entity.GetScanJobProgressRequest, entity.GetScanJobProgressReply](
			ctx, c.runtime, entity.ScanJobService_GetProgress_FullMethodName,
			&entity.GetScanJobProgressRequest{Id: c.Args.ID},
		)
		if err != nil {
			return err
		}
		return writeProto(c.runtime.stdout, reply)
	default:
		return runtimeError("internal", fmt.Errorf("unsupported Job kind, kind=%s", job.Job.Kind))
	}
}

func (c *jobLogCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateOffset("log offset", c.Offset); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetJobLogRequest, entity.GetJobLogReply](
		ctx,
		c.runtime,
		entity.JobService_GetLog_FullMethodName,
		&entity.GetJobLogRequest{Id: c.Args.ID, Offset: c.Offset},
	)
	if err != nil {
		return err
	}
	return writeJSON(c.runtime.stdout, jobLogOutput{Logs: string(reply.Logs), Offset: reply.Offset})
}

func (c *jobCancelCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CancelJobRequest, entity.CancelJobReply](
		ctx,
		c.runtime,
		entity.JobService_Cancel_FullMethodName,
		&entity.CancelJobRequest{Id: c.Args.ID},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *jobRetryIndexCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.RetryJobIndexRequest, entity.RetryJobIndexReply](
		ctx,
		c.runtime,
		entity.JobService_RetryIndex_FullMethodName,
		&entity.RetryJobIndexRequest{Id: c.Args.ID},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

func (c *jobDeleteCommand) Execute(_ []string) error {
	// Require confirmation and a valid target set before any remote reads.
	if !c.Confirm {
		return safetyError(fmt.Errorf("job delete requires --confirm"))
	}
	ids, err := positiveIDs("Job ID", c.Args.IDs)
	if err != nil {
		return err
	}
	ctx, cancel := c.runtime.context()
	defer cancel()

	// Resolve every retained Job immediately before deleting its state.
	for _, id := range ids {
		reply, err := getJob(ctx, c.runtime, id)
		if err != nil {
			return err
		}
		if reply.Job == nil {
			return runtimeError("not_found", fmt.Errorf("Job does not exist, id=%d", id))
		}
	}

	// Delete the exact validated identifier set once.
	reply, err := callRPC[entity.DeleteJobsRequest, entity.DeleteJobsReply](
		ctx,
		c.runtime,
		entity.JobService_Delete_FullMethodName,
		&entity.DeleteJobsRequest{Ids: ids},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
