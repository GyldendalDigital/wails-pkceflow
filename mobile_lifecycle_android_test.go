//go:build android

package wailspkceflow

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/events"
)

func TestAndroidPlatformLifecycleEvents(t *testing.T) {
	got := platformMobileLifecycleEvents()
	if !got.enabled || got.pause != events.Android.ActivityPaused || got.resume != events.Android.ActivityResumed {
		t.Fatalf("Android mobile lifecycle events = %+v", got)
	}
}
