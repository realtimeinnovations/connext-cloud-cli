// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/doctor"
	"github.com/spf13/cobra"
)

type executionError struct {
	Detail *clierror.Error
	JSON   bool
}

func (err *executionError) Error() string { return err.Detail.Error() }
func (err *executionError) Unwrap() error { return err.Detail }

// ReportError writes the CLI error to stderr and returns its stable exit code.
// Execute itself only returns errors, so library users can inspect them.
func ReportError(err error, errOut io.Writer) int {
	if err == nil {
		return 0
	}
	var completed *doctor.CompletedError
	if errors.As(err, &completed) {
		return completed.ExitCode
	}
	typed := clierror.From(err)
	var execution *executionError
	if errors.As(err, &execution) && execution.JSON {
		_ = json.NewEncoder(errOut).Encode(struct {
			SchemaVersion string          `json:"schema_version"`
			Error         *clierror.Error `json:"error"`
		}{SchemaVersion: "1", Error: typed})
	} else {
		_, _ = fmt.Fprintln(errOut, typed.Message)
	}
	return typed.ExitCode()
}

// Detect output intent even when Cobra fails before RunE (unknown arguments,
// missing required flags). Stop at -- so positional data cannot select a mode.
func requestsJSON(argv []string) bool {
	format := ""
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--format=") {
			format = strings.TrimPrefix(arg, "--format=")
		} else if arg == "--format" && i+1 < len(argv) {
			i++
			format = argv[i]
		}
	}
	return format == "json"
}

func executionFailure(err error, jsonOutput, invoked bool) error {
	var completed *doctor.CompletedError
	if errors.As(err, &completed) {
		return completed
	}
	typed := clierror.From(err)
	if errors.Is(err, config.ErrNotConfigured) {
		typed = &clierror.Error{Code: clierror.CodeConfigRequired, Message: err.Error(), RequiredAction: "configure_region", Cause: err}
	} else if !invoked && typed.Code == clierror.CodeCommandFailed {
		typed = &clierror.Error{Code: clierror.CodeInvalidArgument, Message: err.Error(), Cause: err}
	}
	return &executionError{Detail: typed, JSON: jsonOutput}
}

func nonInteractive(cmd *cobra.Command) bool {
	value, _ := cmd.Flags().GetBool("non-interactive")
	return value
}

func supportsJSON(cmd *cobra.Command) bool {
	path := strings.Fields(cmd.CommandPath())
	if len(path) == 2 && path[1] == "doctor" {
		return true
	}
	if len(path) == 3 {
		switch path[1] {
		case "databus":
			return path[2] != "app" && !cmd.HasAvailableSubCommands()
		case "observability", "network", "license":
			return !cmd.HasAvailableSubCommands()
		}
	}
	if len(path) == 4 && path[1] == "databus" && path[2] == "app" {
		return path[3] == "create" || path[3] == "delete"
	}
	if len(path) == 5 && path[1] == "databus" && path[2] == "app" && path[3] == "client" {
		return path[4] == "list" || path[4] == "revoke"
	}
	return len(path) == 4 && path[1] == "edge-provisioning" && !cmd.HasAvailableSubCommands()
}

func prepareCommands(root *cobra.Command, runtime *app.Runtime, invoked *bool) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if run := cmd.RunE; run != nil {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				*invoked = true
				format, _ := cmd.Flags().GetString("format")
				if cmd.Flags().Lookup("format") != nil {
					if format != "" && format != "text" && format != "json" {
						allowed := "text"
						if supportsJSON(cmd) {
							allowed = "text or json"
						}
						return &clierror.Error{Code: clierror.CodeInvalidArgument, Message: fmt.Sprintf("invalid --format %q; expected %s", format, allowed)}
					}
					if format == "json" && !supportsJSON(cmd) {
						return &clierror.Error{Code: clierror.CodeFormatUnsupported, Message: "--format json is not supported for this command"}
					}
				}
				jsonOutput := format == "json"
				if jsonOutput {
					short, _ := cmd.Flags().GetBool("short")
					if short {
						return &clierror.Error{Code: clierror.CodeInvalidArgument, Message: "--short cannot be combined with --format json"}
					}
				}
				if nonInteractive(cmd) || jsonOutput {
					restore := runtime.WithoutInteraction()
					defer restore()
				}
				if runtime != nil && jsonOutput {
					for _, target := range []**commands.Runner{&runtime.Commands, &runtime.License} {
						if *target == nil {
							continue
						}
						previous := *target
						runner := *previous
						runner.JSON, runner.Out = true, cmd.OutOrStdout()
						*target = &runner
						defer func() { *target = previous }()
						if client, ok := previous.API.(*cloudapi.Client); ok {
							previousOut := client.Out
							client.Out = io.Discard
							defer func() { client.Out = previousOut }()
						}
					}
					if runtime.CloudAPI != nil {
						previous := runtime.CloudAPI.Out
						runtime.CloudAPI.Out = io.Discard
						defer func() { runtime.CloudAPI.Out = previous }()
					}
				}
				return run(cmd, args)
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}
