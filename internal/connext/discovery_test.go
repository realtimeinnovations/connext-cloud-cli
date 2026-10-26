// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransparentManagedResolution(t *testing.T) {
	root := managedTestEnvironment(t)
	managed := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	for _, env := range []map[string]string{{}, {"CONNEXTDDS_DIR": "ignored"}, {"NDDSHOME": managed}, {"NDDSHOME": filepath.Join(filepath.Dir(managed), ".", filepath.Base(managed))}} {
		var out bytes.Buffer
		result, err := DiscoverInstall(env, DiscoveryOptions{HTTPClient: rejectDownloadClient(t), Confirmations: scriptedConfirmation("", &out)})
		if err != nil || result.Path != managed || out.Len() != 0 {
			t.Fatalf("install=%#v err=%v unexpected prompt=%q", result, err, out.String())
		}
	}
}

func TestNDDSHOMEConfirmation(t *testing.T) {
	for _, scenario := range []struct {
		name, input         string
		external, cancelled bool
	}{
		{name: "enter-uses-managed", input: "\n"},
		{name: "choose-external", input: "2\n", external: true},
		{name: "eof-cancels", cancelled: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := managedTestEnvironment(t)
			managed := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
			custom := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiroutingservice")
			var out bytes.Buffer
			result, err := DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{HTTPClient: rejectDownloadClient(t), Confirmations: scriptedConfirmation(scenario.input, &out)})
			if scenario.cancelled {
				if err == nil || result.Path != "" {
					t.Fatalf("EOF accepted an installation: %#v %v", result, err)
				}
			} else {
				want := managed
				if scenario.external {
					want = custom
				}
				if err != nil || result.Path != want {
					t.Fatalf("install=%#v want=%s err=%v", result, want, err)
				}
			}
			if !strings.Contains(out.String(), custom) || !strings.Contains(out.String(), UseManagedConnextLabel) || !strings.Contains(out.String(), UseNDDSHOMELabel) {
				t.Fatalf("missing installation choices: %s", out.String())
			}
		})
	}
}

func TestSkipPreflightAcceptsNDDSHOMEWithoutAsking(t *testing.T) {
	managedTestEnvironment(t)
	custom := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiroutingservice")
	result, err := DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string, string) (string, error) {
		t.Fatal("unexpected confirmation")
		return "", nil
	}), AcceptExternalNDDSHome: true})
	if err != nil || result.Path != custom {
		t.Fatalf("%#v %v", result, err)
	}
	_, err = DiscoverInstall(map[string]string{"NDDSHOME": filepath.Join(t.TempDir(), "missing")}, DiscoveryOptions{AcceptExternalNDDSHome: true})
	if err == nil {
		t.Fatal("skip-preflight bypassed installation validation")
	}
}

func TestOverrideCancellationAndMissingPrompt(t *testing.T) {
	managedTestEnvironment(t)
	env := map[string]string{"NDDSHOME": "external"}
	_, err := DiscoverInstall(env, DiscoveryOptions{})
	if err == nil || !strings.Contains(err.Error(), "--skip-preflight") {
		t.Fatalf("%v", err)
	}
	cancelled := errors.New("cancelled")
	_, err = DiscoverInstall(env, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string, string) (string, error) { return "", cancelled })})
	if !errors.Is(err, cancelled) {
		t.Fatalf("%v", err)
	}
}

// Use one reader for every confirmation in a flow, including warning then copy.
func scriptedConfirmation(input string, out io.Writer) Confirmer {
	selector := prompt.Selector{In: bufio.NewReader(strings.NewReader(input)), Out: out}
	return ConfirmationFromSelector(selector.Select)
}

func rejectDownloadClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		t.Errorf("unexpected download: %s", request.URL)
		return nil, errors.New("unexpected download")
	})}

}
