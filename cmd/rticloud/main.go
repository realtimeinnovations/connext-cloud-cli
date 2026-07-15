// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package main

import (
	"fmt"
	"os"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/cli"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
)

func main() {
	runtime := app.NewRuntime("", os.Stdout)
	if err := cli.Execute(os.Args[1:], os.Stdout, os.Stderr, runtime); err != nil {
		switch typed := err.(type) {
		case gateway.GatewayError:
			_, _ = fmt.Fprintln(os.Stdout, typed.Error())
		default:
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
