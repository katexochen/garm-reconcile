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

// IsServerError reports whether garm failed to serve a request, which
// includes failures of the forge it talks to.
func IsServerError(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode >= http.StatusInternalServerError
}

// IsBadRequest reports whether garm rejected a request with HTTP 400, which
// includes requests that are not possible in the current state of a
// resource.
func IsBadRequest(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest
}

// ErrNoWebhooks is returned by the webhook calls if garm does not manage
// webhooks, which is the case unless enable_webhook_management is set in its
// configuration.
var ErrNoWebhooks = errors.New("garm is not configured to manage webhooks")

// webhookError recognizes that garm does not manage webhooks. It does not
// serve the endpoints then, so the error does not come from the API.
func webhookError(err error) error {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == "" {
		return ErrNoWebhooks
	}
	return err
}
