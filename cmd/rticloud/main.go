package main

import (
	"fmt"
	"os"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/cli"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
)

func main() {
	args, err := cli.ParseArgs(os.Args[1:])
	if err != nil {
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
