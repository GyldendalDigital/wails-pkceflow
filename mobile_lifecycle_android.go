//go:build android

package wailspkceflow

func platformMobileLifecycleEvents() mobileLifecycleEventSet {
	return androidMobileLifecycleEvents()
}
