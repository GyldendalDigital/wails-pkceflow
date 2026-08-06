//go:build !android && !ios

package wailspkceflow

import "testing"

func TestDesktopPlatformHasNoMobileLifecycleEvents(t *testing.T) {
	if got := platformMobileLifecycleEvents(); got.enabled {
		t.Fatalf("desktop mobile lifecycle events = %+v, want disabled", got)
	}
}
