package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

var ErrHelp = errors.New("help requested")

const commandGroupAnnotation = "rticloud.commandGroup"

var rootCommandGroups = []string{
	"Connect to Connext Cloud",
	"Manage Connext Cloud",
	"Setup",
}

type Args struct {
	DisableSSLVerify     bool
	Resource             string
	Command              string
	GatewayCommand       string
	SpyCommand           string
	Name                 string
	Replicas             int
	ObservabilityService string
	SystemDesigner       bool
	NetworkName          string
	Short                bool
	Service              string
	Unlink               bool
	Filters              string
	Email                string
	ClientName           string
	Port                 int
	Kind                 string
	Example              bool
	Force                bool
	AppName              string
	ClientID             string
	CSRFile              string
	GenPrivateKey        bool
	ExpirationDays       int
	HasExpirationDays    bool
	Output               string
	Format               string
	Region               string
	GetRegion            bool
}

func ParseArgs(argv []string) (Args, error) {
	return ParseArgsWithOutput(argv, io.Discard, io.Discard)
}

func ParseArgsWithOutput(argv []string, out io.Writer, errOut io.Writer) (Args, error) {
	args := defaultArgs()
	ran := false
	root := newRootCommand(&args, &ran)
	root.SetArgs(argv)
	root.SetOut(out)
	root.SetErr(errOut)
	if err := root.Execute(); err != nil {
		return Args{}, err
	}
	if !ran {
		return Args{}, ErrHelp
	}
	return args, nil
}

func defaultArgs() Args {
	return Args{Replicas: 2, Port: 7777, Kind: "app", Output: "rti_license.dat"}
}

