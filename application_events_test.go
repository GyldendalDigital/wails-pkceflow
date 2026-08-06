package wailspkceflow

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type callbackDeliverer func(string)

func (deliver callbackDeliverer) DeliverURL(url string) {
	deliver(url)
}

type recordedApplicationSubscription struct {
	eventType    events.ApplicationEventType
	callback     func(*application.ApplicationEvent)
	unsubscribed atomic.Int32
}

type recordingApplicationEventSubscriber struct {
	mu            sync.Mutex
	subscriptions []*recordedApplicationSubscription
}

func (s *recordingApplicationEventSubscriber) OnApplicationEvent(
	eventType events.ApplicationEventType,
	callback func(*application.ApplicationEvent),
) func() {
	subscription := &recordedApplicationSubscription{
		eventType: eventType,
		callback:  callback,
	}
	s.mu.Lock()
	s.subscriptions = append(s.subscriptions, subscription)
	s.mu.Unlock()
	return func() {
		subscription.unsubscribed.Add(1)
	}
}

func (s *recordingApplicationEventSubscriber) snapshot() []*recordedApplicationSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*recordedApplicationSubscription(nil), s.subscriptions...)
}

func (s *recordingApplicationEventSubscriber) callback(
	eventType events.ApplicationEventType,
) func(*application.ApplicationEvent) {
	for _, subscription := range s.snapshot() {
		if subscription.eventType == eventType {
			return subscription.callback
		}
	}
	return nil
}

func (s *recordingApplicationEventSubscriber) emit(
	t *testing.T,
	eventType events.ApplicationEventType,
	event *application.ApplicationEvent,
) {
	t.Helper()
	callback := s.callback(eventType)
	if callback == nil {
		t.Fatalf("no subscription for application event %d", eventType)
	}
	callback(event)
}

func TestMobileLifecycleEventMappings(t *testing.T) {
	tests := []struct {
		name       string
		events     mobileLifecycleEventSet
		wantPause  events.ApplicationEventType
		wantResume events.ApplicationEventType
	}{
		{
			name:       "Android",
			events:     androidMobileLifecycleEvents(),
			wantPause:  events.Android.ActivityPaused,
			wantResume: events.Android.ActivityResumed,
		},
		{
			name:       "iOS",
			events:     iosMobileLifecycleEvents(),
			wantPause:  events.IOS.ApplicationDidEnterBackground,
			wantResume: events.IOS.ApplicationWillEnterForeground,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refresh := &recordingRefreshController{}
			service := &AuthService{refresh: refresh}
			runCtx := installTestLifecycle(t, service, context.Background())
			subscriber := &recordingApplicationEventSubscriber{}
			cleanup := service.subscribeApplicationEvents(runCtx, subscriber, test.events)
			service.setLifecycleCleanup(runCtx, cleanup)

			subscriptions := subscriber.snapshot()
			gotEventTypes := make([]events.ApplicationEventType, 0, len(subscriptions))
			for _, subscription := range subscriptions {
				gotEventTypes = append(gotEventTypes, subscription.eventType)
			}
			if !test.events.enabled {
				t.Fatal("mobile lifecycle events are disabled")
			}
			wantEventTypes := []events.ApplicationEventType{test.wantPause, test.wantResume}
			if !slices.Equal(gotEventTypes, wantEventTypes) {
				t.Fatalf("application event subscriptions = %v, want %v", gotEventTypes, wantEventTypes)
			}

			service.startRefreshLoop(runCtx)
			subscriber.emit(t, test.events.pause, nil)
			subscriber.emit(t, test.events.pause, nil)
			subscriber.emit(t, test.events.resume, nil)
			subscriber.emit(t, test.events.resume, nil)
			if got := refresh.recordedOperations(); !slices.Equal(got, []string{"start", "stop", "start"}) {
				t.Fatalf("refresh operations = %v, want [start stop start]", got)
			}

			cleared := service.clearLifecycle()
			service.stopRefreshLoop(cleared)
			for _, subscription := range subscriptions {
				if got := subscription.unsubscribed.Load(); got != 1 {
					t.Errorf("event %d unsubscribe calls = %d, want 1", subscription.eventType, got)
				}
			}
		})
	}
}

