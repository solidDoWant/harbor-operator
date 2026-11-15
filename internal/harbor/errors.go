package harbor

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-openapi/runtime"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/models"
)

// Condition reasons for Harbor failures.
const (
	ReasonUnauthorized      = "Unauthorized"
	ReasonForbidden         = "Forbidden"
	ReasonNotFound          = "NotFound"
	ReasonConflict          = "Conflict"
	ReasonInvalidSpec       = "InvalidSpec"
	ReasonDeleteBlocked     = "DeleteBlocked"
	ReasonHarborUnavailable = "HarborUnavailable"
)

// Error is a failure reported by Harbor or on the way to it. Its message never
// contains the credential password.
type Error struct {
	// StatusCode is the HTTP status, or 0 when Harbor was not reached.
	StatusCode int
	// Message is Harbor's message, or a description of the network failure.
	Message string
	cause   error
}

func (e *Error) Error() string {
	if e.StatusCode == 0 {
		return "harbor: " + e.Message
	}

	return "harbor: HTTP " + http.StatusText(e.StatusCode) + ": " + e.Message
}

// Unwrap returns nothing: the original error can carry request details, and
// callers only need the mapped status.
func (e *Error) Unwrap() error { return nil }

type codeChecker interface{ IsCode(code int) bool }

type payloadGetter interface{ GetPayload() *models.Errors }

// statusCodes lists the statuses that go-client's generated error types answer
// to through IsCode.
var statusCodes = []int{
	http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
	http.StatusConflict, http.StatusPreconditionFailed, http.StatusInternalServerError,
	http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
}

// statusOf returns the HTTP status carried by err, or 0.
func statusOf(err error) int {
	var apiErr *runtime.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}

	var checker codeChecker
	if errors.As(err, &checker) {
		for _, code := range statusCodes {
			if checker.IsCode(code) {
				return code
			}
		}
	}

	return 0
}

// wrapError converts a go-client error into an *Error with the password
// removed from every text it keeps.
func wrapError(err error, password string) error {
	if err == nil {
		return nil
	}

	var existing *Error
	if errors.As(err, &existing) {
		return err
	}

	status := statusOf(err)
	message := ""

	var getter payloadGetter
	if errors.As(err, &getter) {
		if payload := getter.GetPayload(); payload != nil {
			parts := make([]string, 0, len(payload.Errors))
			for _, item := range payload.Errors {
				if item != nil && item.Message != "" {
					parts = append(parts, item.Message)
				}
			}

			message = strings.Join(parts, "; ")
		}
	}

	if message == "" {
		if status != 0 {
			message = http.StatusText(status)
		} else {
			message = err.Error()
		}
	}

	if password != "" {
		message = strings.ReplaceAll(message, password, "***")
	}

	return &Error{StatusCode: status, Message: message}
}

// IsNotFound reports whether err is a Harbor 404.
func IsNotFound(err error) bool {
	var harborErr *Error
	return errors.As(err, &harborErr) && harborErr.StatusCode == http.StatusNotFound
}

// MapError returns the condition reason for a Harbor error and whether the
// reconciler should retry with backoff. InvalidSpec waits for a spec change.
// A 412 means Harbor refused to delete a project that still holds content.
func MapError(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	status := 0

	var harborErr *Error
	if errors.As(err, &harborErr) {
		status = harborErr.StatusCode
	} else {
		status = statusOf(err)
	}

	switch status {
	case http.StatusBadRequest:
		return ReasonInvalidSpec, false
	case http.StatusUnauthorized:
		return ReasonUnauthorized, true
	case http.StatusForbidden:
		return ReasonForbidden, true
	case http.StatusNotFound:
		return ReasonNotFound, true
	case http.StatusConflict:
		return ReasonConflict, true
	case http.StatusPreconditionFailed:
		return ReasonDeleteBlocked, true
	default:
		return ReasonHarborUnavailable, true
	}
}

// IsForbidden reports whether err is a Harbor 403.
func IsForbidden(err error) bool {
	var harborErr *Error
	return errors.As(err, &harborErr) && harborErr.StatusCode == http.StatusForbidden
}

// IsRefused reports whether Harbor answered with a 4xx: it understood the request and did not carry it out. A network
// error, a timeout or a 5xx leaves the outcome unknown.
func IsRefused(err error) bool {
	if err == nil {
		return false
	}

	status := statusOf(err)

	var harborErr *Error
	if errors.As(err, &harborErr) {
		status = harborErr.StatusCode
	}

	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}
