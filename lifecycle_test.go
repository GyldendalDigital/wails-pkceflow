package wailspkceflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/oidctest"
	"github.com/wailsapp/wails/v3/pkg/events"
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
	operationsMu  sync.Mutex
	operations    []string
}

type countingTokenStore struct {
	loads atomic.Int32
}

type blockingStopRefreshController struct {
	starts      atomic.Int32
	stops       atomic.Int32
	stopEntered chan struct{}
	releaseStop chan struct{}
	stopOnce    sync.Once
}

func (*countingTokenStore) Save(pkceflow.TokenState) error { return nil }

func (s *countingTokenStore) Load() (pkceflow.TokenState, error) {
	s.loads.Add(1)
	return pkceflow.TokenState{}, nil
}

func (*countingTokenStore) Delete() error { return nil }

func (c *blockingStopRefreshController) StartRefreshLoop(context.Context) {
	c.starts.Add(1)
}

func (c *blockingStopRefreshController) StopRefreshLoop() {
	c.stops.Add(1)
	c.stopOnce.Do(func() { close(c.stopEntered) })
	<-c.releaseStop
}

func (c *recordingRefreshController) StartRefreshLoop(context.Context) {
	c.starts.Add(1)
	c.recordOperation("start")
}

func (c *recordingRefreshController) StopRefreshLoop() {
	c.stops.Add(1)
	c.recordOperation("stop")
}

func (c *recordingRefreshController) recordOperation(operation string) {
	active := c.active.Add(1)
	for {
		maxConcurrent := c.maxConcurrent.Load()
		if active <= maxConcurrent || c.maxConcurrent.CompareAndSwap(maxConcurrent, active) {
			break
		}
	}
	c.operationsMu.Lock()
	c.operations = append(c.operations, operation)
	c.operationsMu.Unlock()
	time.Sleep(2 * time.Millisecond)
	c.active.Add(-1)
}

