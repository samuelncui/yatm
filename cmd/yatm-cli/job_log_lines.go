package main

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type jobLogLinesCommand struct {
	runtime   *runtime
	Direction string     `long:"direction" choice:"older" choice:"newer" default:"older" description:"Read toward older or newer log lines"`
	Cursor    *int64     `long:"cursor" description:"Byte cursor from a prior log-lines response"`
	Level     string     `long:"level" description:"Exact log level: trace, debug, info, warning, error, fatal, or panic"`
	Query     string     `long:"query" description:"Case-insensitive text contained in the raw line"`
	Args      fileIDArgs `positional-args:"yes"`
}

func (c *jobLogLinesCommand) Execute(_ []string) error {
	if err := positiveID("Job ID", c.Args.ID); err != nil {
		return err
	}
	if err := validateOffset("log cursor", c.Cursor); err != nil {
		return err
	}
	direction := entity.JobLogDirection_JOB_LOG_DIRECTION_OLDER
	if c.Direction == "newer" {
		direction = entity.JobLogDirection_JOB_LOG_DIRECTION_NEWER
	} else if c.Direction != "older" {
		return usageError(fmt.Errorf("invalid log direction %q", c.Direction))
	}

	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.ListJobLogLinesRequest, entity.ListJobLogLinesResponse](
		ctx,
		c.runtime,
		entity.JobService_ListLogLines_FullMethodName,
		&entity.ListJobLogLinesRequest{Id: c.Args.ID, Direction: direction, Cursor: c.Cursor, Level: c.Level, Query: c.Query},
	)
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
