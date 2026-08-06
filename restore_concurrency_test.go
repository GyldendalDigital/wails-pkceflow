package wailspkceflow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type callbackRestoreStore struct {
	load func() (pkceflow.TokenState, error)
}

func (*callbackRestoreStore) Save(pkceflow.TokenState) error { return nil }

func (s *callbackRestoreStore) Load() (pkceflow.TokenState, error) {
	return s.load()
}

func (*callbackRestoreStore) Delete() error { return nil }

type callbackEmitter struct {
	emit func(string, any)
}

func (e callbackEmitter) Emit(event string, data any) {
	e.emit(event, data)
}

type callbackWriter struct {
	once     sync.Once
	callback func()
}

type shutdownOnSubscribe struct {
	service       *AuthService
	once          sync.Once
	registrations atomic.Int32
	unsubscribes  atomic.Int32
}

func (s *shutdownOnSubscribe) OnApplicationEvent(
	_ events.ApplicationEventType,
	_ func(*application.ApplicationEvent),
) func() {
	s.registrations.Add(1)
	s.once.Do(func() {
		if err := s.service.ServiceShutdown(); err != nil {
			panic(err)
		}
	})
	return func() { s.unsubscribes.Add(1) }
}

func (w *callbackWriter) Write(data []byte) (int, error) {
	w.once.Do(w.callback)
	return len(data), nil
}

func TestCancelledStartupContextHasNoSideEffects(t *testing.T) {
	store := &restoreTestStore{}
	service, refresh := newRestoreTestService(t, store, nil)
	subscriber := &recordingApplicationEventSubscriber{}
	var installed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := service.startService(
		ctx,
		subscriber,
		androidMobileLifecycleEvents(),
		func() { installed.Add(1) },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startService error = %v, want context.Canceled", err)
	}
	if got := store.loadCount(); got != 0 {
		t.Fatalf("Load calls = %d, want 0", got)
	}
	if got := service.RestoreStatus(); got != RestoreStatusPending {
		t.Fatalf("RestoreStatus = %q, want pending", got)
	}
	if got := installed.Load(); got != 0 {
		t.Fatalf("event target installs = %d, want 0", got)
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("subscriptions = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts = %d, want 0", got)
	}
}

func TestOverlappingStartupReturnsInProgress(t *testing.T) {
	store := &blockingRestoreStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, _ := newRestoreTestService(t, store, nil)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- service.startService(
			context.Background(),
			nil,
			mobileLifecycleEventSet{},
			nil,
		)
	}()

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence Load")
	}
	if err := service.startService(
		context.Background(),
		nil,
		mobileLifecycleEventSet{},
		nil,
	); !errors.Is(err, ErrServiceStartupInProgress) {
		t.Fatalf("overlapping startService = %v, want ErrServiceStartupInProgress", err)
	}
	close(store.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first startService: %v", err)
	}
	if err := service.startService(
		context.Background(),
		nil,
		mobileLifecycleEventSet{},
		nil,
	); err != nil {
		t.Fatalf("active duplicate startService: %v", err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
}

func TestCancellationDuringRestoreHasNoLifecycleSideEffects(t *testing.T) {
	store := &blockingRestoreStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, refresh := newRestoreTestService(t, store, nil)
	subscriber := &recordingApplicationEventSubscriber{}
	var installed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- service.startService(
			ctx,
			subscriber,
			androidMobileLifecycleEvents(),
			func() { installed.Add(1) },
		)
	}()

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence Load")
	}
	cancel()
	close(store.release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("startService error = %v, want context.Canceled", err)
	}
	if got := service.RestoreStatus(); got != RestoreStatusNoSession {
		t.Fatalf("RestoreStatus = %q, want no_session", got)
	}
	if got := installed.Load(); got != 0 {
		t.Fatalf("event target installs = %d, want 0", got)
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("subscriptions = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts = %d, want 0", got)
	}
	if _, active := service.activeLifecycleContext(); active {
		t.Fatal("cancelled startup left an active lifecycle generation")
	}
}

