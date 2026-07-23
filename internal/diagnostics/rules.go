package diagnostics

import "strings"

const (
	UDPUnicastSocketCreateFailure = "NDDS_Transport_UDP_assertUnisocket:FAILED TO CREATE | default unicast socket"
	ComponentNotInstalledPrefix   = "The current configuration of your RTI product does not include "
	componentNotInstalledHint     = "See https://community.rti.com/documentation for information, or choose \"Download Connext Professional\" for a new installation that includes all the required components. To start from scratch, run \"rticloud gateway reset\", then \"rticloud gateway\" again."
)

func DefaultRules() []Rule {
	return []Rule{
		{
			ID:       "udp-unicast-socket-create-failed",
			Contains: UDPUnicastSocketCreateFailure,
			Severity: SeverityError,
			Summary: func(string) string {
				return "A required UDP socket could not be created."
			},
			Hint: "You may be running another gateway on this machine. Stop it or configure them to use different ports.",
		},
		{
			ID:       "rti-component-not-installed",
			Contains: ComponentNotInstalledPrefix,
			Severity: SeverityError,
			Summary:  componentNotInstalledSummary,
			Hint:     componentNotInstalledHint,
		},
	}
}

func componentNotInstalledSummary(line string) string {
	start := strings.Index(line, ComponentNotInstalledPrefix)
	if start < 0 {
		return strings.TrimSpace(line)
	}
	message := line[start:]
	if end := strings.Index(message, ". See "); end >= 0 {
		message = message[:end+1]
	}
	return strings.TrimSpace(message)
}
