// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import "fmt"

// Confirmation is a binary decision. Decline is always the default.
type Confirmation struct {
	Message      string
	AcceptLabel  string
	DeclineLabel string
}

type Confirmer interface {
	Confirm(Confirmation) (bool, error)
}

type ConfirmFunc func(Confirmation) (bool, error)

func (f ConfirmFunc) Confirm(request Confirmation) (bool, error) { return f(request) }

// ConfirmationFromSelector adapts the application's existing menu UI at its boundary.
// Discovery and installation consume boolean decisions, never menu selections.
func ConfirmationFromSelector(selectChoice func(string, []string) (string, error)) Confirmer {
	if selectChoice == nil {
		return nil
	}
	return selectionConfirmer(selectChoice)
}

type selectionConfirmer func(string, []string) (string, error)

func (selectChoice selectionConfirmer) Confirm(request Confirmation) (bool, error) {
	if selectChoice == nil {
		return false, fmt.Errorf("confirmation is unavailable")
	}
	choice, err := selectChoice(request.Message, []string{request.DeclineLabel, request.AcceptLabel})
	if err != nil {
		return false, err
	}
	switch choice {
	case request.AcceptLabel:
		return true, nil
	case request.DeclineLabel:
		return false, nil
	default:
		return false, fmt.Errorf("confirmation cancelled")
	}
}