func (c *recordingRefreshController) recordedOperations() []string {
	c.operationsMu.Lock()
	defer c.operationsMu.Unlock()
	return append([]string(nil), c.operations...)
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

func installTestLifecycle(t *testing.T, service *AuthService, parent context.Context) context.Context {
	t.Helper()
	runCtx, installed := service.installLifecycle(parent)
	if !installed {
		t.Fatal("installLifecycle did not install a new generation")
	}
	return runCtx
}

func TestAuthCommandsRejectOverlapAndUseServiceContext(t *testing.T) {
	service, flow := newBlockingService(t)
	installTestLifecycle(t, service, context.Background())

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

	installTestLifecycle(t, service, context.Background())
	if got := service.Logout(); !got.OK {
		t.Fatalf("Logout after lifecycle restart = %+v", got)
	}
	service.clearLifecycle()
}

func TestLifecycleInstallIsIdempotentAndClearCleansUpOnce(t *testing.T) {
	service := &AuthService{}
	var cleanupCalls atomic.Int32
	first := installTestLifecycle(t, service, context.Background())
	service.setLifecycleCleanup(first, func() {
		cleanupCalls.Add(1)
	})

	duplicate, installed := service.installLifecycle(context.Background())
	if installed {
		t.Fatal("duplicate lifecycle installation created a new generation")
	}
	if duplicate != first {
		t.Fatal("duplicate lifecycle installation returned a different context")
	}
	if got := cleanupCalls.Load(); got != 0 {
		t.Fatalf("cleanup calls before shutdown = %d, want 0", got)
	}

	var duplicateCleanupCalls atomic.Int32
	service.setLifecycleCleanup(first, func() {
		duplicateCleanupCalls.Add(1)
	})
	if got := duplicateCleanupCalls.Load(); got != 1 {
		t.Fatalf("duplicate cleanup calls = %d, want immediate cleanup", got)
	}

	cleared := service.clearLifecycle()
	if cleared != first {
		t.Fatal("clearLifecycle returned the wrong generation")
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("clearLifecycle did not cancel the active context")
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("cleanup calls = %d, want 1", got)
	}

	if cleared := service.clearLifecycle(); cleared != nil {
		t.Fatal("duplicate clearLifecycle returned a generation")
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("clearLifecycle called cleanup more than once: %d", got)
	}
}

func TestCancelledLifecycleIsReplaced(t *testing.T) {
	service := &AuthService{}
	parent, cancel := context.WithCancel(context.Background())
	first := installTestLifecycle(t, service, parent)
	var cleanupCalls atomic.Int32
	service.setLifecycleCleanup(first, func() {
		cleanupCalls.Add(1)
	})
	cancel()

	second := installTestLifecycle(t, service, context.Background())
	if second == first {
		t.Fatal("cancelled lifecycle was not replaced")
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("replaced lifecycle cleanup calls = %d, want 1", got)
	}
	service.clearLifecycle()
}

func TestServiceShutdownClearsLifecycle(t *testing.T) {
	service, _ := newBlockingService(t)
	var unsubscribed atomic.Int32
	runCtx := installTestLifecycle(t, service, context.Background())
	service.setLifecycleCleanup(runCtx, func() {
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
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("duplicate ServiceShutdown: %v", err)
	}
	if got := unsubscribed.Load(); got != 1 {
		t.Fatalf("duplicate shutdown cleanup calls = %d, want 1", got)
	}
}

func TestStartServiceIsIdempotentAndRestartable(t *testing.T) {
	store := &countingTokenStore{}
	flow := &blockingFlow{redirectURI: "https://app.example.com/auth/callback"}
	service, err := New(Options{
		Config: pkceflow.Config{
			IssuerURL: "https://idp.example.com",
			ClientID:  "test-app",
		},
		Flow:  flow,
		Store: store,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refresh := &recordingRefreshController{}
	service.refresh = refresh
	subscriber := &recordingApplicationEventSubscriber{}
	mobileEvents := mobileLifecycleEventSet{
		pause:   events.Android.ActivityPaused,
		resume:  events.Android.ActivityResumed,
		enabled: true,
	}
	var installedCalls atomic.Int32

	const startups = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(startups)
	for range startups {
		go func() {
			defer wg.Done()
			<-start
			err := service.startService(context.Background(), subscriber, mobileEvents, func() {
				installedCalls.Add(1)
			})
			if err != nil && !errors.Is(err, ErrServiceStartupInProgress) {
				t.Errorf("startService: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := installedCalls.Load(); got != 1 {
		t.Fatalf("installed callbacks = %d, want 1", got)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("session restore loads = %d, want 1", got)
	}
	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts = %d, want 1", got)
	}
	if got := len(subscriber.snapshot()); got != 2 {
		t.Fatalf("application subscriptions = %d, want 2", got)
	}

	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	for _, subscription := range subscriber.snapshot() {
		if got := subscription.unsubscribed.Load(); got != 1 {
			t.Fatalf("unsubscribe calls = %d, want 1", got)
		}
	}

	service.startService(context.Background(), subscriber, mobileEvents, func() {
		installedCalls.Add(1)
	})
	if got := installedCalls.Load(); got != 2 {
		t.Fatalf("installed callbacks after restart = %d, want 2", got)
	}
	if got := store.loads.Load(); got != 2 {
		t.Fatalf("session restore loads after restart = %d, want 2", got)
	}
	if got := refresh.starts.Load(); got != 2 {
		t.Fatalf("refresh starts after restart = %d, want 2", got)
	}
	if got := len(subscriber.snapshot()); got != 4 {
		t.Fatalf("application subscriptions after restart = %d, want 4", got)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("final ServiceShutdown: %v", err)
	}
}

func TestRefreshTransitionsAreIdempotent(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())

	service.startRefreshLoop(runCtx)
	service.startRefreshLoop(runCtx)
	service.Pause()
	service.Pause()
	service.Resume()
	service.Resume()

	if got := refresh.recordedOperations(); !equalStrings(got, []string{"start", "stop", "start"}) {
		t.Fatalf("refresh operations = %v, want [start stop start]", got)
	}

	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("duplicate ServiceShutdown: %v", err)
	}
	service.Resume()
	if got := refresh.recordedOperations(); !equalStrings(got, []string{"start", "stop", "start", "stop"}) {
		t.Fatalf("final refresh operations = %v, want [start stop start stop]", got)
	}
}

func TestPauseBeforeRefreshActivationDefersStartUntilResume(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	defer func() {
		cleared := service.clearLifecycle()
		service.stopRefreshLoop(cleared)
	}()

	service.Pause()
	service.startRefreshLoop(runCtx)
	if got := refresh.starts.Load(); got != 0 {
		t.Fatalf("refresh starts while paused = %d, want 0", got)
	}

	service.Resume()
	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts after resume = %d, want 1", got)
	}
}

func TestManualAndLifecyclePauseReasonsCompose(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)
	defer func() {
		cleared := service.clearLifecycle()
		service.stopRefreshLoop(cleared)
	}()

	service.Pause()
	service.pauseRefreshLoop(runCtx, refreshPauseLifecycle)
	service.resumeRefreshLoop(runCtx, refreshPauseLifecycle)
	if got := refresh.recordedOperations(); !equalStrings(got, []string{"start", "stop"}) {
		t.Fatalf("automatic foreground overrode manual pause: %v", got)
	}
	service.Resume()

	service.pauseRefreshLoop(runCtx, refreshPauseLifecycle)
	service.Pause()
	service.Resume()
	if got := refresh.recordedOperations(); !equalStrings(got, []string{"start", "stop", "start", "stop"}) {
		t.Fatalf("manual resume overrode lifecycle pause: %v", got)
	}
	service.resumeRefreshLoop(runCtx, refreshPauseLifecycle)

	want := []string{"start", "stop", "start", "stop", "start"}
	if got := refresh.recordedOperations(); !equalStrings(got, want) {
		t.Fatalf("composed refresh operations = %v, want %v", got, want)
	}
}

func TestLifecycleControlsAreNoOpsWithoutActiveService(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}

	service.Pause()
	service.Resume()
	if got := refresh.recordedOperations(); len(got) != 0 {
		t.Fatalf("pre-start refresh operations = %v, want none", got)
	}

	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	operationsAfterShutdown := refresh.recordedOperations()
	service.Pause()
	service.Resume()
	if got := refresh.recordedOperations(); !equalStrings(got, operationsAfterShutdown) {
		t.Fatalf("post-shutdown refresh operations changed from %v to %v", operationsAfterShutdown, got)
	}
}

func TestRefreshTransitionsAreSerialized(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)
	defer func() {
		cleared := service.clearLifecycle()
		service.stopRefreshLoop(cleared)
	}()

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
	operations := refresh.recordedOperations()
	for i := 1; i < len(operations); i++ {
		if operations[i] == operations[i-1] {
			t.Fatalf("non-idempotent refresh operations = %v", operations)
		}
	}
	if len(operations) > transitions+1 {
		t.Fatalf("refresh operations = %d, want at most %d", len(operations), transitions+1)
	}
}