func TestApplicationSubscriptionsHaveAggregateIdempotentCleanup(t *testing.T) {
	service := &AuthService{
		deliver: &spyDeliverer{},
		refresh: &recordingRefreshController{},
	}
	runCtx := installTestLifecycle(t, service, context.Background())
	subscriber := &recordingApplicationEventSubscriber{}
	mobileEvents := mobileLifecycleEventSet{
		pause:   events.Android.ActivityPaused,
		resume:  events.Android.ActivityResumed,
		enabled: true,
	}
	cleanup := service.subscribeApplicationEvents(runCtx, subscriber, mobileEvents)
	service.setLifecycleCleanup(runCtx, cleanup)

	subscriptions := subscriber.snapshot()
	wantEventTypes := []events.ApplicationEventType{
		events.Common.ApplicationLaunchedWithUrl,
		mobileEvents.pause,
		mobileEvents.resume,
	}
	gotEventTypes := make([]events.ApplicationEventType, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		gotEventTypes = append(gotEventTypes, subscription.eventType)
	}
	if !slices.Equal(gotEventTypes, wantEventTypes) {
		t.Fatalf("application event subscriptions = %v, want %v", gotEventTypes, wantEventTypes)
	}

	const cleanupCalls = 20
	var wg sync.WaitGroup
	wg.Add(cleanupCalls)
	for range cleanupCalls {
		go func() {
			defer wg.Done()
			cleanup()
		}()
	}
	wg.Wait()
	service.clearLifecycle()
	for _, subscription := range subscriptions {
		if got := subscription.unsubscribed.Load(); got != 1 {
			t.Errorf("event %d unsubscribe calls = %d, want 1", subscription.eventType, got)
		}
	}
}

