// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
)

// WithoutInteraction disables prompts and browser actions for one invocation.
// The returned function restores the runtime for embedded/repeated CLI calls.
func (runtime *Runtime) WithoutInteraction() func() {
	if runtime == nil {
		return func() {}
	}
	var restore []func()
	previous := runtime.NonInteractive
	runtime.NonInteractive = true
	restore = append(restore, func() { runtime.NonInteractive = previous })
	blockInput := func(message string) (string, error) {
		return "", clierror.InputRequired("Interactive input is disabled: "+message, "configure_interactively_or_provide_explicit_inputs")
	}
	blockSelect := func(message string, _ []string) (string, error) {
		return blockInput(message)
	}
	blockConfirm := func(message string) (bool, error) {
		_, err := blockInput(message)
		return false, err
	}
	if manager := runtime.Config; manager != nil {
		previous := manager.NonInteractive
		manager.NonInteractive = true
		restore = append(restore, func() { manager.NonInteractive = previous })
	}
	for _, manager := range []*auth.Manager{runtime.Auth, runtime.WorkAuth} {
		if manager != nil {
			previous := manager.NonInteractive
			manager.NonInteractive = true
			restore = append(restore, func() { manager.NonInteractive = previous })
		}
	}
	if gateway := runtime.Gateway; gateway != nil {
		selectFn, inputFn, confirmFn, browserFn := gateway.SelectFunc, gateway.InputFunc, gateway.ConfirmReloadFunc, gateway.OpenBrowserFunc
		gateway.SelectFunc, gateway.InputFunc, gateway.ConfirmReloadFunc = blockSelect, blockInput, blockConfirm
		gateway.OpenBrowserFunc = func(string) error {
			return clierror.InputRequired("Opening a browser is disabled by --non-interactive", "open_dashboard_interactively")
		}
		restore = append(restore, func() {
			gateway.SelectFunc, gateway.InputFunc, gateway.ConfirmReloadFunc, gateway.OpenBrowserFunc = selectFn, inputFn, confirmFn, browserFn
		})
	}
	if spy := runtime.Spy; spy != nil {
		selectFn, inputFn, confirmFn := spy.SelectFunc, spy.InputFunc, spy.ConfirmReloadFunc
		spy.SelectFunc, spy.InputFunc, spy.ConfirmReloadFunc = blockSelect, blockInput, blockConfirm
		restore = append(restore, func() { spy.SelectFunc, spy.InputFunc, spy.ConfirmReloadFunc = selectFn, inputFn, confirmFn })
	}
	if agent := runtime.EdgeSyncAgent; agent != nil {
		selectFn, inputFn, previous := agent.SelectFunc, agent.InputFunc, agent.NonInteractive
		agent.SelectFunc, agent.InputFunc, agent.NonInteractive = blockSelect, blockInput, true
		restore = append(restore, func() { agent.SelectFunc, agent.InputFunc, agent.NonInteractive = selectFn, inputFn, previous })
	}
	return func() {
		for i := len(restore) - 1; i >= 0; i-- {
			restore[i]()
		}
	}
}