func newRootCommand(args *Args, ran *bool) *cobra.Command {
	versionFlag := false
	root := &cobra.Command{
		Use:           "rticloud [command]",
		Short:         "RTI Connext Cloud CLI",
		Long:          "RTI Connext Cloud CLI. First-time setup: run 'rticloud configure'.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			if versionFlag {
				args.Resource = "version"
				*ran = true
				return nil
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().BoolVar(&args.DisableSSLVerify, "disable-ssl-verify", false, "Disable SSL certificate verification")
	root.Flags().BoolVar(&versionFlag, "version", false, "Print version and build metadata")
	root.SetHelpFunc(groupedRootHelp)

	root.AddCommand(
		groupCommand(newConfigureCommand(args, ran), "Setup"),
		groupCommand(runnableCommand("login", "Login to Connext Cloud", args, ran, func() {
			args.Resource = "login"
		}), "Setup"),
		groupCommand(runnableCommand("logout", "Logout from Connext Cloud", args, ran, func() {
			args.Resource = "logout"
		}), "Setup"),
		groupCommand(newDatabusCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newObservabilityCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newClientCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newAppClientCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newNetworkCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newLicenseCommand(args, ran), "Manage Connext Cloud"),
		groupCommand(newGatewayCommand(args, ran), "Connect to Connext Cloud"),
		groupCommand(newSpyCommand(args, ran), "Connect to Connext Cloud"),
	)
	return root
}

func groupCommand(cmd *cobra.Command, group string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[commandGroupAnnotation] = group
	return cmd
}

func groupedRootHelp(cmd *cobra.Command, _ []string) {
	if cmd.HasParent() {
		defaultHelp(cmd)
		return
	}
	out := cmd.OutOrStdout()
	if cmd.Long != "" {
		_, _ = fmt.Fprintln(out, cmd.Long)
	} else if cmd.Short != "" {
		_, _ = fmt.Fprintln(out, cmd.Short)
	}
	_, _ = fmt.Fprintf(out, "\nUsage:\n  %s\n", cmd.UseLine())
	for _, group := range rootCommandGroups {
		commands := visibleCommandsInGroup(cmd.Commands(), group)
		if len(commands) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s:\n", group)
		printCommandList(out, commands)
	}
	if flags := strings.TrimRight(cmd.NonInheritedFlags().FlagUsages(), "\n"); flags != "" {
		_, _ = fmt.Fprintf(out, "\nFlags:\n%s\n", flags)
	}
	if flags := strings.TrimRight(cmd.InheritedFlags().FlagUsages(), "\n"); flags != "" {
		_, _ = fmt.Fprintf(out, "\nGlobal Flags:\n%s\n", flags)
	}
	_, _ = fmt.Fprintf(out, "\nUse \"%s [command] --help\" for more information about a command.\n", cmd.CommandPath())
}

func visibleCommandsInGroup(commands []*cobra.Command, group string) []*cobra.Command {
	matches := make([]*cobra.Command, 0)
	for _, cmd := range commands {
		if cmd.IsAvailableCommand() && cmd.Annotations[commandGroupAnnotation] == group {
			matches = append(matches, cmd)
		}
	}
	return matches
}

func defaultHelp(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	if cmd.Long != "" {
		_, _ = fmt.Fprintln(out, cmd.Long)
	} else if cmd.Short != "" {
		_, _ = fmt.Fprintln(out, cmd.Short)
	}
	_, _ = fmt.Fprintf(out, "\nUsage:\n  %s\n", cmd.UseLine())
	if cmd.HasAvailableSubCommands() {
		_, _ = fmt.Fprintln(out, "\nAvailable Commands:")
		printCommandList(out, visibleCommands(cmd.Commands()))
	}
	if flags := strings.TrimRight(cmd.NonInheritedFlags().FlagUsages(), "\n"); flags != "" {
		_, _ = fmt.Fprintf(out, "\nFlags:\n%s\n", flags)
	}
	if flags := strings.TrimRight(cmd.InheritedFlags().FlagUsages(), "\n"); flags != "" {
		_, _ = fmt.Fprintf(out, "\nGlobal Flags:\n%s\n", flags)
	}
	if cmd.HasAvailableSubCommands() {
		_, _ = fmt.Fprintf(out, "\nUse \"%s [command] --help\" for more information about a command.\n", cmd.CommandPath())
	}
}

func visibleCommands(commands []*cobra.Command) []*cobra.Command {
	visible := make([]*cobra.Command, 0)
	for _, cmd := range commands {
		if cmd.IsAvailableCommand() {
			visible = append(visible, cmd)
		}
	}
	return visible
}

func printCommandList(out io.Writer, commands []*cobra.Command) {
	width := 0
	for _, cmd := range commands {
		if len(cmd.Name()) > width {
			width = len(cmd.Name())
		}
	}
	for _, cmd := range commands {
		_, _ = fmt.Fprintf(out, "  %-*s  %s\n", width, cmd.Name(), cmd.Short)
	}
}

func newConfigureCommand(args *Args, ran *bool) *cobra.Command {
	cmd := runnableCommand("configure", "Configure CLI region settings", args, ran, func() {
		args.Resource = "configure"
	})
	cmd.Flags().StringVar(&args.Region, "region", "", "Region to configure")
	cmd.Flags().BoolVar(&args.GetRegion, "get-region", false, "Print the configured region")
	return cmd
}

func newDatabusCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("databus", "Manage Databuses")
	cmd.AddCommand(
		databusCommand("create", "Create a Databus", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().IntVar(&args.Replicas, "replicas", args.Replicas, "Number of replicas")
			c.Flags().StringVar(&args.ObservabilityService, "observability-service", "", "Observability Service to link")
			c.Flags().BoolVar(&args.SystemDesigner, "system-designer", false, "Enable System Designer support")
			c.Flags().StringVar(&args.NetworkName, "network-name", "", "Network name")
		}),
		databusCommand("list", "List Databuses", args, ran, func(c *cobra.Command) {
			c.Flags().BoolVar(&args.Short, "short", false, "Print compact output")
		}),
		databusCommand("query", "Show Databus details", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		databusCommand("delete", "Delete a Databus", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		databusCommand("disable", "Disable a Databus", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		databusCommand("resume", "Resume a Databus", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		databusCommand("set-observability", "Link or unlink an Observability Service", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().StringVar(&args.Service, "service", "", "Observability Service to link")
			c.Flags().BoolVar(&args.Unlink, "unlink", false, "Unlink the current Observability Service")
		}),
		databusCommand("update-filters", "Update Databus filters", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().StringVar(&args.Filters, "filters", "", "Databus filters")
		}),
		databusCommand("add-user", "Add a Databus user", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().StringVar(&args.Email, "email", "", "User email")
		}),
		databusCommand("remove-user", "Remove a Databus user", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().StringVar(&args.Email, "email", "", "User email")
		}),
	)
	return cmd
}

func databusCommand(use string, short string, args *Args, ran *bool, flags func(*cobra.Command)) *cobra.Command {
	return resourceCommand(use, short, "databus", use, args, ran, flags)
}

func newObservabilityCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("observability", "Manage Observability Services")
	cmd.AddCommand(
		observabilityCommand("create", "Create an Observability Service", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			c.Flags().StringVar(&args.NetworkName, "network-name", "", "Network name")
		}),
		observabilityCommand("list", "List Observability Services", args, ran, func(c *cobra.Command) {
			c.Flags().BoolVar(&args.Short, "short", false, "Print compact output")
		}),
		observabilityCommand("query", "Show Observability Service details", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		observabilityCommand("delete", "Delete an Observability Service", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		observabilityCommand("disable", "Disable an Observability Service", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
		observabilityCommand("resume", "Resume an Observability Service", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
	)
	return cmd
}

func observabilityCommand(use string, short string, args *Args, ran *bool, flags func(*cobra.Command)) *cobra.Command {
	return resourceCommand(use, short, "observability", use, args, ran, flags)
}

func newClientCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("client", "Manage Databus client configurations")
	cmd.AddCommand(
		clientCommand("create", "Create a Databus client configuration", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addClientNameFlag(c, args)
			c.Flags().IntVar(&args.Port, "port", args.Port, "Local port")
			c.Flags().StringVar(&args.Kind, "kind", args.Kind, "Client kind: app, gateway, or observability-collector")
		}),
		clientCommand("get", "Get a Databus client configuration", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addClientNameFlag(c, args)
			c.Flags().BoolVar(&args.Example, "example", false, "Include example configuration")
			c.Flags().BoolVarP(&args.Force, "force", "f", false, "Overwrite existing files")
		}),
		clientCommand("delete", "Delete a Databus client configuration", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addClientNameFlag(c, args)
		}),
	)
	return cmd
}

func clientCommand(use string, short string, args *Args, ran *bool, flags func(*cobra.Command)) *cobra.Command {
	return resourceCommand(use, short, "client", use, args, ran, flags)
}

func newAppClientCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("app-client", "Manage application template clients")
	cmd.AddCommand(
		appClientCommand("list", "List application template clients", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addAppNameFlag(c, args)
		}),
		appClientCommand("register", "Register an application template client", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addAppNameFlag(c, args)
			c.Flags().StringVar(&args.ClientID, "client-id", "", "Client ID")
			c.Flags().StringVar(&args.CSRFile, "csr-file", "", "CSR file")
			c.Flags().BoolVar(&args.GenPrivateKey, "gen-private-key", false, "Generate a private key")
			c.Flags().BoolVarP(&args.Force, "force", "f", false, "Overwrite existing files")
		}),
		appClientCommand("revoke", "Revoke an application template client", args, ran, func(c *cobra.Command) {
			addNameFlag(c, args)
			addAppNameFlag(c, args)
			c.Flags().StringVar(&args.ClientID, "client-id", "", "Client ID")
		}),
	)
	return cmd
}

func appClientCommand(use string, short string, args *Args, ran *bool, flags func(*cobra.Command)) *cobra.Command {
	return resourceCommand(use, short, "app-client", use, args, ran, flags)
}

func newNetworkCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("network", "Manage networks")
	cmd.AddCommand(
		resourceCommand("list", "List networks", "network", "list", args, ran, nil),
		resourceCommand("delete", "Delete a network", "network", "delete", args, ran, func(c *cobra.Command) { addNameFlag(c, args) }),
	)
	return cmd
}

func newLicenseCommand(args *Args, ran *bool) *cobra.Command {
	cmd := parentCommand("license", "Download your RTI license file")
	get := resourceCommand("get", "Download your RTI license file", "license", "get", args, ran, func(c *cobra.Command) {
		c.Flags().IntVar(&args.ExpirationDays, "expiration-days", 0, "License expiration days")
		c.Flags().StringVarP(&args.Output, "output", "o", args.Output, "Output file")
	})
	get.PreRun = func(cmd *cobra.Command, cmdArgs []string) {
		args.HasExpirationDays = cmd.Flags().Changed("expiration-days")
	}
	cmd.AddCommand(get)
	return cmd
}

func newGatewayCommand(args *Args, ran *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Connect your applications to Connext Cloud",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			args.Resource = "gateway"
			*ran = true
			return validate(*args)
		},
	}
	cmd.Flags().StringVar(&args.Format, "format", "", "Output format: text")
	cmd.AddCommand(
		gatewayCommand("status", "Show the status of the gateway in the current directory", args, ran),
		gatewayCommand("reset", "Reset the gateway in the current directory", args, ran),
		gatewayCommand("obs", "Open the observability dashboard", args, ran),
	)
	return cmd
}

func gatewayCommand(use string, short string, args *Args, ran *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			args.Resource = "gateway"
			args.GatewayCommand = use
			*ran = true
			return validate(*args)
		},
	}
	cmd.Flags().StringVar(&args.Format, "format", "", "Output format: text")
	return cmd
}

