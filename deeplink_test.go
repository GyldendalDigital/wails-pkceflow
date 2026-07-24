package wailspkceflow

import "testing"

type spyDeliverer struct{ urls []string }

func (s *spyDeliverer) DeliverURL(url string) { s.urls = append(s.urls, url) }

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
