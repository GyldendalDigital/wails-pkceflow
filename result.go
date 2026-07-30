package wailspkceflow

import (
	"context"
	"errors"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/mobileflow"
)

// AuthResult is the structured outcome returned by the bound action methods
// (Login, Logout, Claims). It is returned in place of a Go error so the frontend
// receives an inspectable object instead of a rejected promise carrying only a
// message string.
//
// Frontends should branch on Code (a stable, non-localized key) and localize
// their own display text. Message is a non-localized diagnostic detail and never
// contains tokens.
type AuthResult struct {
	// OK is true when the operation succeeded.
	OK bool `json:"ok"`

	// Code is a stable machine-readable outcome code. Empty when OK is true.
	// Known values: "cancelled", "flow_in_progress", "not_initialized",
	// "not_authenticated", "session_expired", an OAuth2 error code (for
	// example "invalid_grant"), or "error".
	Code string `json:"code,omitempty"`

	// Message is a non-localized diagnostic detail. Never contains tokens.
	Message string `json:"message,omitempty"`
}

// Result code constants returned in AuthResult.Code.
const (
	// CodeCancelled indicates the user or context cancelled the flow.
	CodeCancelled = "cancelled"
	// CodeFlowInProgress indicates another frontend login or logout command is
	// still active.
	CodeFlowInProgress = "flow_in_progress"
	// CodeNotInitialized indicates a method was called before Init succeeded.
	CodeNotInitialized = "not_initialized"
	// CodeNotAuthenticated indicates there is no active session (for example
	// when reading Claims before login).
	CodeNotAuthenticated = "not_authenticated"
	// CodeSessionExpired indicates the refresh token is permanently invalid and
	// the user must re-authenticate.
	CodeSessionExpired = "session_expired"
	// CodeError is a generic fallback for unclassified errors.
	CodeError = "error"
)

// newResult maps a go-pkceflow error to a structured AuthResult. Permanent
// errors are reported as CodeSessionExpired; other OAuth2 errors carry their own
// code; sentinel errors map to their dedicated codes; anything else is CodeError.
func newResult(err error) AuthResult {
	if err == nil {
		return AuthResult{OK: true}
	}

	switch {
	case errors.Is(err, pkceflow.ErrFlowCancelled),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return AuthResult{Code: CodeCancelled, Message: err.Error()}
	case errors.Is(err, mobileflow.ErrFlowInProgress):
		return AuthResult{Code: CodeFlowInProgress, Message: err.Error()}
	case errors.Is(err, pkceflow.ErrNotInitialized):
		return AuthResult{Code: CodeNotInitialized, Message: err.Error()}
	case errors.Is(err, pkceflow.ErrNotAuthenticated):
		return AuthResult{Code: CodeNotAuthenticated, Message: err.Error()}
	case pkceflow.IsPermanentError(err):
		return AuthResult{Code: CodeSessionExpired, Message: err.Error()}
	}

	var authErr *pkceflow.AuthError
	if errors.As(err, &authErr) {
		return AuthResult{Code: authErr.Code, Message: authErr.Message}
	}

	return AuthResult{Code: CodeError, Message: err.Error()}
}

func flowInProgressResult() AuthResult {
	return AuthResult{
		Code:    CodeFlowInProgress,
		Message: "another authentication operation is already in progress",
	}
}

func serviceStoppedResult() AuthResult {
	return AuthResult{
		Code:    CodeCancelled,
		Message: "authentication service is not running",
	}
}
