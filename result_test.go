package wailspkceflow

import (
	"errors"
	"fmt"
	"testing"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
)

func TestNewResult(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantOK   bool
		wantCode string
	}{
		{"success", nil, true, ""},
		{"cancelled", pkceflow.ErrFlowCancelled, false, CodeCancelled},
		{"wrapped cancelled", fmt.Errorf("x: %w", pkceflow.ErrFlowCancelled), false, CodeCancelled},
		{"not initialized", pkceflow.ErrNotInitialized, false, CodeNotInitialized},
		{"not authenticated", pkceflow.ErrNotAuthenticated, false, CodeNotAuthenticated},
		{"permanent auth error", &pkceflow.AuthError{Code: "invalid_grant"}, false, CodeSessionExpired},
		{"non-permanent auth error", &pkceflow.AuthError{Code: "interaction_required"}, false, "interaction_required"},
		{"generic", errors.New("boom"), false, CodeError},
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
		})
	}
}

func TestNewResult_MessageNeverEmpty_OnError(t *testing.T) {
	got := newResult(errors.New("some failure"))
	if got.Message == "" {
		t.Error("Message should carry diagnostic detail on error")
	}
}
