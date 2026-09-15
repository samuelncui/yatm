package main

import (
	flags "github.com/jessevdk/go-flags"
)

type statusCommand struct {
	runtime *runtime
}

func registerStatusCommand(root *flags.Command, commandRuntime *runtime) error {
	return addCommands(root, commandSpec{
		name: "status", description: "Check HTTP and gRPC-Web service health",
		handler: &statusCommand{runtime: commandRuntime},
	})
}

func (c *statusCommand) Execute(_ []string) error {
	ctx, cancel := c.runtime.context()
	defer cancel()
	if err := c.runtime.checkStatus(ctx); err != nil {
		return err
	}
	return writeJSON(c.runtime.stdout, statusOutput{HTTP: true, GRPCWeb: true})
}
