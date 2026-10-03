// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"errors"
	"io"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
)

func TestWithoutInteractionBlocksAndRestoresAllInteractiveCallbacks(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), io.Discard)
	selectFn := func(string, []string) (string, error) { return "selected", nil }
	inputFn := func(string) (string, error) { return "entered", nil }
	confirmFn := func(string) (bool, error) { return true, nil }
	runtime.Gateway.SelectFunc, runtime.Spy.SelectFunc, runtime.EdgeSyncAgent.SelectFunc = selectFn, selectFn, selectFn
	runtime.Gateway.InputFunc, runtime.Spy.InputFunc, runtime.EdgeSyncAgent.InputFunc = inputFn, inputFn, inputFn
	runtime.Gateway.ConfirmReloadFunc, runtime.Spy.ConfirmReloadFunc = confirmFn, confirmFn
	browserOpened := false
	runtime.Gateway.OpenBrowserFunc = func(string) error { browserOpened = true; return nil }

	restore := runtime.WithoutInteraction()
	for _, selectFn := range []func(string, []string) (string, error){runtime.Gateway.SelectFunc, runtime.Spy.SelectFunc, runtime.EdgeSyncAgent.SelectFunc} {
		_, err := selectFn("Choose resource", []string{"only choice"})
		var typed *clierror.Error
		if !errors.As(err, &typed) || typed.Code != "INPUT_REQUIRED" {
			t.Fatalf("selection was not blocked: %v", err)
		}
	}
	for _, inputFn := range []func(string) (string, error){runtime.Gateway.InputFunc, runtime.Spy.InputFunc, runtime.EdgeSyncAgent.InputFunc} {
		if _, err := inputFn("Enter input"); err == nil {
			t.Fatal("input was not blocked")
		}
	}
	for _, confirmFn := range []func(string) (bool, error){runtime.Gateway.ConfirmReloadFunc, runtime.Spy.ConfirmReloadFunc} {
		if _, err := confirmFn("Confirm"); err == nil {
			t.Fatal("confirmation was not blocked")
		}
	}
	if err := runtime.Gateway.OpenBrowserFunc("https://example.test"); err == nil || browserOpened {
		t.Fatal("browser action was not blocked")
	}
	if !runtime.NonInteractive || !runtime.Auth.NonInteractive || !runtime.WorkAuth.NonInteractive || !runtime.Config.NonInteractive || !runtime.EdgeSyncAgent.NonInteractive {
		t.Fatal("non-interactive policy was not applied to all components")
	}
	restore()
	if runtime.NonInteractive || runtime.Auth.NonInteractive || runtime.WorkAuth.NonInteractive || runtime.Config.NonInteractive || runtime.EdgeSyncAgent.NonInteractive {
		t.Fatal("non-interactive policy leaked into a later invocation")
	}
	if value, err := runtime.Gateway.SelectFunc("Choose", nil); err != nil || value != "selected" {
		t.Fatalf("selection was not restored: %q %v", value, err)
	}
	if err := runtime.Gateway.OpenBrowserFunc("https://example.test"); err != nil || !browserOpened {
		t.Fatal("browser action was not restored")
	}
}
