package main

import (
	"fmt"

	flags "github.com/jessevdk/go-flags"
)

type commandGroup struct{}

type commandSpec struct {
	name        string
	description string
	handler     any
}

func addGroup(parent *flags.Command, name, description string) (*flags.Command, error) {
	command, err := parent.AddCommand(name, description, "", &commandGroup{})
	if err != nil {
		return nil, fmt.Errorf("register %s command group failed, %w", name, err)
	}
	return command, nil
}

func addCommands(parent *flags.Command, specs ...commandSpec) error {
	for _, spec := range specs {
		if _, err := parent.AddCommand(spec.name, spec.description, "", spec.handler); err != nil {
			return fmt.Errorf("register %s command failed, %w", spec.name, err)
		}
	}
	return nil
}
