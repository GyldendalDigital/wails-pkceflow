package wailspkceflow

import (
	"context"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type applicationEventSubscriber interface {
	OnApplicationEvent(events.ApplicationEventType, func(*application.ApplicationEvent)) func()
}

type mobileLifecycleEventSet struct {
	pause   events.ApplicationEventType
	resume  events.ApplicationEventType
	enabled bool
}

func androidMobileLifecycleEvents() mobileLifecycleEventSet {
	return mobileLifecycleEventSet{
		pause:   events.Android.ActivityPaused,
		resume:  events.Android.ActivityResumed,
		enabled: true,
	}
}

func iosMobileLifecycleEvents() mobileLifecycleEventSet {
	return mobileLifecycleEventSet{
		pause:   events.IOS.ApplicationDidEnterBackground,
		resume:  events.IOS.ApplicationWillEnterForeground,
		enabled: true,
	}
}

func (s *AuthService) subscribeApplicationEvents(
	runCtx context.Context,
	subscriber applicationEventSubscriber,
	mobileEvents mobileLifecycleEventSet,
) func() {
	if subscriber == nil {
		return nil
	}

	unsubscribes := make([]func(), 0, 3)
	if s.deliver != nil {
		unsubscribe := subscriber.OnApplicationEvent(
			events.Common.ApplicationLaunchedWithUrl,
			func(event *application.ApplicationEvent) {
				if event == nil || event.Context() == nil {
					return
				}
				s.handleLifecycleLaunchURL(runCtx, event.Context().URL())
			},
		)
		unsubscribes = append(unsubscribes, unsubscribe)
	}

	if mobileEvents.enabled {
		unsubscribePause := subscriber.OnApplicationEvent(
			mobileEvents.pause,
			func(*application.ApplicationEvent) {
				s.pauseRefreshLoop(runCtx, refreshPauseLifecycle)
			},
		)
		unsubscribeResume := subscriber.OnApplicationEvent(
			mobileEvents.resume,
			func(*application.ApplicationEvent) {
				s.resumeRefreshLoop(runCtx, refreshPauseLifecycle)
			},
		)
		unsubscribes = append(unsubscribes, unsubscribePause, unsubscribeResume)
	}

	return aggregateUnsubscribes(unsubscribes...)
}

func (s *AuthService) handleLifecycleLaunchURL(runCtx context.Context, url string) {
	s.mu.Lock()
	active := runCtx != nil && runCtx.Err() == nil && s.lifecycleActive && s.runCtx == runCtx
	deliver := s.deliver
	s.mu.Unlock()

	if active && deliver != nil {
		deliver.DeliverURL(url)
	}
}

func aggregateUnsubscribes(unsubscribes ...func()) func() {
	filtered := unsubscribes[:0]
	for _, unsubscribe := range unsubscribes {
		if unsubscribe != nil {
			filtered = append(filtered, unsubscribe)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(filtered) - 1; i >= 0; i-- {
				filtered[i]()
			}
		})
	}
}
