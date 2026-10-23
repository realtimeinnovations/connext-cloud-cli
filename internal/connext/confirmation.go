// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"fmt"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
)

type ConfirmationKind uint8

const ConfirmLicenseFeatures ConfirmationKind = 1

// Confirmation is a binary decision. Decline is the default unless explicitly changed.
type Confirmation struct {
	Kind          ConfirmationKind
	Message       string
	AcceptLabel   string
	DeclineLabel  string
	DefaultAccept bool
}

type Confirmer interface {
	Confirm(Confirmation) (bool, error)
}

type ConfirmFunc func(Confirmation) (bool, error)

func (f ConfirmFunc) Confirm(request Confirmation) (bool, error) { return f(request) }

// ConfirmationFromSelector adapts the application's existing menu UI at its boundary.
// Discovery and installation consume boolean decisions, never menu selections.
func ConfirmationFromSelector(selectChoice prompt.SelectFunc) Confirmer {
	if selectChoice == nil {
		return nil
	}
	return selectionConfirmer(selectChoice)
}

type selectionConfirmer prompt.SelectFunc

// Choices puts the default first in both terminal and numbered menus.
func (request Confirmation) Choices() []string {
	choices := []string{request.DeclineLabel, request.AcceptLabel}
	if request.DefaultAccept {
		choices[0], choices[1] = choices[1], choices[0]
	}
	return choices
}

func (selectChoice selectionConfirmer) Confirm(request Confirmation) (bool, error) {
	if selectChoice == nil {
		return false, fmt.Errorf("confirmation is unavailable")
	}
	choices := request.Choices()
	choice, err := selectChoice(request.Message, choices, choices[0])
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
