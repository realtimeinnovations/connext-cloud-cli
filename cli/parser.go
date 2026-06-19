package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/edgesyncagent"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
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
func resolveConnextOutput(rt *app.Runtime, serial, service, domainID, participantID, output string) string {
	if output != "" || service == "" || participantID == "" || rt == nil || rt.EdgeStore == nil {
		return output
	}
	slotDomain := domainID
	if slotDomain == "" {
		slotDomain = service
	}
	return rt.EdgeStore.ConnextArtifactsDir(serial, slotDomain, participantID) + string(os.PathSeparator)
}

// resolveConnextURL returns the device endpoint URL from the local store when
// --url is not set and a slot is available.  Falls back to the caller-supplied
// value (including "") in all other cases.
// Returns an error if a store lookup is intended (--service and --participant-tpl-id set)
// but --serial is missing.
func resolveConnextURL(rt *app.Runtime, serial, service, domainID, participantID, rawURL string) (string, error) {
	if rawURL != "" || service == "" || participantID == "" || rt == nil || rt.EdgeStore == nil {
		return rawURL, nil
	}
	if serial == "" {
		return "", fmt.Errorf("--serial is required when using --service and --participant-tpl-id without --url")
	}
	slotDomain := domainID
	if slotDomain == "" {
		slotDomain = service
	}
	resolved := rt.EdgeStore.ResolveDeviceURL(serial, slotDomain, participantID)
	if resolved == "" {
		return "", fmt.Errorf("--url is required (device_url not found in store at %s)",
			rt.EdgeStore.DeviceURLPath(serial, slotDomain, participantID))
	}
	return resolved, nil
}

// campaignTokenClaims decodes the payload of a JWT (without verifying the
// signature) and returns its claims as a map.  Returns nil on any error.
func campaignTokenClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	// JWT payload uses raw base64url encoding (no padding).
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

// claimString extracts a string value from a JWT claims map using the
// "https://devices.cloud.rti.com/<key>" namespace prefix.
func claimString(claims map[string]any, key string) string {
	const ns = "https://devices.cloud.rti.com/"
	v, _ := claims[ns+key].(string)
	return v
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
	return app.DeriveDeviceURL(apiHost, serviceID)
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
		newEdgeProvisioningGovernanceTemplateCommand(runtime),
		newEdgeProvisioningPermissionsTemplateCommand(runtime),
		newEdgeProvisioningDomainTemplateCommand(runtime),
		newEdgeProvisioningParticipantTemplateCommand(runtime),
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
		var name, description string
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Provisioning Service",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.CreateEdgeSystem(name, description)
			},
		}
		c.Flags().StringVar(&name, "name", "", "Provisioning Service name")
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

// ── edge-provisioning governance-template ───────────────────────────────────────

func newEdgeProvisioningGovernanceTemplateCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("governance-template", "Manage Governance Templates")

	{ // create
		var edgeSystem, name, xmlFile string
		c := &cobra.Command{
			Use:   "create",
			Short: "Upload a Governance Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if xmlFile == "" {
					return fmt.Errorf("--governance-file is required")
				}
				return runtime.Commands.CreateGovernanceTemplate(edgeSystem, name, xmlFile)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&name, "name", "", "Template name")
		c.Flags().StringVar(&xmlFile, "governance-file", "", "Path to DDS Security Governance XML file")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Governance Templates",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListGovernanceTemplates(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, templateName string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Governance Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateName == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeleteGovernanceTemplate(edgeSystem, templateName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateName, "name", "", "Template name")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning permissions-template ──────────────────────────────────────

func newEdgeProvisioningPermissionsTemplateCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("permissions-template", "Manage Permissions Templates")

	{ // create
		var edgeSystem, name, xmlFile string
		c := &cobra.Command{
			Use:   "create",
			Short: "Upload a Permissions Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if xmlFile == "" {
					return fmt.Errorf("--permissions-file is required")
				}
				return runtime.Commands.CreatePermissionsTemplate(edgeSystem, name, xmlFile)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&name, "name", "", "Template name")
		c.Flags().StringVar(&xmlFile, "permissions-file", "", "Path to DDS Security Permissions XML file")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Permissions Templates",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListPermissionsTemplates(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // get
		var edgeSystem, templateName string
		c := &cobra.Command{
			Use:   "get",
			Short: "Get a Permissions Template (includes XML)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateName == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.GetPermissionsTemplate(edgeSystem, templateName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateName, "name", "", "Template name")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, templateName string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Permissions Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateName == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeletePermissionsTemplate(edgeSystem, templateName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateName, "name", "", "Template name")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning domain-template ──────────────────────────────────────────

func newEdgeProvisioningDomainTemplateCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("domain-template", "Manage Domain Templates")

	{ // create
		var edgeSystem, governanceTemplate, customGovernanceFile, customGovernanceName, domainTag string
		var domainID int
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Domain Template",
			Long: `Create a Domain Template

To see available Governance Templates for a service, run:
  rticloud edge-provisioning governance-template list --service <service>`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if governanceTemplate == "" && customGovernanceFile == "" {
					return fmt.Errorf("--governance-template is required (or provide --custom-governance-file to create one inline)")
				}
				if customGovernanceFile != "" && customGovernanceName == "" {
					return fmt.Errorf("--custom-governance-name is required when --custom-governance-file is provided")
				}
				return runtime.Commands.CreateDomainTemplate(edgeSystem, domainID, governanceTemplate, domainTag, customGovernanceFile, customGovernanceName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().IntVar(&domainID, "domain-id", 0, "DDS domain ID")
		c.Flags().StringVar(&governanceTemplate, "governance-template", "", "Name of an existing Governance Template (see: governance-template list)")
		c.Flags().StringVar(&domainTag, "domain-tag", "", "Tag to identify the domain template")
		c.Flags().StringVar(&customGovernanceFile, "custom-governance-file", "", "Path to inline custom Governance XML (creates a Governance Template automatically)")
		c.Flags().StringVar(&customGovernanceName, "custom-governance-name", "", "Name to assign to the Governance Template created from --custom-governance-file")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Domain Templates",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListDomainTemplates(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, templateID string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Domain Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateID == "" {
					return fmt.Errorf("--template-id is required")
				}
				return runtime.Commands.DeleteDomainTemplate(edgeSystem, templateID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateID, "template-id", "", "Domain Template ID (e.g. 1:rafa)")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning participant-template ──────────────────────────────────────

func newEdgeProvisioningParticipantTemplateCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("participant-template", "Manage Participant Templates")

	{ // create
		var edgeSystem, name, permissionsRef string
		var artifactMaxTTLMinutes int
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Participant Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				if permissionsRef == "" {
					return fmt.Errorf("--permissions-ref is required")
				}
				return runtime.Commands.CreateParticipantTemplate(edgeSystem, name, permissionsRef, artifactMaxTTLMinutes)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&name, "name", "", "Participant template name")
		c.Flags().StringVar(&permissionsRef, "permissions-ref", "", "Name of an existing Permissions Template")
		c.Flags().IntVar(&artifactMaxTTLMinutes, "artifact-max-ttl-minutes", 0, "Max artifact TTL in minutes (default: server-side 1440)")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Participant Templates",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListParticipantTemplates(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // get
		var edgeSystem, templateName string
		c := &cobra.Command{
			Use:   "get",
			Short: "Show Participant Template details",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateName == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.GetParticipantTemplate(edgeSystem, templateName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateName, "name", "", "Participant template name")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, templateName string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Participant Template",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if templateName == "" {
					return fmt.Errorf("--name is required")
				}
				return runtime.Commands.DeleteParticipantTemplate(edgeSystem, templateName)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&templateName, "name", "", "Participant template name")
		cmd.AddCommand(c)
	}

	return cmd
}

// ── edge-provisioning campaign ───────────────────────────────────────────────

