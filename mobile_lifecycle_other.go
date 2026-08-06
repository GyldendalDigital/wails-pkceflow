//go:build !android && !ios

package wailspkceflow

func platformMobileLifecycleEvents() mobileLifecycleEventSet {
	return mobileLifecycleEventSet{}
}
