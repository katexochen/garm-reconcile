package garm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	apiparams "github.com/cloudbase/garm/apiserver/params"
)

// Error is an error response of the garm API.
type Error struct {
	StatusCode int
	// Code is the short description garm gives the error, like
	// "init_required" or "Bad Request".
	Code    string
	Details string
}

func (e *Error) Error() string {
	msg := e.Code
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Details != "" {
		msg += ": " + e.Details
	}
	return fmt.Sprintf("garm: %s (HTTP %d)", msg, e.StatusCode)
}

func newError(resp *http.Response) *Error {
	apiErr := &Error{StatusCode: resp.StatusCode}
	var body apiparams.APIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		apiErr.Code = body.Error
		apiErr.Details = body.Details
	}
	return apiErr
}

// IsNotInitialized reports whether err was returned because the controller
// has no admin user yet.
func IsNotInitialized(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Code == apiparams.InitializationRequired.Error
}

// IsNotFound reports whether garm does not know the requested resource.
func IsNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsBadRequest reports whether garm rejected a request with HTTP 400, which
// includes requests that are not possible in the current state of a
// resource.
func IsBadRequest(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest
}
