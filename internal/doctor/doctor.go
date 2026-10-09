// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo"
)

type Options struct {
	Config      *config.Manager
	Auth        *auth.Manager
	HTTPClient  *http.Client
	GatewayPath string
	SpyPath     string
	Timeout     time.Duration
	Now         time.Time
}

func Run(ctx context.Context, options Options) Report {
	r := Report{Healthy: true}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	if options.Config == nil {
		options.Config = config.New("")
	}
	if options.Auth == nil {
		options.Auth = auth.New(options.Config, "")
	}
	r.add(installation())
	endpoint, configured := configuration(options.Config, &r)
	token, apiKey, credentialsOK := credentials(options.Auth, options.Now, &r)
	if !configured || !credentialsOK {
		var deps []string
		if !configured {
			deps = append(deps, "configuration")
		}
		if !credentialsOK {
			deps = append(deps, "credentials")
		}
		r.add(skip("cloud_access", "Cloud access", "Requires valid configuration and usable credentials", deps...))
	} else {
		networkCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		r.add(probe(networkCtx, options, endpoint, token, apiKey))
		cancel()
	}
	localChecks(&r, options)
	return r
}

func installation() Check {
	c := Check{ID: "cli", Label: "CLI", Status: CheckPass, Message: buildinfo.Version() + " · " + runtime.GOOS + "/" + runtime.GOARCH}
	c.Details = append(c.Details, detail("Build", strings.ReplaceAll(strings.TrimSpace(buildinfo.VersionString()), "\n", " · ")))
	executable, err := os.Executable()
	if err != nil {
		c.Status, c.Code, c.Message = CheckWarn, "EXECUTABLE_UNKNOWN", "Cannot determine the running executable"
		return c
	}
	c.Details = append(c.Details, detail("Executable", executable))
	others := pathCopies(executable, os.Getenv("PATH"))
	for _, other := range others {
		c.Details = append(c.Details, detail("Other copy", other))
	}
	if len(others) > 0 {
		c.Status, c.Code = CheckWarn, "MULTIPLE_INSTALLATIONS"
		c.NextStep = "Check PATH order when upgrading: other rticloud executables are present."
	}
	return c
}

func pathCopies(active, path string) []string {
	activeInfo, _ := os.Stat(active)
	var copies []string
	var infos []os.FileInfo
	if activeInfo != nil {
		infos = append(infos, activeInfo)
	}
	name := "rticloud"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
			continue
		}
		duplicate := false
		for _, previous := range infos {
			if os.SameFile(info, previous) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		infos = append(infos, info)
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			candidate = absolute
		}
		copies = append(copies, candidate)
	}
	return copies
}

func configuration(manager *config.Manager, r *Report) (string, bool) {
	c := Check{ID: "configuration", Label: "Configuration", Status: CheckPass, Details: []Detail{detail("Source", manager.Path)}}
	values, err := manager.InspectConfig()
	if err != nil {
		c.Status, c.Code, c.Message, c.RequiredAction = CheckFail, "CONFIG_INVALID", "Configuration cannot be read or parsed", "repair_configuration"
		c.NextStep = "Repair the configuration file, then run rticloud doctor."
		r.add(c)
		return "", false
	}
	endpoint := values["api_host"]
	if endpoint == "" {
		c.Status, c.Code, c.Message, c.RequiredAction = CheckFail, "CONFIG_REQUIRED", "No Cloud endpoint configured", "configure_region"
		c.NextStep = "Run rticloud configure (or rticloud configure --region us-east-2 --non-interactive). Existing legacy files are migrated by configure."
		r.add(c)
		return "", false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		c.Status, c.Code, c.Message, c.RequiredAction = CheckFail, "CONFIG_INVALID", "Cloud endpoint must be an HTTP(S) URL without credentials, query, or fragment", "configure_region"
		c.NextStep = "Run rticloud configure to correct the Cloud endpoint."
		r.add(c)
		return "", false
	}
	region := "custom"
	for key, value := range config.RegionURLMap {
		if strings.TrimRight(endpoint, "/") == strings.TrimRight(value, "/") {
			region = key
			break
		}
	}
	c.Message = region
	c.Details = append(c.Details, detail("Endpoint", endpoint))
	r.add(c)
	return endpoint, true
}

func credentials(manager *auth.Manager, now time.Time, r *Report) (string, string, bool) {
	c := Check{ID: "credentials", Label: "Credentials", Status: CheckPass, Details: []Detail{detail("Token cache", manager.TokenPath)}}
	apiKey := ""
	if manager.Env != nil {
		apiKey = manager.Env("CONNEXT_CLOUD_API_KEY")
	}
	copy := *manager
	copy.Now = func() time.Time { return now }
	inspection, err := copy.InspectCredentials()
	if err != nil {
		c.Status, c.Code, c.Message, c.RequiredAction = CheckFail, "CREDENTIALS_UNREADABLE", "Token cache cannot be read", "repair_credentials"
		c.NextStep = "Check access to the token cache file, then run rticloud doctor."
		r.add(c)
		return "", "", false
	}
	c.Details = append(c.Details, detail("Cached token", string(inspection.State)))
	if !inspection.ExpiresAt.IsZero() {
		c.Details = append(c.Details, detail("Usable until", inspection.ExpiresAt.Format(time.RFC3339)))
	}
	if inspection.State == auth.CredentialUsable {
		c.Message = fmt.Sprintf("Reusing cached access token · usable for another %s", inspection.ExpiresAt.Sub(now).Round(time.Second))
		if apiKey != "" {
			c.Details = append(c.Details, detail("API key", "Present · available to obtain a new token when the cache expires"))
		} else {
			c.Details = append(c.Details, detail("API key", "Not set"))
		}
		r.add(c)
		return inspection.AccessToken, "", true
	}
	if apiKey != "" {
		c.Message = "API key · token exchange checked below"
		c.Details = append(c.Details, detail("API key", "Present"))
		r.add(c)
		return "", apiKey, true
	}
	c.Status, c.Code, c.Message, c.RequiredAction = CheckFail, "AUTH_REQUIRED", "No usable credentials; cached token is "+string(inspection.State), "configure_credentials"
	c.Details = append(c.Details, detail("API key", "CONNEXT_CLOUD_API_KEY is not set"))
	c.NextStep = "Run rticloud login (rticloud login --device remotely), or supply CONNEXT_CLOUD_API_KEY through your secret store."
	r.add(c)
	return "", "", false
}
