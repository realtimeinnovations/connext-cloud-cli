package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/spf13/cobra"
)

const commandGroupAnnotation = "rticloud.commandGroup"

var rootCommandGroups = []string{
	"Connect to Connext Cloud",
	"Manage Connext Cloud",
	"Setup",
}

func Execute(argv []string, out io.Writer, errOut io.Writer, runtime *app.Runtime) error {
	var disableSSLVerify bool
	root := newRootCommand(runtime, &disableSSLVerify)
	root.SetArgs(argv)
	root.SetOut(out)
	root.SetErr(errOut)
	return root.Execute()
}

func newRootCommand(runtime *app.Runtime, disableSSLVerify *bool) *cobra.Command {
	var versionFlag bool
	root := &cobra.Command{
		Use:           "rticloud [command]",
		Short:         "RTI Connext Cloud CLI",
		Long:          "RTI Connext Cloud CLI. First-time setup: run 'rticloud configure'.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if runtime != nil {
				runtime.CloudAPI.SSLVerify = !*disableSSLVerify
				if runtime.EdgeProvision != nil {
					runtime.EdgeProvision.SSLVerify = !*disableSSLVerify
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			if versionFlag {
				_, _ = fmt.Fprint(cmd.OutOrStdout(), app.VersionString())
				return nil
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().BoolVar(disableSSLVerify, "disable-ssl-verify", false, "Disable SSL certificate verification")
	root.Flags().BoolVar(&versionFlag, "version", false, "Print version and build metadata")
	root.SetHelpFunc(groupedRootHelp)

	root.AddCommand(
		groupCommand(newConfigureCommand(runtime), "Setup"),
		groupCommand(newLoginCommand(runtime), "Setup"),
		groupCommand(newLogoutCommand(runtime), "Setup"),
		groupCommand(newDatabusCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newObservabilityCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newClientCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newAppClientCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newNetworkCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newLicenseCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newEdgeProvisioningCommand(runtime), "Manage Connext Cloud"),
		groupCommand(newEdgeSyncCommand(runtime), "Connect to Connext Cloud"),
		groupCommand(newGatewayCommand(runtime), "Connect to Connext Cloud"),
		groupCommand(newSpyCommand(runtime), "Connect to Connext Cloud"),
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

// resolveConnextOutput returns the connext_artifacts directory (with trailing
// separator so resolveOutputPath treats it as a directory) when --service and
// --participant-id are set and the caller did not supply an explicit --output.
// Falls back to the caller-supplied value (including "") in all other cases.
func resolveConnextOutput(rt *app.Runtime, service, participantID, output string) string {
	if output != "" || service == "" || participantID == "" || rt == nil || rt.EdgeStore == nil {
		return output
	}
	return rt.EdgeStore.ConnextArtifactsDir(service, participantID) + string(os.PathSeparator)
}

// resolveConnextURL returns the device endpoint URL from the local store when
// --url is not set and a slot is available.  Falls back to the caller-supplied
// value (including "") in all other cases.
func resolveConnextURL(rt *app.Runtime, service, participantID, rawURL string) string {
	if rawURL != "" || service == "" || participantID == "" || rt == nil || rt.EdgeStore == nil {
		return rawURL
	}
	return rt.EdgeStore.ResolveDeviceURL(service, participantID)
}

// deriveDeviceURL constructs the device endpoint base URL from the Manager API
// host URL and the service namespace.  The "ces-" naming prefix is stripped
// from the service namespace (Kubernetes convention for the edge-service).
//
//	API host                              → device URL
//	https://test.cloud.dev-rti.com/…     → https://<svc>.devices.cloud.dev-rti.com
//	https://us-west-2.cloud.dev-rti.com  → https://<svc>.devices.cloud.dev-rti.com
//	http://localhost:8090                 → https://<svc>.devices.cloud.dev-rti.com (dev-local fallback)
func deriveDeviceURL(apiHost, serviceID string) string {
	// Strip scheme, extract hostname.
	h := apiHost
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	// Strip path/port.
	if i := strings.IndexAny(h, "/:"); i >= 0 {
		h = h[:i]
	}
	// Find ".cloud." and keep everything from "cloud." onwards.
	const marker = ".cloud."
	cloudDomain := ""
	if idx := strings.Index(h, marker); idx >= 0 {
		cloudDomain = h[idx+1:] // e.g. "cloud.dev-rti.com"
	} else {
		// Local/dev deployment (e.g. localhost): fall back to the dev-rti cloud domain.
		cloudDomain = "cloud.dev-rti.com"
	}
	name := strings.TrimPrefix(serviceID, "ces-")
	return "https://" + name + ".devices." + cloudDomain
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

func newConfigureCommand(runtime *app.Runtime) *cobra.Command {
	var region string
	var getRegion bool
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure CLI region settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if region != "" && getRegion {
				return fmt.Errorf("exactly one of --region or --get-region is allowed")
			}
			_, err := runtime.Config.ConfigureRegion(region, getRegion, os.Stdin, cmd.OutOrStdout())
			return err
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "Region to configure")
	cmd.Flags().BoolVar(&getRegion, "get-region", false, "Print the configured region")
	return cmd
}

func newLoginCommand(runtime *app.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Connext Cloud",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runtime.Auth.Login()
			return err
		},
	}
}

func newLogoutCommand(runtime *app.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout from Connext Cloud",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.Auth.Logout()
		},
	}
}

func newDatabusCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("databus", "Manage Databuses")

	{ // create
		var name, obsService, networkName string
		var replicas int
		var sysDesigner bool
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Databus",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.CreateDatabus(name, replicas, obsService, sysDesigner, networkName)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().IntVar(&replicas, "replicas", 2, "Number of replicas")
		c.Flags().StringVar(&obsService, "observability-service", "", "Observability Service to link")
		c.Flags().BoolVar(&sysDesigner, "system-designer", false, "Enable System Designer support")
		c.Flags().StringVar(&networkName, "network-name", "", "Network name")
		cmd.AddCommand(c)
	}

	{ // list
		var short bool
		c := &cobra.Command{
			Use:   "list",
			Short: "List Databuses",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Commands.ListDatabuses(short)
			},
		}
		c.Flags().BoolVar(&short, "short", false, "Print compact output")
		cmd.AddCommand(c)
	}

	{ // query
		var name string
		c := &cobra.Command{
			Use:   "query",
			Short: "Show Databus details",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.QueryDatabus(name)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	{ // delete
		var name string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Databus",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeleteDatabus(name)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	{ // disable
		var name string
		c := &cobra.Command{
			Use:   "disable",
			Short: "Disable a Databus",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.UpdateDatabusStatus(name, "disable")
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	{ // resume
		var name string
		c := &cobra.Command{
			Use:   "resume",
			Short: "Resume a Databus",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.UpdateDatabusStatus(name, "resume")
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	{ // set-observability
		var name, service string
		var unlink bool
		c := &cobra.Command{
			Use:   "set-observability",
			Short: "Link or unlink an Observability Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if (service == "") == !unlink {
					return fmt.Errorf("exactly one of --service or --unlink is required")
				}
				var svc any = service
				if unlink {
					svc = nil
				}
				return runtime.Commands.UpdateObservabilityLink(name, svc)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&service, "service", "", "Observability Service to link")
		c.Flags().BoolVar(&unlink, "unlink", false, "Unlink the current Observability Service")
		cmd.AddCommand(c)
	}

	{ // update-filters
		var name, filters string
		c := &cobra.Command{
			Use:   "update-filters",
			Short: "Update Databus filters",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if filters == "" {
					return fmt.Errorf("--filters is required")
				}
				return runtime.Commands.UpdateFilters(name, filters)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&filters, "filters", "", "Databus filters")
		cmd.AddCommand(c)
	}

	{ // add-user
		var name, email string
		c := &cobra.Command{
			Use:   "add-user",
			Short: "Add a Databus user",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if email == "" {
					return fmt.Errorf("--email is required")
				}
				return runtime.Commands.AddUserToDatabus(name, email)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&email, "email", "", "User email")
		cmd.AddCommand(c)
	}

	{ // remove-user
		var name, email string
		c := &cobra.Command{
			Use:   "remove-user",
			Short: "Remove a Databus user",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if email == "" {
					return fmt.Errorf("--email is required")
				}
				return runtime.Commands.RemoveUserFromDatabus(name, email)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&email, "email", "", "User email")
		cmd.AddCommand(c)
	}

	return cmd
}

func newObservabilityCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("observability", "Manage Observability Services")

	{ // create
		var name, networkName string
		c := &cobra.Command{
			Use:   "create",
			Short: "Create an Observability Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.CreateObsService(name, networkName)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&networkName, "network-name", "", "Network name")
		cmd.AddCommand(c)
	}

	{ // list
		var short bool
		c := &cobra.Command{
			Use:   "list",
			Short: "List Observability Services",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Commands.ListObservabilityServices(short)
			},
		}
		c.Flags().BoolVar(&short, "short", false, "Print compact output")
		cmd.AddCommand(c)
	}

	for _, sub := range []struct {
		use, short, action string
	}{
		{"query", "Show Observability Service details", "query"},
		{"delete", "Delete an Observability Service", "delete"},
		{"disable", "Disable an Observability Service", "disable"},
		{"resume", "Resume an Observability Service", "resume"},
	} {
		sub := sub
		var name string
		c := &cobra.Command{
			Use:   sub.use,
			Short: sub.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				switch sub.action {
				case "query":
					return runtime.Commands.QueryObservabilityService(name)
				case "delete":
					return runtime.Commands.DeleteObservabilityService(name)
				default:
					return runtime.Commands.UpdateDatabusStatus(name, sub.action)
				}
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	return cmd
}

func newClientCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("client", "Manage Databus client configurations")

	{ // create
		var name, clientName, kind string
		var port int
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Databus client configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if clientName == "" {
					return fmt.Errorf("--client-name is required")
				}
				if kind != "app" && kind != "gateway" && kind != "observability-collector" {
					return fmt.Errorf("invalid --kind %q; expected app, gateway, or observability-collector", kind)
				}
				return runtime.Commands.CreateClientConfig(name, port, kind, clientName)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&clientName, "client-name", "", "Client configuration name")
		c.Flags().IntVar(&port, "port", 7777, "Local port")
		c.Flags().StringVar(&kind, "kind", "app", "Client kind: app, gateway, or observability-collector")
		cmd.AddCommand(c)
	}

	{ // get
		var name, clientName string
		var example, force bool
		c := &cobra.Command{
			Use:   "get",
			Short: "Get a Databus client configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if clientName == "" {
					return fmt.Errorf("--client-name is required")
				}
				return runtime.Commands.GetClientConfig(name, clientName, example, force, "")
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&clientName, "client-name", "", "Client configuration name")
		c.Flags().BoolVar(&example, "example", false, "Include example configuration")
		c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")
		cmd.AddCommand(c)
	}

	{ // delete
		var name, clientName string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Databus client configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if clientName == "" {
					return fmt.Errorf("--client-name is required")
				}
				return runtime.Commands.DeleteClientConfig(name, clientName)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&clientName, "client-name", "", "Client configuration name")
		cmd.AddCommand(c)
	}

	return cmd
}

func newAppClientCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("app-client", "Manage application template clients")

	{ // list
		var name, appName string
		c := &cobra.Command{
			Use:   "list",
			Short: "List application template clients",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if appName == "" {
					return fmt.Errorf("--app-name is required")
				}
				return runtime.Commands.ListAppClients(name, appName)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&appName, "app-name", "", "Application template name")
		cmd.AddCommand(c)
	}

	{ // register
		var name, appName, clientID, csrFile string
		var genPrivateKey, force bool
		c := &cobra.Command{
			Use:   "register",
			Short: "Register an application template client",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if appName == "" {
					return fmt.Errorf("--app-name is required")
				}
				if clientID == "" {
					return fmt.Errorf("--client-id is required")
				}
				if (csrFile == "") == !genPrivateKey {
					return fmt.Errorf("exactly one of --csr-file or --gen-private-key is required")
				}
				return runtime.Commands.RegisterAppClient(name, appName, clientID, csrFile, genPrivateKey, force)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&appName, "app-name", "", "Application template name")
		c.Flags().StringVar(&clientID, "client-id", "", "Client ID")
		c.Flags().StringVar(&csrFile, "csr-file", "", "CSR file")
		c.Flags().BoolVar(&genPrivateKey, "gen-private-key", false, "Generate a private key")
		c.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")
		cmd.AddCommand(c)
	}

	{ // revoke
		var name, appName, clientID string
		c := &cobra.Command{
			Use:   "revoke",
			Short: "Revoke an application template client",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if appName == "" {
					return fmt.Errorf("--app-name is required")
				}
				if clientID == "" {
					return fmt.Errorf("--client-id is required")
				}
				return runtime.Commands.RevokeAppClient(name, appName, clientID)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		c.Flags().StringVar(&appName, "app-name", "", "Application template name")
		c.Flags().StringVar(&clientID, "client-id", "", "Client ID")
		cmd.AddCommand(c)
	}

	return cmd
}

func newNetworkCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("network", "Manage networks")

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List networks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.Commands.ListNetworks()
		},
	})

	{ // delete
		var name string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a network",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeleteNetwork(name)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Resource name")
		cmd.AddCommand(c)
	}

	return cmd
}

func newLicenseCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("license", "Download your RTI license file")

	{ // get
		var expirationDays int
		var output string
		c := &cobra.Command{
			Use:   "get",
			Short: "Download your RTI license file",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if cmd.Flags().Changed("expiration-days") && expirationDays < 0 {
					return fmt.Errorf("expiration-days must be greater than or equal to 0")
				}
				var days *int
				if cmd.Flags().Changed("expiration-days") {
					days = &expirationDays
				}
				return runtime.Commands.GetLicense(days, output)
			},
		}
		c.Flags().IntVar(&expirationDays, "expiration-days", 0, "License expiration days")
		c.Flags().StringVarP(&output, "output", "o", "rti_license.dat", "Output file")
		cmd.AddCommand(c)
	}

	return cmd
}

func newGatewayCommand(runtime *app.Runtime) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Connect your applications to Connext Cloud",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			if format != "" && format != "text" {
				return fmt.Errorf("invalid --format %q; expected text", format)
			}
			return runtime.RunGateway(format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "Output format: text")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show the status of the gateway in the current directory",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Gateway.Status()
			},
		},
		&cobra.Command{
			Use:   "reset",
			Short: "Reset the gateway in the current directory",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Gateway.Reset()
			},
		},
		&cobra.Command{
			Use:   "obs",
			Short: "Open the observability dashboard",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Gateway.OpenObservabilityDashboard()
			},
		},
	)
	return cmd
}