func TestPersistenceLoadCanReenterServiceShutdown(t *testing.T) {
	store := &callbackRestoreStore{}
	var service *AuthService
	var refresh *recordingRefreshController
	var shutdownErr error
	store.load = func() (pkceflow.TokenState, error) {
		shutdownErr = service.ServiceShutdown()
		return pkceflow.TokenState{}, nil
	}
	service, refresh = newRestoreTestService(t, store, nil)
	subscriber := &recordingApplicationEventSubscriber{}
	var installed atomic.Int32

	if err := service.startService(
		context.Background(),
		subscriber,
		androidMobileLifecycleEvents(),
		func() { installed.Add(1) },
	); err != nil {
		t.Fatalf("startService: %v", err)
	}
	if shutdownErr != nil {
		t.Fatalf("reentrant ServiceShutdown: %v", shutdownErr)
	}
	if got := service.RestoreStatus(); got != RestoreStatusNoSession {
		t.Fatalf("RestoreStatus = %q, want no_session", got)
	}
	if got := installed.Load(); got != 0 {
		t.Fatalf("event target installs = %d, want 0", got)
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("subscriptions = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts = %d, want 0", got)
	}
}

func TestBufferedEventTargetCanReenterServiceShutdown(t *testing.T) {
	service, refresh := newRestoreTestService(t, &restoreTestStore{}, nil)
	service.bus.Emit("test:buffered", nil)
	subscriber := &recordingApplicationEventSubscriber{}
	var emitterCalls atomic.Int32
	var reentryTimedOut atomic.Bool
	target := callbackEmitter{emit: func(string, any) {
		emitterCalls.Add(1)
		done := make(chan error, 1)
		go func() { done <- service.ServiceShutdown() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("reentrant ServiceShutdown: %v", err)
			}
		case <-time.After(time.Second):
			reentryTimedOut.Store(true)
		}
	}}

	if err := service.startService(
		context.Background(),
		subscriber,
		androidMobileLifecycleEvents(),
		func() { service.bus.SetTarget(target) },
	); err != nil {
		t.Fatalf("startService: %v", err)
	}
	if reentryTimedOut.Load() {
		t.Fatal("event target ran while the service lifecycle lock was held")
	}
	if got := emitterCalls.Load(); got != 1 {
		t.Fatalf("emitter calls = %d, want 1", got)
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("subscriptions after reentrant shutdown = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts after reentrant shutdown = %d, want 0", got)
	}
}

func TestLoggerShutdownSuppressesStaleRestoreEventAndRefresh(t *testing.T) {
	cause := errors.New("storage unavailable")
	store := &restoreTestStore{err: cause}
	writer := &callbackWriter{}
	var service *AuthService
	service, refresh := newRestoreTestService(t, store, func(opts *Options) {
		opts.Logger = slog.New(slog.NewTextHandler(writer, nil))
	})
	writer.callback = func() {
		if err := service.ServiceShutdown(); err != nil {
			t.Errorf("logger ServiceShutdown: %v", err)
		}
	}
	subscriber := &recordingApplicationEventSubscriber{}
	emitter := &oidctest.RecordingEmitter{}

	if err := service.startService(
		context.Background(),
		subscriber,
		androidMobileLifecycleEvents(),
		func() { service.bus.SetTarget(emitter) },
	); err != nil {
		t.Fatalf("startService: %v", err)
	}
	if got := len(emitter.Events()); got != 0 {
		t.Fatalf("events after logger-triggered shutdown = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts after logger-triggered shutdown = %d, want 0", got)
	}
	for _, subscription := range subscriber.snapshot() {
		if got := subscription.unsubscribed.Load(); got != 1 {
			t.Fatalf("unsubscribe calls = %d, want 1", got)
		}
	}
}

func TestShutdownDuringSubscriptionRemovesEveryLateRegistration(t *testing.T) {
	service, refresh := newRestoreTestService(t, &restoreTestStore{}, nil)
	subscriber := &shutdownOnSubscribe{service: service}

	if err := service.startService(
		context.Background(),
		subscriber,
		androidMobileLifecycleEvents(),
		nil,
	); err != nil {
		t.Fatalf("startService: %v", err)
	}
	if got := subscriber.registrations.Load(); got != 2 {
		t.Fatalf("registrations = %d, want 2", got)
	}
	if got := subscriber.unsubscribes.Load(); got != 2 {
		t.Fatalf("unsubscribes = %d, want 2", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts = %d, want 0", got)
	}
	if _, active := service.activeLifecycleContext(); active {
		t.Fatal("subscription-time shutdown left an active lifecycle")
	}
}

var _ io.Writer = (*callbackWriter)(nil)
