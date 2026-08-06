//go:build ios

package wailspkceflow

func platformMobileLifecycleEvents() mobileLifecycleEventSet {
	return iosMobileLifecycleEvents()
}
