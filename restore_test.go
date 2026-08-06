package wailspkceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
)

type restoreBackendError struct {
	detail string
}

func (e *restoreBackendError) Error() string { return e.detail }

type saveFailureStore struct {
	restoreTestStore
	saveErr error
}

func (s *saveFailureStore) Save(pkceflow.TokenState) error { return s.saveErr }

type restoreTestStore struct {
	mu    sync.Mutex
	state pkceflow.TokenState
	err   error
	loads int
}

func (*restoreTestStore) Save(pkceflow.TokenState) error { return nil }

func (s *restoreTestStore) Load() (pkceflow.TokenState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.state, s.err
}

func (*restoreTestStore) Delete() error { return nil }

func (s *restoreTestStore) clearError() {
	s.mu.Lock()
	s.state = pkceflow.TokenState{}
	s.err = nil
	s.mu.Unlock()
}

func (s *restoreTestStore) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *restoreTestStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

type blockingRestoreStore struct {
	entered chan struct{}
	release chan struct{}
	cause   error
	once    sync.Once
}

func (*blockingRestoreStore) Save(pkceflow.TokenState) error { return nil }

func (s *blockingRestoreStore) Load() (pkceflow.TokenState, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return pkceflow.TokenState{}, s.cause
}

func (*blockingRestoreStore) Delete() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newRestoreTestService(
	t *testing.T,
	store pkceflow.TokenPersistence,
	configure func(*Options),
) (*AuthService, *recordingRefreshController) {
	t.Helper()

	opts := Options{
		Config: pkceflow.Config{
			IssuerURL: "https://idp.example.com",
			ClientID:  "test-app",
		},
		Flow: oidctest.NewFakeFlowHandler(
			nil,
			"https://app.example.com/auth/callback",
		),
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if configure != nil {
		configure(&opts)
	}
	service, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refresh := &recordingRefreshController{}
	service.refresh = refresh
	return service, refresh
}

func TestRestoreContractLiterals(t *testing.T) {
	statuses := []struct {
		status RestoreStatus
		want   string
	}{
		{RestoreStatusPending, "pending"},
		{RestoreStatusRestored, "restored"},
		{RestoreStatusNoSession, "no_session"},
		{RestoreStatusPersistenceUnavailable, "persistence_unavailable"},
	}
	for _, test := range statuses {
		if got := string(test.status); got != test.want {
			t.Errorf("RestoreStatus literal = %q, want %q", got, test.want)
		}
	}
	if EventRestorePersistenceUnavailable != "wailspkceflow:restore-persistence-unavailable" {
		t.Fatalf("restore event = %q", EventRestorePersistenceUnavailable)
	}
	payload, err := json.Marshal(RestoreStatusPersistenceUnavailable)
	if err != nil {
		t.Fatalf("marshal restore event payload: %v", err)
	}
	if got, want := string(payload), `"persistence_unavailable"`; got != want {
		t.Fatalf("serialized restore event payload = %s, want %s", got, want)
	}
}

func TestRestoreStatusTracksNormalOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		state      pkceflow.TokenState
		wantStatus RestoreStatus
	}{
		{
			name:       "no stored session",
			wantStatus: RestoreStatusNoSession,
		},
		{
			name: "restored session",
			state: pkceflow.TokenState{
				AccessToken: "stored-access-token",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
			wantStatus: RestoreStatusRestored,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &restoreTestStore{state: test.state}
			var callbackCalls atomic.Int32
			service, refresh := newRestoreTestService(t, store, func(opts *Options) {
				opts.OnRestoreError = func(error) { callbackCalls.Add(1) }
			})
			if got := service.RestoreStatus(); got != RestoreStatusPending {
				t.Fatalf("initial RestoreStatus = %q, want pending", got)
			}
			if got := service.Frontend().RestoreStatus(); got != RestoreStatusPending {
				t.Fatalf("initial frontend RestoreStatus = %q, want pending", got)
			}

			subscriber := &recordingApplicationEventSubscriber{}
			emitter := &oidctest.RecordingEmitter{}
			var installed atomic.Int32
			err := service.startService(
				context.Background(),
				subscriber,
				androidMobileLifecycleEvents(),
				func() {
					installed.Add(1)
					service.bus.SetTarget(emitter)
				},
			)
			if err != nil {
				t.Fatalf("startService: %v", err)
			}
			if got := service.RestoreStatus(); got != test.wantStatus {
				t.Fatalf("RestoreStatus = %q, want %q", got, test.wantStatus)
			}
			if got := service.Frontend().RestoreStatus(); got != test.wantStatus {
				t.Fatalf("frontend RestoreStatus = %q, want %q", got, test.wantStatus)
			}
			if got := store.loadCount(); got != 1 {
				t.Fatalf("Load calls = %d, want 1", got)
			}
			if got := installed.Load(); got != 1 {
				t.Fatalf("event target installs = %d, want 1", got)
			}
			if got := len(subscriber.snapshot()); got != 2 {
				t.Fatalf("application subscriptions = %d, want 2", got)
			}
			if got := refresh.starts.Load(); got != 1 {
				t.Fatalf("refresh starts = %d, want 1", got)
			}
			if got := emitter.Events(); len(got) != 0 {
				t.Fatalf("events = %+v, want none", got)
			}
			if got := callbackCalls.Load(); got != 0 {
				t.Fatalf("restore callback calls = %d, want 0", got)
			}
			if err := service.ServiceShutdown(); err != nil {
				t.Fatalf("ServiceShutdown: %v", err)
			}
		})
	}
}

