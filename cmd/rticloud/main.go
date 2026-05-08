package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/cli"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
)

func main() {
	args, err := cli.ParseArgsWithOutput(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		if errors.Is(err, cli.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtime := app.NewRuntime("", os.Stdout)
	if err := runtime.Execute(args); err != nil {
		switch typed := err.(type) {
		case gateway.GatewayError:
			_, _ = fmt.Fprintln(os.Stdout, typed.Error())
		default:
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
