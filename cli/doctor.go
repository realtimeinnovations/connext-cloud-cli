// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/doctor"
	"github.com/spf13/cobra"
)

func newDoctorCommand(runtime *app.Runtime) *cobra.Command {
	var format string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Check Cloud access and local Gateway/Spy prerequisites",
		Long:    "Inspect configuration, credential selection, Cloud access, and local Gateway/Spy prerequisites. Always non-interactive. Does not migrate files, save tokens, install software, or repair state. License dates are file metadata, not runtime validation.",
		Example: "  rticloud doctor\n  rticloud doctor --format json\n  rticloud doctor --timeout 20s",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return &clierror.Error{Code: clierror.CodeInvalidArgument, Message: "--timeout must be greater than zero"}
			}
			options := doctor.Options{Timeout: timeout}
			if runtime != nil {
				options.Config, options.Auth = runtime.Config, runtime.Auth
				if runtime.CloudAPI != nil {
					options.HTTPClient = runtime.CloudAPI.HTTPClient
				}
				if runtime.Gateway != nil {
					options.GatewayPath = runtime.Gateway.ConfigPath()
				}
				if runtime.Spy != nil {
					options.SpyPath = runtime.Spy.ConfigPath()
				}
			}
			report := doctor.Run(cmd.Context(), options)
			if err := report.Write(cmd.OutOrStdout(), format); err != nil {
				return err
			}
			if report.ExitCode != 0 {
				return &doctor.CompletedError{ExitCode: report.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Total timeout for Cloud authentication and access checks")
	return cmd
}