func TestRestoreErrorContinueReportsSafeLatchedStatusOnce(t *testing.T) {
	const canary = "backend-secret-value"
	cause := &restoreBackendError{detail: canary}
	store := &restoreTestStore{err: cause}
	var logs bytes.Buffer
	var callbackCalls atomic.Int32
	var callbackErr error
	service, refresh := newRestoreTestService(t, store, func(opts *Options) {
		opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		opts.OnRestoreError = func(err error) {
			callbackCalls.Add(1)
			callbackErr = err
		}
	})
	subscriber := &recordingApplicationEventSubscriber{}
	emitter := &oidctest.RecordingEmitter{}
	var installed atomic.Int32
	start := func() error {
		return service.startService(
			context.Background(),
			subscriber,
			androidMobileLifecycleEvents(),
			func() {
				installed.Add(1)
				service.bus.SetTarget(emitter)
			},
		)
	}

	if err := start(); err != nil {
		t.Fatalf("startService in continue mode: %v", err)
	}
	if got := service.RestoreStatus(); got != RestoreStatusPersistenceUnavailable {
		t.Fatalf("RestoreStatus = %q, want persistence_unavailable", got)
	}
	if callbackCalls.Load() != 1 {
		t.Fatalf("restore callback calls = %d, want 1", callbackCalls.Load())
	}
	if !errors.Is(callbackErr, cause) {
		t.Fatalf("callback error does not wrap backend cause: %v", callbackErr)
	}
	var typedCause *restoreBackendError
	if !errors.As(callbackErr, &typedCause) || typedCause != cause {
		t.Fatalf("callback error does not preserve backend type: %v", callbackErr)
	}
	if got := logs.String(); !strings.Contains(got, "persisted session is unavailable") || strings.Contains(got, canary) {
		t.Fatalf("log output is missing the safe warning or contains backend text: %q", got)
	}
	events := emitter.Events()
	if len(events) != 1 || events[0].Name != EventRestorePersistenceUnavailable {
		t.Fatalf("events = %+v, want one %q event", events, EventRestorePersistenceUnavailable)
	}
	if got, ok := events[0].Data.(RestoreStatus); !ok || got != RestoreStatusPersistenceUnavailable {
		t.Fatalf("event payload = %#v, want persistence_unavailable RestoreStatus", events[0].Data)
	}
	if strings.Contains(fmt.Sprint(events[0].Data), canary) {
		t.Fatal("frontend event payload contains backend error text")
	}

	if err := start(); err != nil {
		t.Fatalf("duplicate startService: %v", err)
	}
	if got := store.loadCount(); got != 1 {
		t.Fatalf("Load calls after duplicate startup = %d, want 1", got)
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("callback calls after duplicate startup = %d, want 1", got)
	}
	if got := len(emitter.Events()); got != 1 {
		t.Fatalf("events after duplicate startup = %d, want 1", got)
	}
	if got := installed.Load(); got != 1 {
		t.Fatalf("event target installs = %d, want 1", got)
	}
	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts = %d, want 1", got)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
}

