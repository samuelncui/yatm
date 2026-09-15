// Command yatm-cli exposes YATM's online workflows as an agent-friendly CLI.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	flags "github.com/jessevdk/go-flags"
	"github.com/samuelncui/yatm/internal/buildinfo"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

type options struct {
	Version           bool          `long:"version" description:"Print local program version and commit (used alone)"`
	Server            string        `long:"server" env:"YATM_SERVER" default:"http://127.0.0.1:8080" description:"YATM server URL"`
	Timeout           time.Duration `long:"timeout" env:"YATM_TIMEOUT" default:"30s" description:"Request timeout; use 0 for no deadline"`
	BasicUser         string        `long:"basic-user" env:"YATM_BASIC_USER" description:"HTTP Basic Auth user"`
	BasicPasswordFile string        `long:"basic-password-file" env:"YATM_BASIC_PASSWORD_FILE" value-name:"FILE" description:"File containing the HTTP Basic Auth password"`
}

type runtime struct {
	options *options
	stdin   io.Reader
	stdout  io.Writer

	baseURL    string
	httpClient *http.Client
	prepared   bool
}

type commandError struct {
	code string
	exit int
	err  error
}

func (e *commandError) Error() string {
	return e.err.Error()
}

func (e *commandError) Unwrap() error {
	return e.err
}

type errorOutput struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Version inspection must not load connection settings or contact a server.
	if buildinfo.IsVersion(args) {
		if err := buildinfo.Write(stdout, "yatm-cli"); err != nil {
			writeCommandError(stderr, runtimeError("output", err))
			return exitFailure
		}
		return exitSuccess
	}

	// Build the complete command tree around this invocation's isolated runtime.
	opts := new(options)
	commandRuntime := &runtime{options: opts, stdin: stdin, stdout: stdout}
	parser, err := newParser(opts, commandRuntime)
	if err != nil {
		writeCommandError(stderr, runtimeError("internal", err))
		return exitFailure
	}

	// Parse and execute one leaf command, preserving ordinary help as successful text output.
	if _, err := parser.ParseArgs(args); err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			_, _ = io.WriteString(stdout, flagsErr.Message)
			if !strings.HasSuffix(flagsErr.Message, "\n") {
				_, _ = io.WriteString(stdout, "\n")
			}
			return exitSuccess
		}
		if errors.As(err, &flagsErr) {
			err = usageError(fmt.Errorf("parse command failed, %w", err))
		}
		writeCommandError(stderr, err)
		var commandErr *commandError
		if errors.As(err, &commandErr) {
			return commandErr.exit
		}
		return exitFailure
	}
	return exitSuccess
}

func newParser(opts *options, commandRuntime *runtime) (*flags.Parser, error) {
	// Register global transport options before adding the business command tree.
	parser := flags.NewNamedParser("yatm-cli", flags.HelpFlag|flags.PassDoubleDash)
	parser.ShortDescription = "Operate a YATM server"
	if _, err := parser.AddGroup("Connection options", "", opts); err != nil {
		return nil, fmt.Errorf("register connection options failed, %w", err)
	}

	// Prepare the concrete transport after flags and environment defaults have been resolved.
	parser.CommandHandler = func(command flags.Commander, args []string) error {
		if opts.Version {
			return usageError(fmt.Errorf("use --version without a command"))
		}
		if command == nil {
			return usageError(fmt.Errorf("command is required"))
		}
		if len(args) > 0 {
			return usageError(fmt.Errorf("unexpected arguments: %s", strings.Join(args, " ")))
		}
		if err := commandRuntime.prepare(); err != nil {
			return err
		}
		return command.Execute(args)
	}

	// Populate every business leaf after installing the shared execution boundary.
	if err := registerCommands(parser.Command, commandRuntime); err != nil {
		return nil, err
	}
	return parser, nil
}

func registerCommands(root *flags.Command, commandRuntime *runtime) error {
	// Register each public business area under one stable root command.
	registrations := []func(*flags.Command, *runtime) error{
		registerStatusCommand,
		registerFileCommands,
		registerFilesCommands,
		registerTagCommands,
		registerMediaCommands,
		registerVolumeCommands,
		registerTapeCommands,
		registerJobCommands,
		registerArchiveCommands,
		registerRestoreCommands,
		registerPreviewCommands,
		registerScanCommands,
		registerVerifyCommands,
		registerFileOperationCommands,
		registerOnlineCommands,
		registerCatalogCommands,
		registerAnalyzeCommands,
		registerLibraryCommands,
		registerSettingsCommands,
	}
	for _, register := range registrations {
		if err := register(root, commandRuntime); err != nil {
			return err
		}
	}
	return nil
}

func usageError(err error) error {
	return &commandError{code: "usage", exit: exitUsage, err: err}
}

func safetyError(err error) error {
	return &commandError{code: "safety", exit: exitUsage, err: err}
}

func runtimeError(code string, err error) error {
	return &commandError{code: code, exit: exitFailure, err: err}
}

func writeCommandError(output io.Writer, err error) {
	// Preserve a stable machine-readable shape for parser, transport, and command failures.
	code := "internal"
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		code = commandErr.code
	}
	_ = json.NewEncoder(output).Encode(errorOutput{Error: err.Error(), Code: code})
}