func TestShutdownWaitsForInFlightPauseAndLeavesRefreshStopped(t *testing.T) {
	refresh := &blockingStopRefreshController{
		stopEntered: make(chan struct{}),
		releaseStop: make(chan struct{}),
	}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)

	pauseDone := make(chan struct{})
	go func() {
		service.Pause()
		close(pauseDone)
	}()
	select {
	case <-refresh.stopEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Pause did not enter the core stop operation")
	}

	shutdownDone := make(chan struct{})
	go func() {
		_ = service.ServiceShutdown()
		close(shutdownDone)
	}()
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not clear the lifecycle")
	}
	select {
	case <-shutdownDone:
		t.Fatal("shutdown completed while Pause still owned the refresh transition")
	default:
	}

	service.Resume()
	close(refresh.releaseStop)
	select {
	case <-pauseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Pause did not complete after releasing core stop")
	}
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not complete after Pause")
	}

	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts = %d, want 1", got)
	}
	if got := refresh.stops.Load(); got != 1 {
		t.Fatalf("refresh stops = %d, want 1", got)
	}
	service.refreshMu.Lock()
	state := service.refreshState
	service.refreshMu.Unlock()
	if state != refreshLoopStopped {
		t.Fatalf("refresh state after shutdown = %d, want stopped", state)
	}
}

func TestPostShutdownCallsDoNotEscapeLifecycle(t *testing.T) {
	refresh := &recordingRefreshController{}
	service := &AuthService{refresh: refresh}
	runCtx := installTestLifecycle(t, service, context.Background())
	service.startRefreshLoop(runCtx)

	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	service.Resume()

	if got := refresh.starts.Load(); got != 1 {
		t.Fatalf("refresh starts = %d, want startup only", got)
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

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
