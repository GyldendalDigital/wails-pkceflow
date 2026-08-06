//go:build ios

package wailspkceflow

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/events"
)

func TestIOSPlatformLifecycleEvents(t *testing.T) {
	got := platformMobileLifecycleEvents()
	if !got.enabled || got.pause != events.IOS.ApplicationDidEnterBackground ||
		got.resume != events.IOS.ApplicationWillEnterForeground {
		t.Fatalf("iOS mobile lifecycle events = %+v", got)
	}
}
