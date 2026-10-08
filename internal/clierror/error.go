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

// Error codes are stable values in the CLI JSON error contract.
const (
	CodeAPIError          = "API_ERROR"
	CodeAuthRequired      = "AUTH_REQUIRED"
	CodeCanceled          = "CANCELED"
	CodeCommandFailed     = "COMMAND_FAILED"
	CodeConfigRequired    = "CONFIG_REQUIRED"
	CodeConflict          = "CONFLICT"
	CodeFormatUnsupported = "FORMAT_UNSUPPORTED"
	CodeInputRequired     = "INPUT_REQUIRED"
	CodeInvalidArgument   = "INVALID_ARGUMENT"
	CodeInvalidResponse   = "INVALID_RESPONSE"
	CodeNotFound          = "NOT_FOUND"
	CodeOperationFailed   = "OPERATION_FAILED"
	CodePermissionDenied  = "PERMISSION_DENIED"
	CodeRateLimited       = "RATE_LIMITED"
	CodeTimeout           = "TIMEOUT"
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
	case CodeInvalidArgument, CodeInputRequired, CodeFormatUnsupported:
		return 2
	case CodeAuthRequired, CodePermissionDenied:
		return 3
	case CodeNotFound:
		return 4
	case CodeConflict:
		return 5
	case CodeTimeout:
		return 6
	case CodeCanceled:
		return 130
	default:
		return 1
	}
}

func InputRequired(message, action string) *Error {
	return &Error{Code: CodeInputRequired, Message: message, RequiredAction: action}
}

func AuthRequired() *Error {
	return &Error{
		Code: CodeAuthRequired, Message: "No usable credentials are available. Run 'rticloud login' interactively or set CONNEXT_CLOUD_API_KEY.",
		RequiredAction: "configure_credentials",
	}
}

func From(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	result := &Error{Code: CodeCommandFailed, Message: err.Error(), Cause: err}
	var status *httputil.StatusError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		result.Code = CodeCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		result.Code = CodeTimeout
	case errors.As(err, &status):
		result.HTTPStatus = status.StatusCode
		switch status.StatusCode {
		case 401:
			result.Code, result.RequiredAction = CodeAuthRequired, "configure_credentials"
		case 403:
			result.Code = CodePermissionDenied
		case 404:
			result.Code = CodeNotFound
		case 409:
			result.Code = CodeConflict
		case 429:
			result.Code = CodeRateLimited
		default:
			result.Code = CodeAPIError
		}
	}
	// Retryability is conservative: a failed mutation may already have taken
	// effect. Read-only callers may explicitly mark an error as retryable.
	return result
}
