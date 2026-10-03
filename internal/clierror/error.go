// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

// Package clierror defines errors that callers can inspect without parsing text.
package clierror

import (
	"context"
	"errors"
	"net"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

type Error struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	Retryable      bool   `json:"retryable"`
	RequiredAction string `json:"required_action,omitempty"`
	Cause          error  `json:"-"`
}

func (err *Error) Error() string { return err.Message }
func (err *Error) Unwrap() error { return err.Cause }

// ExitCode is stable across text and JSON output modes.
func (err *Error) ExitCode() int {
	switch err.Code {
	case "INVALID_ARGUMENT", "INPUT_REQUIRED", "FORMAT_UNSUPPORTED":
		return 2
	case "AUTH_REQUIRED", "PERMISSION_DENIED":
		return 3
	case "NOT_FOUND":
		return 4
	case "CONFLICT":
		return 5
	case "TIMEOUT":
		return 6
	case "CANCELED":
		return 130
	default:
		return 1
	}
}

func InputRequired(message, action string) *Error {
	return &Error{Code: "INPUT_REQUIRED", Message: message, RequiredAction: action}
}

func AuthRequired() *Error {
	return &Error{
		Code: "AUTH_REQUIRED", Message: "No usable credentials are available. Run 'rticloud login' interactively or set CONNEXT_CLOUD_API_KEY.",
		RequiredAction: "configure_credentials",
	}
}

func From(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	result := &Error{Code: "COMMAND_FAILED", Message: err.Error(), Cause: err}
	var status *httputil.StatusError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		result.Code = "CANCELED"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		result.Code = "TIMEOUT"
	case errors.As(err, &status):
		result.HTTPStatus = status.StatusCode
		switch status.StatusCode {
		case 401:
			result.Code, result.RequiredAction = "AUTH_REQUIRED", "configure_credentials"
		case 403:
			result.Code = "PERMISSION_DENIED"
		case 404:
			result.Code = "NOT_FOUND"
		case 409:
			result.Code = "CONFLICT"
		case 429:
			result.Code = "RATE_LIMITED"
		default:
			result.Code = "API_ERROR"
		}
	}
	// Retryability is conservative: a failed mutation may already have taken
	// effect. Read-only callers may explicitly mark an error as retryable.
	return result
}