func TestCleanupAndCallbacksAreSafeDuringConcurrentShutdown(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	subscriber := &recordingApplicationEventSubscriber{}
	mobileEvents := mobileLifecycleEventSet{
		pause:   events.Android.ActivityPaused,
		resume:  events.Android.ActivityResumed,
		enabled: true,
	}
	cleanup := service.subscribeApplicationEvents(runCtx, subscriber, mobileEvents)
	service.setLifecycleCleanup(runCtx, cleanup)
	service.startRefreshLoop(runCtx)
	pauseCallback := subscriber.callback(mobileEvents.pause)
	resumeCallback := subscriber.callback(mobileEvents.resume)
	if pauseCallback == nil || resumeCallback == nil {
		t.Fatal("mobile lifecycle callbacks were not registered")
	}

	const calls = 10
	start := make(chan struct{})
	cleared := make(chan context.Context, calls)
	var wg sync.WaitGroup
	wg.Add(calls*2 + 2)
	for range calls {
		go func() {
			defer wg.Done()
			<-start
			cleanup()
		}()
		go func() {
			defer wg.Done()
			<-start
			cleared <- service.clearLifecycle()
		}()
	}
	go func() {
		defer wg.Done()
		<-start
		pauseCallback(nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		resumeCallback(nil)
	}()
	close(start)
	wg.Wait()
	close(cleared)
	service.stopRefreshLoop(runCtx)

	clearedGenerations := 0
	for clearedCtx := range cleared {
		if clearedCtx == runCtx {
			clearedGenerations++
		} else if clearedCtx != nil {
			t.Fatalf("clearLifecycle returned unexpected generation")
		}
	}
	if clearedGenerations != 1 {
		t.Fatalf("successful lifecycle clears = %d, want 1", clearedGenerations)
	}
	for _, subscription := range subscriber.snapshot() {
		if got := subscription.unsubscribed.Load(); got != 1 {
			t.Errorf("event %d unsubscribe calls = %d, want 1", subscription.eventType, got)
		}
	}

	operations := refresh.recordedOperations()
	subscriber.emit(t, mobileEvents.pause, nil)
	subscriber.emit(t, mobileEvents.resume, nil)
	if got := refresh.recordedOperations(); !slices.Equal(got, operations) {
		t.Fatalf("post-shutdown callbacks changed refresh operations from %v to %v", operations, got)
	}
}

func TestStaleApplicationCallbacksCannotAffectNewLifecycle(t *testing.T) {
	refresh := &recordingRefreshController{}
	deliverer := &spyDeliverer{}
	service := &AuthService{refresh: refresh, deliver: deliverer}
	first := installTestLifecycle(t, service, context.Background())
	firstSubscriber := &recordingApplicationEventSubscriber{}
	mobileEvents := mobileLifecycleEventSet{
		pause:   events.Android.ActivityPaused,
		resume:  events.Android.ActivityResumed,
		enabled: true,
	}
	service.setLifecycleCleanup(
		first,
		service.subscribeApplicationEvents(first, firstSubscriber, mobileEvents),
	)
	service.startRefreshLoop(first)
	service.handleLifecycleLaunchURL(first, "https://app.example.com/callback?generation=first")

	cleared := service.clearLifecycle()
	service.stopRefreshLoop(cleared)
	firstSubscriber.emit(t, mobileEvents.resume, nil)
	firstSubscriber.emit(t, events.Common.ApplicationLaunchedWithUrl, nil)

	second := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(second)
	operationsBeforeStaleCallbacks := refresh.recordedOperations()
	firstSubscriber.emit(t, mobileEvents.pause, nil)
	firstSubscriber.emit(t, mobileEvents.resume, nil)
	service.handleLifecycleLaunchURL(first, "https://app.example.com/callback?generation=stale")

	if got := refresh.recordedOperations(); !slices.Equal(got, operationsBeforeStaleCallbacks) {
		t.Fatalf("stale callbacks changed refresh operations from %v to %v", operationsBeforeStaleCallbacks, got)
	}
	if got := deliverer.urls; !slices.Equal(got, []string{"https://app.example.com/callback?generation=first"}) {
		t.Fatalf("delivered URLs = %v, want only the active first-generation URL", got)
	}

	cleared = service.clearLifecycle()
	service.stopRefreshLoop(cleared)
}

func TestLifecycleLaunchURLDeliveryCanReenterService(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)
	delivered := make(chan string, 1)
	service.deliver = callbackDeliverer(func(url string) {
		service.Pause()
		delivered <- url
	})

	done := make(chan struct{})
	go func() {
		service.handleLifecycleLaunchURL(runCtx, "https://app.example.com/callback")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant lifecycle URL delivery deadlocked")
	}
	select {
	case got := <-delivered:
		if got != "https://app.example.com/callback" {
			t.Fatalf("delivered URL = %q", got)
		}
	default:
		t.Fatal("lifecycle URL was not delivered")
	}
	if got := refresh.recordedOperations(); !slices.Equal(got, []string{"start", "stop"}) {
		t.Fatalf("refresh operations = %v, want [start stop]", got)
	}

	cleared := service.clearLifecycle()
	service.stopRefreshLoop(cleared)
}

func TestDisabledMobileLifecycleDoesNotSubscribe(t *testing.T) {
	service := &AuthService{}
	runCtx := installTestLifecycle(t, service, context.Background())
	defer service.clearLifecycle()
	subscriber := &recordingApplicationEventSubscriber{}

	cleanup := service.subscribeApplicationEvents(runCtx, subscriber, mobileLifecycleEventSet{})
	if cleanup != nil {
		t.Fatal("disabled mobile lifecycle returned cleanup")
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("disabled mobile lifecycle subscriptions = %d, want 0", got)
	}
}
