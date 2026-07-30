package wailspkceflow

import (
	"context"
	"net/url"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/mobileflow"
)

type spyDeliverer struct{ urls []string }

func (s *spyDeliverer) DeliverURL(callbackURL string) {
	s.urls = append(s.urls, callbackURL)
}

type deliverableFlow struct {
	spyDeliverer
	redirectURI string
}

func (f *deliverableFlow) StartAuthFlow(context.Context, string) (string, error) {
	return "", nil
}

func (f *deliverableFlow) RedirectURI() string {
	return f.redirectURI
}

func TestHandleLaunchURL_RoutesToDeliverer(t *testing.T) {
	spy := &spyDeliverer{}
	s := &AuthService{deliver: spy}

	s.handleLaunchURL("https://app.example.com/callback?code=abc&state=xyz")

	if len(spy.urls) != 1 {
		t.Fatalf("delivered %d URLs, want 1", len(spy.urls))
	}
	if spy.urls[0] != "https://app.example.com/callback?code=abc&state=xyz" {
		t.Errorf("delivered %q", spy.urls[0])
	}
}

func TestHandleLaunchURL_NilDelivererDoesNotPanic(t *testing.T) {
	s := &AuthService{} // no deliverer configured (desktop)
	s.handleLaunchURL("https://app.example.com/callback")
}

func TestNewDerivesDeliveryFromFlow(t *testing.T) {
	flow := &deliverableFlow{redirectURI: "https://app.example.com/callback"}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: "https://idp.example.com",
			ClientID:  "test-app",
		},
		Flow: flow,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if service.deliver != flow {
		t.Fatal("New did not derive URL delivery from Options.Flow")
	}
}

func TestNewExplicitDeliveryOverridesFlow(t *testing.T) {
	flow := &deliverableFlow{redirectURI: "https://app.example.com/callback"}
	override := &spyDeliverer{}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: "https://idp.example.com",
			ClientID:  "test-app",
		},
		Flow:             flow,
		DeepLinkDelivery: override,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if service.deliver != override {
		t.Fatal("New did not preserve the explicit delivery override")
	}
}

func TestHandleLaunchURLLeavesFilteringToMobileFlow(t *testing.T) {
	const redirectURI = "https://app.example.com/auth/callback"
	opened := make(chan string, 1)
	handler := mobileflow.New(redirectURI, func(target string) error {
		opened <- target
		return nil
	})
	service := &AuthService{deliver: handler}

	query := url.Values{
		"redirect_uri": {redirectURI},
		"state":        {"expected"},
	}
	result := make(chan struct {
		callback string
		err      error
	}, 1)
	go func() {
		callback, err := handler.StartAuthFlow(
			context.Background(),
			"https://idp.example.com/authorize?"+query.Encode(),
		)
		result <- struct {
			callback string
			err      error
		}{callback: callback, err: err}
	}()

	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mobile flow opener")
	}

	service.handleLaunchURL("https://app.example.com/unrelated")
	want := redirectURI + "?code=abc&state=expected"
	service.handleLaunchURL(want)

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("StartAuthFlow: %v", got.err)
		}
		if got.callback != want {
			t.Errorf("callback = %q, want %q", got.callback, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated URL prevented the matching callback from completing")
	}
}