func TestRestoreErrorStrictHasNoStartupSideEffectsAndCanRetry(t *testing.T) {
	const canary = "native-store-secret"
	cause := &restoreBackendError{detail: canary}
	store := &restoreTestStore{err: cause}
	var service *AuthService
	var callbackCalls atomic.Int32
	var callbackErr error
	var callbackReentryTimedOut atomic.Bool
	service, refresh := newRestoreTestService(t, store, func(opts *Options) {
		opts.RestoreErrorPolicy = RestoreErrorFailStartup
		opts.OnRestoreError = func(err error) {
			callbackCalls.Add(1)
			callbackErr = err
			done := make(chan error, 1)
			go func() { done <- service.ServiceShutdown() }()
			select {
			case shutdownErr := <-done:
				if shutdownErr != nil {
					t.Errorf("callback reentrant ServiceShutdown: %v", shutdownErr)
				}
			case <-time.After(time.Second):
				callbackReentryTimedOut.Store(true)
			}
			if err := service.startService(
				context.Background(),
				nil,
				mobileLifecycleEventSet{},
				nil,
			); !errors.Is(err, ErrServiceStartupInProgress) {
				t.Errorf("callback reentrant startService = %v, want ErrServiceStartupInProgress", err)
			}
		}
	})
	subscriber := &recordingApplicationEventSubscriber{}
	emitter := &oidctest.RecordingEmitter{}
	var installed atomic.Int32
	start := func() error {
		return service.startService(
			context.Background(),
			subscriber,
			androidMobileLifecycleEvents(),
			func() {
				installed.Add(1)
				service.bus.SetTarget(emitter)
			},
		)
	}

	err := start()
	if err == nil {
		t.Fatal("strict startService returned nil error")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("strict startup error contains backend text: %q", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("strict startup error does not wrap backend cause: %v", err)
	}
	var returnedCause *restoreBackendError
	if !errors.As(err, &returnedCause) || returnedCause != cause {
		t.Fatalf("strict startup error does not preserve backend type: %v", err)
	}
	if callbackCalls.Load() != 1 || !errors.Is(callbackErr, cause) {
		t.Fatalf("restore callback = (%d, %v), want one wrapped cause", callbackCalls.Load(), callbackErr)
	}
	if callbackReentryTimedOut.Load() {
		t.Fatal("restore callback ran while the service lifecycle lock was held")
	}
	if got := service.RestoreStatus(); got != RestoreStatusPersistenceUnavailable {
		t.Fatalf("RestoreStatus = %q, want persistence_unavailable", got)
	}
	if got := installed.Load(); got != 0 {
		t.Fatalf("event target installs after strict failure = %d, want 0", got)
	}
	if got := len(subscriber.snapshot()); got != 0 {
		t.Fatalf("subscriptions after strict failure = %d, want 0", got)
	}
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts after strict failure = %d, want 0", got)
	}
	if got := len(emitter.Events()); got != 0 {
		t.Fatalf("events after strict failure = %d, want 0", got)
	}
	if got := store.loadCount(); got != 1 {
		t.Fatalf("Load calls after callback re-entry = %d, want 1", got)
	}

	store.clearError()
	if err := start(); err != nil {
		t.Fatalf("retry startService: %v", err)
	}
	if got := service.RestoreStatus(); got != RestoreStatusNoSession {
		t.Fatalf("RestoreStatus after retry = %q, want no_session", got)
	}
	if got := store.loadCount(); got != 2 {
		t.Fatalf("Load calls after retry = %d, want 2", got)
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("callback calls after retry = %d, want 1", got)
	}
	if got := installed.Load(); got != 1 {
		t.Fatalf("event target installs after retry = %d, want 1", got)
	}
	if got := len(subscriber.snapshot()); got != 2 {
		t.Fatalf("subscriptions after retry = %d, want 2", got)
	}
	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts after retry = %d, want 1", got)
	}
	if got := len(emitter.Events()); got != 0 {
		t.Fatalf("stale events flushed after strict retry = %d, want 0", got)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
}

