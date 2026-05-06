package cli

import (
	"fmt"
	"strconv"
	"strings"
)

func Usage() string {
	return strings.TrimSpace(`
RTI Connext Cloud CLI. First-time setup: run 'rticloud configure'.

Usage:
  rticloud [--disable-ssl-verify] <resource> <command> [flags]
  rticloud --version

Resources:
  gateway         Connect your applications to Connext Cloud
  databus         Manage Databuses
  observability   Manage Observability Services
  client          Manage Databus client configurations
  app-client      Manage application template clients
  network         Manage networks
  license         Download your RTI license file
  configure       Configure CLI region settings
  login           Login to the RTI Cloud service
  logout          Logout of the RTI Cloud service
  version         Print version and build metadata

Gateway commands:
  rticloud gateway          Start or configure a gateway in the current directory
  rticloud gateway status   Show the status of the gateway in the current directory
  rticloud gateway reset    Reset the gateway in the current directory
  rticloud gateway obs      Open the observability dashboard
`) + "\n"
}

type Args struct {
	DisableSSLVerify     bool
	Help                 bool
	Resource             string
	Command              string
	GatewayCommand       string
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
	Region               string
	GetRegion            bool
}

func ParseArgs(argv []string) (Args, error) {
	args := Args{Replicas: 2, Port: 7777, Kind: "app", Output: "rti_license.dat"}
	if len(argv) == 0 {
		args.Help = true
		return args, nil
	}
	index := 0
	for index < len(argv) && strings.HasPrefix(argv[index], "-") {
		switch argv[index] {
		case "-h", "--help":
			args.Help = true
			return args, nil
		case "--version":
			args.Resource = "version"
			index++
		case "--disable-ssl-verify":
			args.DisableSSLVerify = true
			index++
		default:
			return Args{}, fmt.Errorf("unknown global flag: %s", argv[index])
		}
	}
	if index >= len(argv) {
		if args.Resource == "version" {
			return args, nil
		}
		return Args{}, fmt.Errorf("resource is required")
	}
	args.Resource = argv[index]
	index++
	if index < len(argv) && (argv[index] == "-h" || argv[index] == "--help") {
		args.Help = true
		return args, nil
	}
	switch args.Resource {
	case "gateway":
		if index < len(argv) {
			args.GatewayCommand = argv[index]
			index++
		}
		if index != len(argv) {
			return Args{}, fmt.Errorf("unknown gateway argument: %s", argv[index])
		}
		return args, validate(args)
	case "configure":
		for index < len(argv) {
			switch argv[index] {
			case "--region":
				index++
				value, err := valueAt(argv, index, "--region")
				if err != nil {
					return Args{}, err
				}
				args.Region = value
			case "--get-region":
				args.GetRegion = true
			default:
				return Args{}, fmt.Errorf("unknown configure flag: %s", argv[index])
			}
			index++
		}
		return args, validate(args)
	case "login", "logout", "version":
		if index != len(argv) {
			return Args{}, fmt.Errorf("unexpected argument for %s: %s", args.Resource, argv[index])
		}
		return args, nil
	}
	if index >= len(argv) {
		if !isSupportedResource(args.Resource) {
			return Args{}, fmt.Errorf("unsupported resource: %s", args.Resource)
		}
		args.Help = true
		return args, nil
	}
	args.Command = argv[index]
	index++
	if index < len(argv) && (argv[index] == "-h" || argv[index] == "--help") {
		args.Help = true
		return args, nil
	}
	for index < len(argv) {
		token := argv[index]
		switch token {
		case "--name":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Name = value
		case "--replicas":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			replicas, err := strconv.Atoi(value)
			if err != nil {
				return Args{}, err
			}
			args.Replicas = replicas
		case "--observability-service":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.ObservabilityService = value
		case "--system-designer":
			args.SystemDesigner = true
		case "--network-name":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.NetworkName = value
		case "--short":
			args.Short = true
		case "--service":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Service = value
		case "--unlink":
			args.Unlink = true
		case "--filters":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Filters = value
		case "--email":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Email = value
		case "--client-name":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.ClientName = value
		case "--port":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			port, err := strconv.Atoi(value)
			if err != nil {
				return Args{}, err
			}
			args.Port = port
		case "--kind":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Kind = value
		case "--example":
			args.Example = true
		case "-f", "--force":
			args.Force = true
		case "--app-name":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.AppName = value
		case "--client-id":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.ClientID = value
		case "--csr-file":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.CSRFile = value
		case "--gen-private-key":
			args.GenPrivateKey = true
		case "--expiration-days":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			days, err := strconv.Atoi(value)
			if err != nil {
				return Args{}, err
			}
			args.ExpirationDays = days
			args.HasExpirationDays = true
		case "-o", "--output":
			index++
			value, err := valueAt(argv, index, token)
			if err != nil {
				return Args{}, err
			}
			args.Output = value
		default:
			return Args{}, fmt.Errorf("unknown flag: %s", token)
		}
		index++
	}
	return args, validate(args)
}

func valueAt(argv []string, index int, flag string) (string, error) {
	if index >= len(argv) || strings.HasPrefix(argv[index], "-") {
		return "", fmt.Errorf("missing value for %s", flag)
	}
	return argv[index], nil
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
		if args.Command != "list" && args.Command != "delete" {
			return fmt.Errorf("unsupported network command: %s", args.Command)
		}
		return requireName(args, args.Command == "delete")
	case "license":
		if args.Command != "get" {
			return fmt.Errorf("unsupported license command: %s", args.Command)
		}
		if args.HasExpirationDays && args.ExpirationDays < 0 {
			return fmt.Errorf("expiration-days must be greater than or equal to 0")
		}
	case "gateway":
		if args.GatewayCommand != "" && args.GatewayCommand != "status" && args.GatewayCommand != "reset" && args.GatewayCommand != "obs" {
			return fmt.Errorf("unsupported gateway command: %s", args.GatewayCommand)
		}
	case "configure", "login", "logout", "version":
	default:
		return fmt.Errorf("unsupported resource: %s", args.Resource)
	}
	return nil
}

func isSupportedResource(resource string) bool {
	switch resource {
	case "databus", "observability", "client", "app-client", "network", "license", "gateway", "configure", "login", "logout", "version":
		return true
	default:
		return false
	}
}

func validateDatabus(args Args) error {
	switch args.Command {
	case "create", "list", "query", "delete", "disable", "resume", "set-observability", "update-filters", "add-user", "remove-user":
	default:
		return fmt.Errorf("unsupported databus command: %s", args.Command)
	}
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
	switch args.Command {
	case "create", "list", "query", "delete", "disable", "resume":
	default:
		return fmt.Errorf("unsupported observability command: %s", args.Command)
	}
	return requireName(args, args.Command != "list")
}

func validateClient(args Args) error {
	switch args.Command {
	case "create", "get", "delete":
	default:
		return fmt.Errorf("unsupported client command: %s", args.Command)
	}
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
	switch args.Command {
	case "list", "register", "revoke":
	default:
		return fmt.Errorf("unsupported app-client command: %s", args.Command)
	}
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
