package main

import (
	"fmt"
	"strings"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/entity"
)

type calibrationCreateCommand struct {
	runtime       *runtime
	Device        string `long:"device" required:"yes" description:"Configured server-side Tape device holding the cartridge"`
	WriteTestData bool   `long:"write-test-data" description:"OVERWRITE the loaded cartridge from the beginning of tape with test data; never use it on a cartridge holding data. Use the cartridge again only through FORMAT"`
}

func registerCalibrationCommands(root *flags.Command, rt *runtime) error {
	group, err := addGroup(root, "calibration", "Measure a Tape drive's positioning times into its read parameters")
	if err != nil {
		return err
	}
	return addCommands(group,
		commandSpec{name: "create", description: "Create and start a Calibration on the loaded cartridge: the data it holds, or written test data", handler: &calibrationCreateCommand{runtime: rt}},
	)
}

func (c *calibrationCreateCommand) Execute(_ []string) error {
	// Reject malformed targets before the Job starts moving the tape.
	device := strings.TrimSpace(c.Device)
	if device == "" {
		return usageError(fmt.Errorf("device must not be empty"))
	}
	// Creation runs the Job immediately; job wait and job progress follow it.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.CreateCalibrationJobRequest, entity.CreateCalibrationJobResponse](ctx, c.runtime,
		entity.CalibrationJobService_Create_FullMethodName,
		&entity.CreateCalibrationJobRequest{Spec: &entity.CalibrationJobSpec{Device: device, WriteTestData: c.WriteTestData}})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}