func TestStrictRestoreFailureDoesNotStartAutoInit(t *testing.T) {
	requests := make(chan struct{}, 1)
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests <- struct{}{}
		return nil, errors.New("unexpected discovery request")
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })

	cause := errors.New("backend unavailable")
	store := &restoreTestStore{err: cause}
	service, _ := newRestoreTestService(t, store, func(opts *Options) {
		opts.AutoInit = true
		opts.RestoreErrorPolicy = RestoreErrorFailStartup
	})
	if err := service.startService(
		context.Background(),
		&recordingApplicationEventSubscriber{},
		androidMobileLifecycleEvents(),
		func() { t.Error("strict failure installed the event target") },
	); err == nil {
		t.Fatal("strict startService returned nil error")
	}

	select {
	case <-requests:
		t.Fatal("strict restore failure started background OIDC discovery")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStrictRestoreFailureLeavesExistingInMemorySessionUnchanged(t *testing.T) {
	redirectURI := "https://app.example.com/auth/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)
	store := &restoreTestStore{}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: idp.IssuerURL(),
			ClientID:  "test-app",
		},
		Flow:               oidctest.NewFakeFlowHandler(idp, redirectURI),
		Store:              store,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RestoreErrorPolicy: RestoreErrorFailStartup,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result := service.Login(); !result.OK {
		t.Fatalf("Login: %+v", result)
	}
	if !service.IsAuthenticated() {
		t.Fatal("session is not usable before restore failure")
	}

	cause := errors.New("persistence unavailable")
	store.fail(cause)
	if err := service.startService(
		context.Background(),
		nil,
		mobileLifecycleEventSet{},
		func() { t.Error("strict failure installed the event target") },
	); !errors.Is(err, cause) {
		t.Fatalf("startService error = %v, want wrapped persistence cause", err)
	}
	if !service.IsAuthenticated() {
		t.Fatal("strict restore failure cleared the existing in-memory session")
	}
	if got := service.RestoreStatus(); got != RestoreStatusPersistenceUnavailable {
		t.Fatalf("RestoreStatus = %q, want persistence_unavailable", got)
	}
}

func TestStrictRestoreRetainsAuthoritativeDirtyInMemoryGeneration(t *testing.T) {
	redirectURI := "https://app.example.com/auth/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)
	store := &saveFailureStore{saveErr: errors.New("save unavailable")}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: idp.IssuerURL(),
			ClientID:  "test-app",
		},
		Flow:               oidctest.NewFakeFlowHandler(idp, redirectURI),
		Store:              store,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RestoreErrorPolicy: RestoreErrorFailStartup,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result := service.Login(); !result.OK {
		t.Fatalf("Login: %+v", result)
	}
	refresh := &recordingRefreshController{}
	service.refresh = refresh
	var installed atomic.Int32
	if err := service.startService(
		context.Background(),
		nil,
		mobileLifecycleEventSet{},
		func() { installed.Add(1) },
	); err != nil {
		t.Fatalf("startService: %v", err)
	}
	if got := store.loadCount(); got != 0 {
		t.Fatalf("Load calls = %d, want 0 for authoritative dirty memory", got)
	}
	if got := service.RestoreStatus(); got != RestoreStatusRestored {
		t.Fatalf("RestoreStatus = %q, want restored", got)
	}
	if !service.IsAuthenticated() {
		t.Fatal("authoritative in-memory session is not usable")
	}
	if got := installed.Load(); got != 1 {
		t.Fatalf("event target installs = %d, want 1", got)
	}
	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts = %d, want 1", got)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
}

func TestRestoreStatusIsPendingWhileLoadIsInProgress(t *testing.T) {
	store := &blockingRestoreStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		cause:   errors.New("storage unavailable"),
	}
	service, _ := newRestoreTestService(t, store, nil)
	result := make(chan error, 1)
	go func() {
		result <- service.startService(context.Background(), nil, mobileLifecycleEventSet{}, nil)
	}()

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence Load")
	}
	if got := service.RestoreStatus(); got != RestoreStatusPending {
		t.Fatalf("RestoreStatus during Load = %q, want pending", got)
	}

	const readers = 32
	var wg sync.WaitGroup
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			for range 100 {
				_ = service.Frontend().RestoreStatus()
			}
		}()
	}
	close(store.release)
	wg.Wait()
	if err := <-result; err != nil {
		t.Fatalf("startService in continue mode: %v", err)
	}
	if got := service.RestoreStatus(); got != RestoreStatusPersistenceUnavailable {
		t.Fatalf("RestoreStatus after Load = %q, want persistence_unavailable", got)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
}

func TestNewRejectsUnknownRestoreErrorPolicy(t *testing.T) {
	_, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: "https://idp.example.com",
			ClientID:  "test-app",
		},
		Flow: oidctest.NewFakeFlowHandler(
			nil,
			"https://app.example.com/auth/callback",
		),
		RestoreErrorPolicy: RestoreErrorPolicy(255),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported restore error policy") {
		t.Fatalf("New error = %v, want unsupported restore policy error", err)
	}
}
