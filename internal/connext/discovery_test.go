// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransparentManagedResolution(t *testing.T) {
	managedTestEnvironment(t)
	old := ManagedInstaller
	t.Cleanup(func() { ManagedInstaller = old })
	managed, err := ManagedInstallationPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{{}, {"CONNEXTDDS_DIR": "ignored"}, {"NDDSHOME": managed}, {"NDDSHOME": filepath.Join(filepath.Dir(managed), ".", filepath.Base(managed))}} {
		called := false
		ManagedInstaller = func(DiscoveryOptions) (Install, error) { called = true; return Install{Path: managed}, nil }
		result, err := DiscoverInstall(env, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string) (string, error) { t.Fatal("unexpected selector"); return "", nil })})
		if err != nil || !called || result.Path != managed {
			t.Fatalf("%#v %v", result, err)
		}
	}
}

func TestNDDSHOMEConfirmation(t *testing.T) {
	managedTestEnvironment(t)
	custom := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiroutingservice")
	old := ManagedInstaller
	t.Cleanup(func() { ManagedInstaller = old })
	for _, choice := range []string{UseManagedConnextLabel, UseNDDSHOMELabel} {
		managedCalled := false
		ManagedInstaller = func(DiscoveryOptions) (Install, error) { managedCalled = true; return Install{Path: "managed"}, nil }
		prompts := 0
		result, err := DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(message string, choices []string) (string, error) {
			prompts++
			if !strings.Contains(message, custom) || !strings.Contains(message, "rticloud-managed Connext under your .rti directory?") || len(choices) != 2 || choices[0] != UseManagedConnextLabel {
				t.Fatalf("unexpected confirmation: %s %v", message, choices)
			}
			return choice, nil
		})})
		if err != nil || prompts != 1 {
			t.Fatalf("prompts=%d err=%v", prompts, err)
		}
		if choice == UseManagedConnextLabel && (!managedCalled || result.Path != "managed") {
			t.Fatal(result)
		}
		if choice == UseNDDSHOMELabel && (managedCalled || result.Path != custom) {
			t.Fatal(result)
		}
	}
}

func TestSkipPreflightAcceptsNDDSHOMEWithoutAsking(t *testing.T) {
	managedTestEnvironment(t)
	custom := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiroutingservice")
	result, err := DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string) (string, error) { t.Fatal("unexpected confirmation"); return "", nil }), AcceptExternalNDDSHome: true})
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
	_, err = DiscoverInstall(env, DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string) (string, error) { return "", cancelled })})
	if !errors.Is(err, cancelled) {
		t.Fatalf("%v", err)
	}
}