func newEdgeProvisioningCampaignCommand(runtime *app.Runtime) *cobra.Command {
	cmd := parentCommand("campaign", "Manage Campaigns")

	{ // create
		var edgeSystem, participantID, enrollmentList, domainTemplateID string
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if participantID == "" {
					return fmt.Errorf("--participant-tpl-id is required")
				}
				if enrollmentList == "" {
					return fmt.Errorf("--enrollment-list is required")
				}
				if domainTemplateID == "" {
					return fmt.Errorf("--domain-tpl-id is required")
				}
				return runtime.Commands.CreateCampaign(edgeSystem, participantID, enrollmentList, domainTemplateID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&participantID, "participant-tpl-id", "", "Participant Template ID (see: edge-provisioning participant-template list, field: participant_id)")
		c.Flags().StringVar(&enrollmentList, "enrollment-list", "", "Path to JSON or CSV file with the list of devices to enroll")
		c.Flags().StringVar(&domainTemplateID, "domain-tpl-id", "", "Domain Template ID (see: edge-provisioning domain-template list, field: tag)")
		cmd.AddCommand(c)
	}

	{ // list
		var edgeSystem string
		c := &cobra.Command{
			Use:   "list",
			Short: "List Campaigns",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				return runtime.Commands.ListCampaigns(edgeSystem)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		cmd.AddCommand(c)
	}

	{ // list-devices
		var edgeSystem, campaignID string
		c := &cobra.Command{
			Use:   "list-devices",
			Short: "List devices in a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if campaignID == "" {
					return fmt.Errorf("--campaign-id is required")
				}
				return runtime.Commands.ListCampaignDevices(edgeSystem, campaignID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
		c.Flags().StringVar(&campaignID, "campaign-id", "", "Campaign ID")
		cmd.AddCommand(c)
	}

	{ // delete
		var edgeSystem, campaignID string
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete a Campaign",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if edgeSystem == "" {
					return fmt.Errorf("--service is required")
				}
				if campaignID == "" {
					return fmt.Errorf("--campaign-id is required")
				}
				return runtime.Commands.DeleteCampaign(edgeSystem, campaignID)
			},
		}
		c.Flags().StringVar(&edgeSystem, "service", "", "Provisioning Service name")
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
	var connextDir, service, domainID, participantID, serial string
	var debug bool
	cmd.PersistentFlags().StringVar(&connextDir, "connext-dir", "", "Override the local artifact store base directory (default: <workdir>/.connext)")
	cmd.PersistentFlags().StringVar(&service, "service", "", "Provisioning Service ID (selects the store slot)")
	cmd.PersistentFlags().StringVar(&domainID, "domain-tpl-id", "", "Domain Template ID")
	cmd.PersistentFlags().StringVar(&participantID, "participant-tpl-id", "", "Participant Template ID")
	cmd.PersistentFlags().StringVar(&serial, "serial", "", "Device serial number (selects the store slot under .connext/<serial>/)")
	cmd.PersistentFlags().BoolVar(&debug, "debug", false, "Log HTTP request and response bodies to stdout (or to --log-file for the agent subcommand)")

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
		if runtime != nil {
			if runtime.EdgeProvision != nil {
				runtime.EdgeProvision.Debug = debug
			}
			if runtime.EdgeSyncAgent != nil {
				runtime.EdgeSyncAgent.Debug = debug
			}
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
		var csrFile, keyFile, campaignToken, output string
		var macs []string
		c := &cobra.Command{
			Use:   "enroll",
			Short: "Enroll this device with a Provisioning Service (first-time setup)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if serial == "" {
					return fmt.Errorf("--serial is required")
				}
				if len(macs) == 0 {
					return fmt.Errorf("--mac is required (at least one)")
				}
				if csrFile == "" {
					return fmt.Errorf("--csr-file is required")
				}
				// Auto-populate service, participant-id and domain-id from the campaign
				// token when the user has not supplied them explicitly.
				effectiveService := service
				effectiveParticipant := participantID
				effectiveDomain := domainID
				if campaignToken != "" {
					if claims := campaignTokenClaims(campaignToken); claims != nil {
						if effectiveService == "" {
							effectiveService = claimString(claims, "edge_system_id")
						}
						if effectiveParticipant == "" {
							effectiveParticipant = claimString(claims, "participant_id")
						}
						if effectiveDomain == "" {
							effectiveDomain = claimString(claims, "domain_id")
						}
					}
				}
				if effectiveService == "" {
					return fmt.Errorf("--service is required (or provide a --campaign-token that includes the edge_system_id claim)")
				}
				if effectiveParticipant == "" {
					return fmt.Errorf("--participant-id is required (or provide a --campaign-token that includes the participant_id claim)")
				}
				domainTemplateID, err := runtime.Commands.EnrollDevice(effectiveService, effectiveParticipant, serial, macs, csrFile, keyFile, campaignToken, output)
				if err != nil {
					return err
				}
				// Persist the device endpoint URL using the serial+domainTemplateID-based
				// slot so subsequent commands resolve it from the correct folder.
				if runtime.EdgeStore != nil {
					slotID := domainTemplateID
					if slotID == "" {
						slotID = effectiveDomain
					}
					if slotID == "" {
						slotID = effectiveService
					}
					deviceURL := deriveDeviceURL(runtime.Config.GetAPIURLSafe(), effectiveService)
					if err := runtime.EdgeStore.WriteDeviceURL(serial, slotID, effectiveParticipant, deviceURL); err != nil {
						_, _ = fmt.Fprintf(runtime.Out, "Warning: could not save device URL: %v\n", err)
					}
				}
				return nil
			},
		}
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
					slotDomain := domainID
					if slotDomain == "" {
						slotDomain = service
					}
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, serial, service, domainID, participantID, output)
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
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
					slotDomain := domainID
					if slotDomain == "" {
						slotDomain = service
					}
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, serial, service, domainID, participantID, output)
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
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
					slotDomain := domainID
					if slotDomain == "" {
						slotDomain = service
					}
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, serial, service, domainID, participantID, output)
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
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
					slotDomain := domainID
					if slotDomain == "" {
						slotDomain = service
					}
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				out := resolveConnextOutput(runtime, serial, service, domainID, participantID, output)
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
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

	{ // renew-cert
		var url, certFile, keyFile, caFile, serverAddr, csrFile, output string
		var validityMinutes int
		c := &cobra.Command{
			Use:   "renew-cert",
			Short: "Renew the mTLS device certificate using the same key pair",
			Long: `Renew the mTLS device certificate without rotating the private key.

The device presents its current certificate via mTLS.  The server verifies that
the CSR subject and public key match the current certificate before signing and
returning a fresh certificate valid for a new period.

Provide a CSR generated from the same private key currently in use
(mtls_artifacts/device.key).  When --service and --participant-id are set the
renewed certificate and CA chain are saved directly into mtls_artifacts/.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if csrFile == "" {
					return fmt.Errorf("--csr-file is required")
				}
				cert, key, ca := certFile, keyFile, caFile
				slotDomain := domainID
				if slotDomain == "" {
					slotDomain = service
				}
				if runtime != nil && runtime.EdgeStore != nil {
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
				out := output
				if out == "" && service != "" && participantID != "" && runtime != nil && runtime.EdgeStore != nil {
					out = runtime.EdgeStore.MTLSDir(serial, slotDomain, participantID) + string(os.PathSeparator)
				}
				return runtime.EdgeProvision.RenewDeviceCert(resolvedURL, cert, key, ca, serverAddr, csrFile, validityMinutes, out)
			},
		}
		c.Flags().StringVar(&url, "url", "", "Device endpoint URL (auto-resolved from store when --service and --participant-id are set)")
		c.Flags().StringVar(&certFile, "cert", "", "Path to client certificate PEM file")
		c.Flags().StringVar(&keyFile, "key", "", "Path to client private key PEM file")
		c.Flags().StringVar(&caFile, "ca", "", "Path to Provisioning Service CA chain PEM file")
		c.Flags().StringVar(&serverAddr, "server", "", "TCP address to connect to (e.g. nlb.example.com:443); overrides DNS lookup while preserving TLS SNI")
		c.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM CSR file (must be signed by the same key as the current device certificate)")
		c.Flags().IntVar(&validityMinutes, "validity-minutes", 0, "Requested certificate lifetime in minutes (0 = server default)")
		c.Flags().StringVarP(&output, "output", "o", "", "Directory to save device.crt and ca-chain.pem (defaults to mtls_artifacts/ when --service and --participant-id are set)")
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
					slotDomain := domainID
					if slotDomain == "" {
						slotDomain = service
					}
					cert, key, ca = runtime.EdgeStore.ResolveMTLSDefaults(serial, slotDomain, participantID, cert, key, ca)
				}
				resolvedURL, err := resolveConnextURL(runtime, serial, service, domainID, participantID, url)
				if err != nil {
					return err
				}
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

	{ // agent
		var crlInterval time.Duration
		var logFile string
		var manualMode bool
		c := &cobra.Command{
			Use:   "agent",
			Short: "Run the artifact lifecycle agent (foreground process)",
			Long: `Run the long-lived artifact lifecycle agent.

The agent autonomously manages enrollment, identity certificates, permissions,
PSK, and CRL for one or more Participant Profiles.

On first run an interactive wizard prompts for a campaign token and immediately
enrolls the device using the auto-detected serial number and MAC addresses.
Use --manual to be prompted to confirm or override the detected values.

Once the agent is running, additional profiles can be enrolled with the
'enroll' sub-command or by dropping an enroll-*.json file into the inbox
directory.

The process runs in the foreground. Use systemd (Type=simple), launchd, or
your container runtime for supervision.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if crlInterval > 0 {
					runtime.EdgeSyncAgent.CRLInterval = crlInterval
				}
				runtime.EdgeSyncAgent.LogFile = logFile
				runtime.EdgeSyncAgent.ManualMode = manualMode
				err := runtime.EdgeSyncAgent.Run(cmd.Context())
				if err == nil {
					return nil
				}
				if _, ok := err.(common.UserError); ok {
					return gateway.GatewayError{Message: "Agent enrollment cancelled."}
				}
				if err == context.Canceled || err == context.DeadlineExceeded {
					return gateway.GatewayError{Message: "Agent enrollment cancelled."}
				}
				return err
			},
		}
		c.Flags().DurationVar(&crlInterval, "crl-interval", 5*time.Minute, "How often to refresh the Certificate Revocation List")
		c.Flags().StringVar(&logFile, "log-file", ".connext/rticloud-edge-agent.log", "Path to the agent log file (empty to disable)")
		c.Flags().BoolVar(&manualMode, "manual", false, "Prompt to confirm or override auto-detected serial number and MAC addresses during first-run enrollment")

		{ // agent enroll
			var campaignToken, serial, deviceName string
			var macs []string
			enroll := &cobra.Command{
				Use:   "enroll",
				Short: "Enroll an additional Participant Profile into the running agent",
				Long: `Write an enrollment request to the agent inbox.

The agent picks up the request within its poll interval (default 10 s) and
runs the full enrollment flow autonomously.  The campaign token is decoded to
extract the service ID and participant ID automatically.`,
				Args: cobra.NoArgs,
				RunE: func(cmd *cobra.Command, args []string) error {
					if campaignToken == "" {
						return fmt.Errorf("--campaign-token is required")
					}
					if deviceName == "" {
						return fmt.Errorf("--device-name is required (the name registered in the inventory)")
					}
					inboxDir := runtime.EdgeSyncAgent.InboxDir
					if err := os.MkdirAll(inboxDir, 0o755); err != nil {
						return err
					}
					serviceID, participantID, err := edgesyncagent.ParseCampaignToken(campaignToken)
					if err != nil {
						return fmt.Errorf("invalid campaign token: %w", err)
					}
					if serial == "" {
						serial = edgesyncagent.DetectSerial()
					}
					if len(macs) == 0 {
						macs = edgesyncagent.DetectMACs()
					}
					req := edgesyncagent.EnrollRequest{
						ServiceID:     serviceID,
						ParticipantID: participantID,
						CampaignToken: campaignToken,
						Serial:        serial,
						MACs:          macs,
						DeviceName:    deviceName,
					}
					data, _ := json.MarshalIndent(req, "", "  ")
					fname := strings.ReplaceAll(serviceID+"-"+participantID, "/", "_") + ".json"
					dest := filepath.Join(inboxDir, "enroll-"+fname)
					if err := os.WriteFile(dest, append(data, '\n'), 0o600); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Enrollment request written to %s\n", dest)
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "The agent will process it within its next poll interval.")
					return nil
				},
			}
			enroll.Flags().StringVar(&campaignToken, "campaign-token", "", "Campaign enrollment JWT (required)")
			enroll.Flags().StringVar(&deviceName, "device-name", "", "Device name as registered in the inventory (used as CSR Common Name prefix)")
			enroll.Flags().StringVar(&serial, "serial", "", "Device serial number (auto-detected if omitted)")
			enroll.Flags().StringArrayVar(&macs, "mac", nil, "MAC address (auto-detected if omitted; repeatable)")
			c.AddCommand(enroll)
		}

		{ // agent clean
			clean := &cobra.Command{
				Use:   "clean",
				Short: "Remove all agent state and start fresh",
				Long: `Delete the entire .connext directory, removing all enrolled profiles,
certificates, keys, and cached artifacts.  The next run of the agent will
trigger the first-run enrollment wizard.`,
				Args: cobra.NoArgs,
				RunE: func(cmd *cobra.Command, args []string) error {
					baseDir := runtime.EdgeSyncAgent.Store.BaseDir
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removing %s ...\n", baseDir)
					if err := runtime.EdgeSyncAgent.Clean(); err != nil {
						return fmt.Errorf("clean failed: %w", err)
					}
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Done. Run 'rticloud edge-sync agent' to re-enroll.")
					return nil
				},
			}
			c.AddCommand(clean)
		}

		cmd.AddCommand(c)
	}

	return cmd
}
