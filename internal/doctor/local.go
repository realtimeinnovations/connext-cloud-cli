// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

func localChecks(r *Report, options Options) {
	install, exists, err := connext.InspectManagedInstallation()
	c := Check{ID: "managed_connext", Label: "Managed Connext", Status: "pass", Message: install.Version + " · installation metadata verified"}
	if install.Path != "" {
		c.Details = append(c.Details, detail("Location", install.Path))
	}
	if home := os.Getenv("NDDSHOME"); home != "" {
		c.Details = append(c.Details, detail("NDDSHOME", home), detail("Selection", "These checks describe managed Connext; an external NDDSHOME requires confirmation or --skip-preflight."))
	} else {
		c.Details = append(c.Details, detail("Selection", "Managed installation is the default"))
	}
	switch {
	case err != nil && install.Path == "":
		c.Status, c.Code, c.Message = "warn", "MANAGED_UNAVAILABLE", "Managed installation is unavailable for this platform or home directory"
		c.Details = append(c.Details, detail("Optional setup", "Gateway/Spy can use an external Connext installation selected interactively."))
	case err != nil:
		c.Status, c.Code, c.Message = "fail", "CONNEXT_INVALID", "Managed installation is incomplete or its metadata is invalid"
		c.RequiredAction, c.NextStep = "repair_connext", "Run rticloud gateway or rticloud spy interactively to repair the managed installation."
	case !exists:
		c.Status, c.Code, c.Message = "warn", "CONNEXT_MISSING", "Not installed; required for Gateway/Spy using managed Connext"
		c.Details = append(c.Details, detail("Optional setup", "Installed on first interactive Gateway/Spy setup; not needed for Cloud commands."))
	}
	r.add(c)
	if c.Status != "pass" {
		r.add(skip("gateway_tools", "Gateway tools", "Managed installation unavailable", "managed_connext"))
		r.add(skip("spy_tool", "Spy tool", "Managed installation unavailable", "managed_connext"))
		r.add(skip("license", "Connext license", "Managed installation unavailable", "managed_connext"))
	} else {
		r.add(toolsCheck(install, "gateway_tools", "Gateway tools", gateway.MinConnextVersion, []string{"rtiroutingservice", "rticollectorservicelite"}))
		r.add(toolsCheck(install, "spy_tool", "Spy tool", spy.MinConnextVersion, []string{"rtiddsspy"}))
		r.add(licenseCheck(install, options))
	}
	gatewayPath, spyPath := options.GatewayPath, options.SpyPath
	if gatewayPath == "" {
		gatewayPath = filepath.Join(".connext", "gateway.yaml")
	}
	if spyPath == "" {
		spyPath = filepath.Join(".connext", "spy.yaml")
	}
	r.add(projectCheck("gateway", gatewayPath))
	r.add(projectCheck("spy", spyPath))
}

func toolsCheck(install connext.Install, id, label, minimum string, tools []string) Check {
	c := Check{ID: id, Label: label, Status: "pass", Message: "Required launchers present and executable"}
	for _, tool := range tools {
		if err := connext.ValidateManagedTool(install, tool, minimum); err != nil {
			c.Status, c.Code, c.Message = "fail", "CONNEXT_COMPONENT_INVALID", "Required launcher missing, invalid, or Connext version too old"
			c.RequiredAction, c.NextStep = "repair_connext", "Run rticloud gateway or rticloud spy interactively to repair the managed installation."
			c.Details = append(c.Details, detail(tool, "Failed · "+connext.Executable(install.Path, tool)))
		} else {
			c.Details = append(c.Details, detail(tool, "OK"))
		}
	}
	return c
}

func licenseCheck(install connext.Install, options Options) Check {
	c := Check{ID: "license", Label: "Connext license", Status: "pass", Message: "Expiration metadata read; Connext validates the license at runtime"}
	inspection, err := connext.InspectManagedLicense(install)
	if err != nil {
		c.Status, c.Code, c.Message = "fail", "LICENSE_UNREADABLE", "License source cannot be read safely"
		c.RequiredAction, c.NextStep = "repair_license", "Check the configured license file paths and permissions, then run rticloud doctor."
		return c
	}
	if inspection.Contents == nil {
		c.Status, c.Code, c.Message = "warn", "LICENSE_MISSING", "No readable license found"
		c.RequiredAction, c.NextStep = "setup_license", "Run rticloud gateway or rticloud spy interactively to provision a Connext license."
		return c
	}
	c.Details = append(c.Details, detail("Source", inspection.Source))
	if inspection.CopyMatches {
		c.Details = append(c.Details, detail("Installation", "License copy matches source"))
	} else {
		c.Status, c.Code = "warn", "LICENSE_COPY_PENDING"
		c.Details = append(c.Details, detail("Installation", "License copy needs provisioning on next Gateway/Spy run"))
	}
	c.Features = licenseFeatures(inspection.Contents, options.Now)
	unknown, expiring, expired := len(c.Features) == 0, false, false
	for _, f := range c.Features {
		unknown = unknown || f.Status == "unknown"
		expiring = expiring || f.Status == "expiring"
		expired = expired || f.Status == "expired"
	}
	// Concatenated license files can contain alternative grants for a feature.
	// Report expired records, but do not infer runtime rejection from those alone.
	switch {
	case expired:
		c.Status, c.Code, c.Message = "warn", "LICENSE_EXPIRED_RECORDS", "License contains expired feature records; runtime acceptance is not verified"
		c.RequiredAction, c.NextStep = "review_license", "If using Gateway/Spy, review or renew expired license records before running them."
	case unknown:
		c.Status, c.Code, c.Message = "warn", "LICENSE_EXPIRATION_UNKNOWN", "Some license expiration metadata is unrecognized"
	case expiring:
		c.Status, c.Code, c.Message = "warn", "LICENSE_EXPIRING", "License feature records expire within 14 days"
		c.RequiredAction, c.NextStep = "renew_license", "If using Gateway/Spy, renew the Connext license before the reported expiration date."
	}
	return c
}

func projectCheck(command, path string) Check {
	c := Check{ID: command + "_config", Label: map[string]string{"gateway": "Gateway config", "spy": "Spy config"}[command], Status: "pass", Message: "YAML parsed successfully; runtime resources not checked", Details: []Detail{detail("Source", path)}}
	// Use each command's own YAML reader, without its setup/preflight workflow.
	workDir := filepath.Dir(filepath.Dir(path))
	var values map[string]any
	var err error
	if command == "gateway" {
		values, err = gateway.NewGatewayApp(workDir, io.Discard).ReadConfig()
	} else {
		values, err = spy.NewApp(workDir, io.Discard).ReadConfig()
	}
	if err != nil {
		c.Status, c.Code, c.Message = "fail", "PROJECT_CONFIG_INVALID", "Project configuration cannot be read or parsed"
		c.RequiredAction, c.NextStep = "repair_project_configuration", fmt.Sprintf("Repair %s, then run rticloud doctor.", path)
	} else if _, statErr := os.Stat(path); values == nil && os.IsNotExist(statErr) {
		c.Status, c.Code, c.Message = "skip", "PROJECT_NOT_CONFIGURED", "Not configured in this directory · optional, no action needed"
	} else if len(values) == 0 {
		c.Status, c.Code, c.Message = "fail", "PROJECT_CONFIG_INVALID", "Project configuration is empty"
		c.RequiredAction, c.NextStep = "repair_project_configuration", fmt.Sprintf("Repair %s, then run rticloud doctor.", path)
	}
	return c
}
