package wailspkceflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
)

type blockingFlow struct {
	redirectURI string
	started     chan struct{}
	startOnce   sync.Once
}

type recordingRefreshController struct {
	active        atomic.Int32
	maxConcurrent atomic.Int32
	starts        atomic.Int32
	stops         atomic.Int32
}

func (c *recordingRefreshController) StartRefreshLoop(context.Context) {
	c.starts.Add(1)
	c.recordOperation()
}

func (c *recordingRefreshController) StopRefreshLoop() {
	c.stops.Add(1)
	c.recordOperation()
}

func (c *recordingRefreshController) recordOperation() {
	active := c.active.Add(1)
	for {
		maxConcurrent := c.maxConcurrent.Load()
		if active <= maxConcurrent || c.maxConcurrent.CompareAndSwap(maxConcurrent, active) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	c.active.Add(-1)
}

func (f *blockingFlow) RedirectURI() string {
	return f.redirectURI
}

func (f *blockingFlow) StartAuthFlow(ctx context.Context, _ string) (string, error) {
	f.startOnce.Do(func() { close(f.started) })
	<-ctx.Done()
	return "", ctx.Err()
}

func newBlockingService(t *testing.T) (*AuthService, *blockingFlow) {
	t.Helper()
	redirectURI := "https://app.example.com/auth/callback"
	idp := oidctest.NewFakeIDP(t,
		oidctest.WithClientID("test-app"),
		oidctest.WithRedirectURI(redirectURI),
	)
	flow := &blockingFlow{
		redirectURI: redirectURI,
		started:     make(chan struct{}),
	}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: idp.IssuerURL(),
			ClientID:  "test-app",
		},
		Flow: flow,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.client.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return service, flow
}

func TestAuthCommandsRejectOverlapAndUseServiceContext(t *testing.T) {
	service, flow := newBlockingService(t)
	service.installLifecycle(context.Background(), nil)

	loginResult := make(chan AuthResult, 1)
	go func() {
		loginResult <- service.Login()
	}()
	select {
	case <-flow.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Login to start")
	}

	if got := service.Logout(); got.OK || got.Code != CodeFlowInProgress {
		t.Fatalf("overlapping Logout = %+v, want flow_in_progress", got)
	}

	service.clearLifecycle()
	select {
	case got := <-loginResult:
		if got.OK || got.Code != CodeCancelled {
			t.Fatalf("cancelled Login = %+v, want cancelled", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service context cancellation did not release Login")
	}

	if got := service.Logout(); got.OK || got.Code != CodeCancelled {
		t.Fatalf("Logout after lifecycle shutdown = %+v, want cancelled", got)
	}

	service.installLifecycle(context.Background(), nil)
	if got := service.Logout(); !got.OK {
		t.Fatalf("Logout after lifecycle restart = %+v", got)
	}
	service.clearLifecycle()
}

func TestLifecycleReplacementAndClear(t *testing.T) {
	service := &AuthService{}
	var firstUnsubscribed atomic.Int32
	var secondUnsubscribed atomic.Int32

	first := service.installLifecycle(context.Background(), func() {
		firstUnsubscribed.Add(1)
	})
	second := service.installLifecycle(context.Background(), func() {
		secondUnsubscribed.Add(1)
	})

	select {
	case <-first.Done():
	default:
		t.Fatal("installing a new lifecycle did not cancel the previous context")
	}
	if got := firstUnsubscribed.Load(); got != 1 {
		t.Fatalf("first unsubscribe calls = %d, want 1", got)
	}
	var staleUnsubscribed atomic.Int32
	service.setLifecycleSubscription(first, func() {
		staleUnsubscribed.Add(1)
	})
	if got := staleUnsubscribed.Load(); got != 1 {
		t.Fatalf("stale unsubscribe calls = %d, want 1", got)
	}
	if err := second.Err(); err != nil {
		t.Fatalf("new lifecycle context is already cancelled: %v", err)
	}

	service.clearLifecycle()
	select {
	case <-second.Done():
	default:
		t.Fatal("clearLifecycle did not cancel the active context")
	}
	if got := secondUnsubscribed.Load(); got != 1 {
		t.Fatalf("second unsubscribe calls = %d, want 1", got)
	}

	service.clearLifecycle()
	if got := secondUnsubscribed.Load(); got != 1 {
		t.Fatalf("clearLifecycle called unsubscribe more than once: %d", got)
	}
}

func TestServiceShutdownClearsLifecycle(t *testing.T) {
	service, _ := newBlockingService(t)
	var unsubscribed atomic.Int32
	runCtx := service.installLifecycle(context.Background(), func() {
		unsubscribed.Add(1)
	})

	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("ServiceShutdown did not cancel the service context")
	}
	if got := unsubscribed.Load(); got != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", got)
	}
	if got := service.Login(); got.OK || got.Code != CodeCancelled {
		t.Fatalf("Login after ServiceShutdown = %+v, want cancelled", got)
	}
}

func TestRefreshTransitionsAreSerialized(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	service.installLifecycle(context.Background(), nil)
	defer service.clearLifecycle()

	const transitions = 40
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(transitions)
	for i := range transitions {
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				service.Resume()
			} else {
				service.Pause()
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := refresh.maxConcurrent.Load(); got != 1 {
		t.Fatalf("concurrent refresh transitions = %d, want 1", got)
	}
	if got := refresh.starts.Load(); got != transitions/2 {
		t.Fatalf("refresh starts = %d, want %d", got, transitions/2)
	}
	if got := refresh.stops.Load(); got != transitions/2 {
		t.Fatalf("refresh stops = %d, want %d", got, transitions/2)
	}
}

func TestPostShutdownCallsDoNotEscapeLifecycle(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	service.installLifecycle(context.Background(), nil)

	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	service.Resume()

	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts after shutdown = %d, want 0", got)
	}
	if got := refresh.stops.Load(); got != 1 {
		t.Fatalf("refresh stops = %d, want 1", got)
	}
	for _, command := range []struct {
		name string
		run  func() AuthResult
	}{
		{name: "Login", run: service.Login},
		{name: "Logout", run: service.Logout},
	} {
		t.Run(command.name, func(t *testing.T) {
			got := command.run()
			if got.OK || got.Code != CodeCancelled {
				t.Fatalf("%s after shutdown = %+v, want cancelled", command.name, got)
			}
		})
	}
}
