package wailspkceflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/mobileflow"
)

func TestNewResult(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantOK      bool
		wantCode    string
		wantMessage string
	}{
		{"success", nil, true, "", ""},
		{"cancelled", pkceflow.ErrFlowCancelled, false, CodeCancelled, messageCancelled},
		{"wrapped cancelled", fmt.Errorf("x: %w", pkceflow.ErrFlowCancelled), false, CodeCancelled, messageCancelled},
		{"context cancelled", context.Canceled, false, CodeCancelled, messageCancelled},
		{"context deadline", context.DeadlineExceeded, false, CodeCancelled, messageCancelled},
		{"flow in progress", mobileflow.ErrFlowInProgress, false, CodeFlowInProgress, messageFlowInProgress},
		{"not initialized", pkceflow.ErrNotInitialized, false, CodeNotInitialized, messageNotInitialized},
		{"not authenticated", pkceflow.ErrNotAuthenticated, false, CodeNotAuthenticated, messageNotAuthenticated},
		{"permanent auth error", &pkceflow.AuthError{Code: "invalid_grant"}, false, CodeSessionExpired, messageSessionExpired},
		{"non-permanent auth error", &pkceflow.AuthError{Code: "interaction_required"}, false, "interaction_required", messageProviderError},
		{"generic", errors.New("boom"), false, CodeError, messageError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newResult(tt.err)
			if got.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v", got.OK, tt.wantOK)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}

func TestNewResultDoesNotForwardErrorText(t *testing.T) {
	const canary = "secret-access-token-canary"
	tests := []error{
		errors.New(canary),
		fmt.Errorf("%s: %w", canary, pkceflow.ErrNotInitialized),
		&pkceflow.AuthError{Code: "interaction_required", Message: canary},
		&pkceflow.AuthError{Code: "invalid_grant", Message: canary},
	}

	for _, err := range tests {
		got := newResult(err)
		if got.Message == "" {
			t.Errorf("newResult(%T) returned an empty message", err)
		}
		if got.Message == canary || strings.Contains(got.Message, canary) {
			t.Errorf("newResult(%T) forwarded backend error text", err)
		}
	}
}