func newSpyCommand(runtime *app.Runtime) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "spy",
		Short: "Inspect Databus topics and samples with RTI DDS Spy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			if format != "" && format != "text" {
				return fmt.Errorf("invalid --format %q; expected text", format)
			}
			return runtime.RunSpy(format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "Output format: text")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show the status of the spy in the current directory",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Spy.Status()
			},
		},
		&cobra.Command{
			Use:   "reset",
			Short: "Reset the spy in the current directory",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Spy.Reset()
			},
		},
	)
	return cmd
}

// ── Edge Provisioning (parent) ────────────────────────────────────────────────

func newEdgeProvisioningCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("edge-provisioning", "Manage Edge Provisioning Services")
	cmd.AddCommand(
		newEdgeProvisioningServiceCommand(runtime),
		newEdgeProvisioningProfileCommand(runtime),
		newEdgeProvisioningCampaignCommand(runtime),
		newEdgeProvisioningDeviceCommand(runtime),
	)
	return cmd
}

// ── edge-provisioning service ─────────────────────────────────────────────────

func newEdgeProvisioningServiceCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("service", "Manage Provisioning Services")

	{ // list
		c := &cobra.Command{
			Use:   "list",
			Short: "List Provisioning Services",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.Commands.ListEdgeSystems()
			},
		}
		cmd.AddCommand(c)
	}

	{ // create
		var name, governanceFile, description string
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Provisioning Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if governanceFile == "" {
					return fmt.Errorf("--governance-file is required")
				}
				return runtime.Commands.CreateEdgeSystem(name, governanceFile, description)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Provisioning Service name")
		c.Flags().StringVar(&governanceFile, "governance-file", "", "Path to DDS Security Governance XML file")
		c.Flags().StringVar(&description, "description", "", "Optional description")
		cmd.AddCommand(c)
	}

	{ // query
		var name string
		c := &cobra.Command{
			Use:   "query",
			Short: "Show Provisioning Service details",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.QueryEdgeSystem(name)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Edge System name or ID")
		cmd.AddCommand(c)
	}

	{ // delete
		var name string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Provisioning Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeleteEdgeSystem(name)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Edge System name or ID")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning profile ────────────────────────────────────────────────

func newEdgeProvisioningProfileCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("profile", "Manage Participant Profiles")

	{ // create
		var edgeSystem, name, permissionsFile string
		var effectiveRevocationSeconds int
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Participant Profile",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if permissionsFile == "" {
					return fmt.Errorf("--permissions-file is required")
				}
				return runtime.Commands.CreateParticipant(edgeSystem, name, permissionsFile, effectiveRevocationSeconds)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&name, "name", "", "Participant name")
		c.Flags().StringVar(&permissionsFile, "permissions-file", "", "Path to DDS Security Permissions XML file")
		c.Flags().IntVar(&effectiveRevocationSeconds, "effective-revocation-seconds", 3600, "Certificate revocation period in seconds")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Participant Profiles",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListParticipants(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // query
		var edgeSystem, participantID string
		c := &cobra.Command{
			Use:   "query",
			Short: "Show Participant Profile details",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				return runtime.Commands.QueryParticipant(edgeSystem, participantID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, participantID string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Participant Profile",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				return runtime.Commands.DeleteParticipant(edgeSystem, participantID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning campaign ───────────────────────────────────────────────

func newEdgeProvisioningCampaignCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("campaign", "Manage Campaigns")

	{ // create
		var edgeSystem, participantID, devicesFile string
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				if devicesFile == "" {
					return fmt.Errorf("--devices-file is required")
				}
				return runtime.Commands.CreateCampaign(edgeSystem, participantID, devicesFile)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		c.Flags().StringVar(&devicesFile, "devices-file", "", "Path to JSON or CSV file with device inventory")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem, participantID string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Campaigns",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				return runtime.Commands.ListCampaigns(edgeSystem, participantID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		cmd.AddCommand(c)
	}

	{ // list-devices
		var edgeSystem, participantID, campaignID string
		c := &cobra.Command{
			Use:   "list-devices",
			Short: "List devices in a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				if campaignID == "" {
					return fmt.Errorf("--campaign-id is required")
				}
				return runtime.Commands.ListCampaignDevices(edgeSystem, participantID, campaignID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		c.Flags().StringVar(&campaignID, "campaign-id", "", "Campaign ID")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, participantID, campaignID string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				if campaignID == "" {
					return fmt.Errorf("--campaign-id is required")
				}
				return runtime.Commands.DeleteCampaign(edgeSystem, participantID, campaignID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		c.Flags().StringVar(&campaignID, "campaign-id", "", "Campaign ID")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning device ─────────────────────────────────────────────────

func newEdgeProvisioningDeviceCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("device", "Manage Devices")

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List all devices in a Provisioning Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListEdgeDevices(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // revoke
		var edgeSystem, participantID, campaignID, serial string
		c := &cobra.Command{
			Use:   "revoke",
			Short: "Revoke a Device",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				if campaignID == "" {
					return fmt.Errorf("--campaign-id is required")
				}
				if serial == "" {
					return fmt.Errorf("--serial is required")
				}
				return runtime.Commands.RevokeDevice(edgeSystem, participantID, campaignID, serial)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-id", "", "Participant ID")
		c.Flags().StringVar(&campaignID, "campaign-id", "", "Campaign ID")
		c.Flags().StringVar(&serial, "serial", "", "Device serial number")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-sync ─────────────────────────────────────────────────────────────────

func newEdgeSyncCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("edge-sync", "Sync security artifacts from a Provisioning Service to this device")

	// Persistent slot-selection flags shared by all subcommands.
	var connextDir, service, participantID string
	cmd.PersistentFlags().StringVar(&connextDir, "connext-dir", "", "Override the local artifact store base directory (default: <workdir>/.connext)")
	cmd.PersistentFlags().StringVar(&service, "service", "", "Provisioning Service ID (selects the store slot)")
	cmd.PersistentFlags().StringVar(&participantID, "participant-id", "", "Participant Profile ID")

	// --disable-ssl-verify must not be used with edge-sync: all endpoints use
	// mTLS and require certificate verification.  Reject it if supplied and
	// hide it from help output (cobra inherits the help func to subcommands).
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if c.Root().PersistentFlags().Changed("disable-ssl-verify") {
			return fmt.Errorf("--disable-ssl-verify cannot be used with edge-sync commands: mTLS requires certificate verification")
		}
		if runtime != nil && runtime.EdgeStore != nil && connextDir != "" {
			runtime.EdgeStore.BaseDir = connextDir
		}
		return nil
	}
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		f := c.Root().PersistentFlags().Lookup("disable-ssl-verify")
		f.Hidden = true
		defer func() { f.Hidden = false }()
		defaultHelp(c)
	})

	{ // enroll
		var serial, csrFile, keyFile, campaignToken, output string
		var macs []string
		c := &cobra.Command{
			Use:   "enroll",
			Short: "Enroll this device with a Provisioning Service (first-time setup)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if service == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				if serial == "" {
					return fmt.Errorf("--serial is required")
				}
				if len(macs) == 0 {
					return fmt.Errorf("--mac is required (at least one)")
				}
				if csrFile == "" {
					return fmt.Errorf("--csr-file is required")
				}
				if err := runtime.Commands.EnrollDevice(service, participantID, serial, macs, csrFile, keyFile, campaignToken, output); err != nil {
					return err
				}
				// Persist the device endpoint URL so subsequent commands don't
				// need --url.
				if runtime.EdgeStore != nil {
					deviceURL := deriveDeviceURL(runtime.Config.GetAPIURLSafe(), service)
					if err := runtime.EdgeStore.WriteDeviceURL(service, participantID, deviceURL); err != nil {
						_, _ = fmt.Fprintf(runtime.Out, "Warning: could not save device URL: %v\n", err)
					}
				}
				return nil
			},
		}
		c.Flags().StringVar(&serial, "serial", "", "Device serial number")
		c.Flags().StringSliceVar(&macs, "mac", nil, "Device MAC address (can be specified multiple times)")
		c.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM CSR file")
		c.Flags().StringVar(&keyFile, "key-file", "", "Path to PEM private key file to store alongside the mTLS certificate")
		c.Flags().StringVar(&campaignToken, "campaign-token", "", "Campaign enrollment JWT (required by the enrollment endpoint)")
		c.Flags().StringVarP(&output, "output", "o", "", "Directory to save enrollment artifacts (identity.crt, identity-ca-chain.crt, signed_governance.p7s); prints JSON to stdout if not set")
		cmd.AddCommand(c)
	}

	{ // identity
		var url, certFile, keyFile, caFile, serverAddr, csrFile, output string
		c := &cobra.Command{
			Use:   "identity",
			Short: "Request or renew an identity certificate",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				cert, key, ca := certFile, keyFile, caFile
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(service, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, service, participantID, output)
				resolvedURL := resolveConnextURL(runtime, service, participantID, url)
				return runtime.EdgeProvision.RequestIdentity(resolvedURL, cert, key, ca, serverAddr, participantID, csrFile, out)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		c.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM CSR file (required for first issuance)")
		c.Flags().StringVarP(&output, "output", "o", "", "Save identity_cert_pem to this path (defaults to connext_artifacts/ when --service and --participant-id are set)")
		cmd.AddCommand(c)
	}

	{ // permissions
		var url, certFile, keyFile, caFile, serverAddr, output string
		c := &cobra.Command{
			Use:   "permissions",
			Short: "Request or renew a permissions document",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				cert, key, ca := certFile, keyFile, caFile
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(service, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, service, participantID, output)
				resolvedURL := resolveConnextURL(runtime, service, participantID, url)
				return runtime.EdgeProvision.RequestPermissions(resolvedURL, cert, key, ca, serverAddr, participantID, out)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		c.Flags().StringVarP(&output, "output", "o", "", "Save permissions_doc_smime to this path (defaults to connext_artifacts/ when --service and --participant-id are set)")
		cmd.AddCommand(c)
	}

	{ // psk
		var url, certFile, keyFile, caFile, serverAddr, output string
		c := &cobra.Command{
			Use:   "psk",
			Short: "Request or rotate a Pre-Shared Key",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cert, key, ca := certFile, keyFile, caFile
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(service, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, service, participantID, output)
				resolvedURL := resolveConnextURL(runtime, service, participantID, url)
				return runtime.EdgeProvision.RequestPSK(resolvedURL, cert, key, ca, serverAddr, out)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		c.Flags().StringVarP(&output, "output", "o", "", "Save PSK JSON to this path (defaults to connext_artifacts/ when --service and --participant-id are set)")
		cmd.AddCommand(c)
	}

	{ // crl
		var url, certFile, keyFile, caFile, serverAddr, output string
		c := &cobra.Command{
			Use:   "crl",
			Short: "Fetch the Certificate Revocation List",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if participantID == "" {
					return fmt.Errorf("--participant-id is required")
				}
				cert, key, ca := certFile, keyFile, caFile
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(service, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, service, participantID, output)
				resolvedURL := resolveConnextURL(runtime, service, participantID, url)
				return runtime.EdgeProvision.GetCRL(resolvedURL, cert, key, ca, serverAddr, participantID, out)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		c.Flags().StringVarP(&output, "output", "o", "", "Output path (defaults to connext_artifacts/ when --service and --participant-id are set)")
		cmd.AddCommand(c)
	}

	{ // status
		var url, certFile, keyFile, caFile, serverAddr string
		c := &cobra.Command{
			Use:   "status",
			Short: "Get this device's enrollment and artifact status",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cert, key, ca := certFile, keyFile, caFile
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(service, participantID, cert, key, ca)
				}
				resolvedURL := resolveConnextURL(runtime, service, participantID, url)
				return runtime.EdgeProvision.DeviceStatus(resolvedURL, cert, key, ca, serverAddr)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		cmd.AddCommand(c)
	}

	{ // healthz
		var url string
		c := &cobra.Command{
			Use:   "healthz",
			Short: "Check Provisioning Service connectivity and health",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runtime.EdgeProvision.Healthz(url)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Provisioning Service signing API base URL (e.g. http://localhost:8080)")
		cmd.AddCommand(c)
	}

	return cmd
}