func newSpyCommand(args *Args, ran *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spy",
		Short: "Inspect Databus topics and samples with RTI DDS Spy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			args.Resource = "spy"
			*ran = true
			return validate(*args)
		},
	}
	cmd.Flags().StringVar(&args.Format, "format", "", "Output format: text")
	cmd.AddCommand(
		spyCommand("status", "Show the status of the spy in the current directory", args, ran),
		spyCommand("reset", "Reset the spy in the current directory", args, ran),
	)
	return cmd
}

func spyCommand(use string, short string, args *Args, ran *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			args.Resource = "spy"
			args.SpyCommand = use
			*ran = true
			return validate(*args)
		},
	}
	cmd.Flags().StringVar(&args.Format, "format", "", "Output format: text")
	return cmd
}

func parentCommand(use string, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			return cmd.Help()
		},
	}
}

func runnableCommand(use string, short string, args *Args, ran *bool, assign func()) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			assign()
			*ran = true
			return validate(*args)
		},
	}
}

func resourceCommand(use string, short string, resource string, command string, args *Args, ran *bool, flags func(*cobra.Command)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			args.Resource = resource
			args.Command = command
			*ran = true
			return validate(*args)
		},
	}
	if flags != nil {
		flags(cmd)
	}
	return cmd
}

func addNameFlag(cmd *cobra.Command, args *Args) {
	cmd.Flags().StringVar(&args.Name, "name", "", "Resource name")
}

func addClientNameFlag(cmd *cobra.Command, args *Args) {
	cmd.Flags().StringVar(&args.ClientName, "client-name", "", "Client configuration name")
}

func addAppNameFlag(cmd *cobra.Command, args *Args) {
	cmd.Flags().StringVar(&args.AppName, "app-name", "", "Application template name")
}

func validate(args Args) error {
	switch args.Resource {
	case "databus":
		return validateDatabus(args)
	case "observability":
		return validateObservability(args)
	case "client":
		return validateClient(args)
	case "app-client":
		return validateAppClient(args)
	case "network":
		return requireName(args, args.Command == "delete")
	case "license":
		if args.HasExpirationDays && args.ExpirationDays < 0 {
			return fmt.Errorf("expiration-days must be greater than or equal to 0")
		}
	case "gateway":
		return validateLiveFormat(args)
	case "spy":
		return validateLiveFormat(args)
	case "configure":
		if args.Region != "" && args.GetRegion {
			return fmt.Errorf("exactly one of --region or --get-region is allowed")
		}
	case "login", "logout", "version":
	default:
		return fmt.Errorf("unsupported resource: %s", args.Resource)
	}
	return nil
}

func validateLiveFormat(args Args) error {
	if args.Format == "" || args.Format == "text" {
		return nil
	}
	return fmt.Errorf("invalid --format %q; expected text", args.Format)
}

func validateDatabus(args Args) error {
	if err := requireName(args, args.Command != "list"); err != nil {
		return err
	}
	if args.Command == "set-observability" {
		if (args.Service == "") == !args.Unlink {
			return fmt.Errorf("exactly one of --service or --unlink is required")
		}
	}
	if args.Command == "update-filters" && args.Filters == "" {
		return fmt.Errorf("--filters is required")
	}
	if (args.Command == "add-user" || args.Command == "remove-user") && args.Email == "" {
		return fmt.Errorf("--email is required")
	}
	return nil
}

func validateObservability(args Args) error {
	return requireName(args, args.Command != "list")
}

func validateClient(args Args) error {
	if err := requireName(args, true); err != nil {
		return err
	}
	if args.ClientName == "" {
		return fmt.Errorf("--client-name is required")
	}
	if args.Command == "create" && args.Kind != "app" && args.Kind != "gateway" && args.Kind != "observability-collector" {
		return fmt.Errorf("invalid --kind %q; expected app, gateway, or observability-collector", args.Kind)
	}
	return nil
}

func validateAppClient(args Args) error {
	if err := requireName(args, true); err != nil {
		return err
	}
	if args.AppName == "" {
		return fmt.Errorf("--app-name is required")
	}
	if args.Command == "register" || args.Command == "revoke" {
		if args.ClientID == "" {
			return fmt.Errorf("--client-id is required")
		}
	}
	if args.Command == "register" {
		if (args.CSRFile == "") == !args.GenPrivateKey {
			return fmt.Errorf("exactly one of --csr-file or --gen-private-key is required")
		}
	}
	return nil
}

func requireName(args Args, required bool) error {
	if required && args.Name == "" {
		return fmt.Errorf("--name is required")
	}
	return nil
}
