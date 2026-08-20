package wailspkceflow_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
)

// errSentinelTransport is returned by a transport that never reaches the
// network, so observing it proves the request used the injected client.
var errSentinelTransport = errors.New("sentinel transport")

// recordingTransport records the path of every request it sees and optionally
// fails instead of delegating.
type recordingTransport struct {
	delegate http.RoundTripper
	failWith error

	mu    sync.Mutex
	paths []string
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.paths = append(t.paths, req.URL.Path)
	t.mu.Unlock()

	if t.failWith != nil {
		return nil, t.failWith
	}
	return t.delegate.RoundTrip(req)
}

func (t *recordingTransport) sawPath(path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.paths {
		if p == path {
			return true
		}
	}
	return false
}

// TestHTTPClient_UsedForDiscovery proves Options.HTTPClient reaches the core
// client: discovery fails with the transport's own sentinel error, which only
// the injected client can produce.
func TestHTTPClient_UsedForDiscovery(t *testing.T) {
	redirectURI := "http://127.0.0.1:9999/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)

	transport := &recordingTransport{failWith: errSentinelTransport}
	svc, err := wailspkceflow.New(wailspkceflow.Options{
		Config:     pkceflow.Config{IssuerURL: idp.IssuerURL(), ClientID: "test-app"},
		Flow:       oidctest.NewFakeFlowHandler(idp, redirectURI),
		Store:      &oidctest.MemoryStore{},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = svc.Client().Init(context.Background())
	if !errors.Is(err, errSentinelTransport) {
		t.Fatalf("Init error = %v, want it to wrap the injected transport's sentinel", err)
	}
	if !transport.sawPath("/.well-known/openid-configuration") {
		t.Errorf("injected transport saw %v, want the discovery request", transport.paths)
	}
}

// TestHTTPClient_UsedForTokenExchange proves the injected client carries the
// token-endpoint traffic too, not only discovery.
func TestHTTPClient_UsedForTokenExchange(t *testing.T) {
	redirectURI := "http://127.0.0.1:9999/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)

	transport := &recordingTransport{delegate: http.DefaultTransport}
	svc, err := wailspkceflow.New(wailspkceflow.Options{
		Config:     pkceflow.Config{IssuerURL: idp.IssuerURL(), ClientID: "test-app"},
		Flow:       oidctest.NewFakeFlowHandler(idp, redirectURI),
		Store:      &oidctest.MemoryStore{},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := svc.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if res := svc.Login(); !res.OK {
		t.Fatalf("Login: %+v", res)
	}

	if !transport.sawPath("/token") {
		t.Errorf("injected transport saw %v, want the token request", transport.paths)
	}
}

// TestHTTPClient_TimeoutApplies is the defect this option exists to fix: with
// no way to supply a client, auth used http.DefaultClient, which has no
// timeout, so a blackholed endpoint blocked callers indefinitely.
func TestHTTPClient_TimeoutApplies(t *testing.T) {
	// Block until the client gives up, but bound it so a regression fails fast
	// instead of hanging until the package test deadline.
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer slow.Close()

	svc, err := wailspkceflow.New(wailspkceflow.Options{
		Config:     pkceflow.Config{IssuerURL: slow.URL, ClientID: "test-app"},
		Flow:       oidctest.NewFakeFlowHandler(nil, "http://127.0.0.1:9999/callback"),
		Store:      &oidctest.MemoryStore{},
		HTTPClient: &http.Client{Timeout: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	err = svc.Client().Init(context.Background())
	elapsed := time.Since(start)

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Init after %s returned %v, want a timeout error from the injected client", elapsed, err)
	}
}
