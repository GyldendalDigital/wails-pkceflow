package wailspkceflow_test

import (
	"context"
	"testing"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
)

func newTestService(t *testing.T) *wailspkceflow.AuthService {
	t.Helper()

	redirectURI := "http://127.0.0.1:9999/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)
	handler := oidctest.NewFakeFlowHandler(idp, redirectURI)

	svc, err := wailspkceflow.New(wailspkceflow.Options{
		Config: pkceflow.Config{IssuerURL: idp.IssuerURL(), ClientID: "test-app"},
		Flow:   handler,
		Store:  &oidctest.MemoryStore{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := wailspkceflow.New(wailspkceflow.Options{
		Config: pkceflow.Config{ClientID: "x"}, // missing IssuerURL
		Flow:   oidctest.NewFakeFlowHandler(nil, "http://127.0.0.1:9999/callback"),
	})
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestLoginStatusClaims(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if svc.IsAuthenticated() {
		t.Error("should not be authenticated before login")
	}

	if res := svc.Login(); !res.OK {
		t.Fatalf("Login: %+v", res)
	}

	if !svc.IsAuthenticated() {
		t.Error("should be authenticated after login")
	}
	if st := svc.AuthStatus(); !st.Valid || !st.CanUseApp {
		t.Errorf("AuthStatus = %+v", st)
	}

	dto, r := svc.Claims()
	if !r.OK {
		t.Fatalf("Claims: %+v", r)
	}
	if dto.Subject == "" {
		t.Error("claims subject empty")
	}
}

func TestClaims_NoSession(t *testing.T) {
	svc := newTestService(t)

	_, r := svc.Claims()
	if r.OK || r.Code != wailspkceflow.CodeNotAuthenticated {
		t.Errorf("expected not_authenticated, got %+v", r)
	}
}

func TestLogout(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if res := svc.Login(); !res.OK {
		t.Fatalf("Login: %+v", res)
	}

	if res := svc.Logout(); !res.OK {
		t.Fatalf("Logout: %+v", res)
	}
	if svc.IsAuthenticated() {
		t.Error("should not be authenticated after logout")
	}
}

func TestPauseResume_SafeWithoutStartup(t *testing.T) {
	svc := newTestService(t)
	// Lifecycle controls are no-ops before Wails starts the service.
	svc.Resume()
	svc.Pause()
}
