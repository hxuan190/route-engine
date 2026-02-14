// Package common provides shared utilities used across all features
package common

import (
	"fmt"
	"net/http"
)

// HttpError represents an HTTP error with status code and message
type HttpError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HttpError) Error() string {
	return fmt.Sprintf("HTTP error: %d %s %s", e.StatusCode, e.Code, e.Message)
}

func messageOrDefault(msg string, defaultMsg string) string {
	if msg != "" {
		return msg
	}
	return defaultMsg
}

// HTTP Error constructors

func HTTPErrorBadRequest(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusBadRequest,
		Code:       "BAD_REQUEST",
		Message:    messageOrDefault(msg, "Bad request"),
	}
}

func HTTPErrorNotFound(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusNotFound,
		Code:       "NOT_FOUND",
		Message:    messageOrDefault(msg, "Not found"),
	}
}

func HTTPErrorInternalError(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusInternalServerError,
		Code:       "INTERNAL_SERVER_ERROR",
		Message:    messageOrDefault(msg, "Internal server error"),
	}
}

func HTTPErrorUnauthorized(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusUnauthorized,
		Code:       "UNAUTHORIZED",
		Message:    messageOrDefault(msg, "Unauthorized"),
	}
}

func HTTPErrorForbidden(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusForbidden,
		Code:       "FORBIDDEN",
		Message:    messageOrDefault(msg, "Forbidden"),
	}
}

func HTTPErrorResourceConflict(msg string) *HttpError {
	return &HttpError{
		StatusCode: http.StatusConflict,
		Code:       "RESOURCE_CONFLICT",
		Message:    messageOrDefault(msg, "Resource conflict"),
	}
}

// Legacy aliases (deprecated, use HTTP* versions)

func HttpErrorBadRequest(msg string) *HttpError {
	return HTTPErrorBadRequest(msg)
}

func HttpErrorNotFound(msg string) *HttpError {
	return HTTPErrorNotFound(msg)
}

func HttpErrorUnauthorized(msg string) *HttpError {
	return HTTPErrorUnauthorized(msg)
}

func HttpErrorConflict(msg string) *HttpError {
	return HTTPErrorResourceConflict(msg)
}
