package wailspkceflow

import (
	"context"
	"log/slog"
	"sync"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/eventbus"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// URLDeliverer receives a URL already surfaced by the Wails host. AuthService
// forwards ApplicationLaunchedWithUrl events; it does not capture native
// Android or iOS callbacks. The interface is implemented by *mobileflow.Handler
// through its DeliverURL method. Implementations must be non-blocking and safe
// for concurrent calls.
type URLDeliverer interface {
	DeliverURL(url string)
}

type refreshLoopController interface {
	StartRefreshLoop(context.Context)
	StopRefreshLoop()
}

type refreshLoopState uint8
type refreshPauseReason uint8

const (
	refreshLoopStopped refreshLoopState = iota
	refreshLoopRunning
	refreshLoopPaused
)

const (
	refreshPauseManual refreshPauseReason = 1 << iota
	refreshPauseLifecycle
)

// Options configures the AuthService facade. Config and Flow are required; the
// rest are optional. The facade owns the event bus and wires it to Wails, so
// there is no separate emitter for the caller to construct.
type Options struct {
	// Config is the OIDC client configuration. Required.
	Config pkceflow.Config

	// Flow is the platform auth flow handler (for example desktopflow.Handler
	// or mobileflow.Handler). Required.
	Flow pkceflow.AuthFlowHandler

	// Store persists tokens across restarts. Optional; defaults to in-memory
	// storage (tokens lost on restart) when nil.
	Store pkceflow.TokenPersistence

	// Logger is the structured logger for the client. Optional; defaults to
	// slog.Default() when nil.
	Logger *slog.Logger

	// AutoInit runs client.Init (OIDC discovery) in a background goroutine on
	// ServiceStartup. Discovery failure is non-fatal and surfaces as the
	// oidcauth:init-failed event; the app can run offline with cached tokens.
	AutoInit bool

	// DeepLinkDelivery, when set, routes the Wails ApplicationLaunchedWithUrl
	// event to the given deliverer. When nil, New uses Flow itself if it
	// implements URLDeliverer. Set this only to explicitly override delivery.
	DeepLinkDelivery URLDeliverer
}

// AuthService adapts a pkceflow.Client to Wails v3. Construct it with New, keep
// Client for Go-side API calls, and register Frontend() with Wails. Registering
// AuthService itself would expose backend-only methods to generated bindings.
type AuthService struct {
	client   *pkceflow.Client
	bus      *eventbus.DeferredEventBus
	deliver  URLDeliverer
	autoInit bool
	logger   *slog.Logger

	serviceMu    sync.Mutex // serializes Wails startup and shutdown
	refresh      refreshLoopController
	refreshMu    sync.Mutex // serializes the core loop's separate stop/start steps
	refreshState refreshLoopState
	refreshCtx   context.Context
	pauseReasons refreshPauseReason

	frontendOnce sync.Once
	frontend     *FrontendService

	mu               sync.Mutex
	runCtx           context.Context
	runCancel        context.CancelFunc
	lifecycleCleanup func()
	lifecycleStarted bool
	lifecycleActive  bool
	commandActive    bool
}

// New builds the underlying pkceflow.Client, wiring an internal deferred event
// bus as its emitter, and returns the Wails service. It returns the same error
// as pkceflow.New when the configuration is invalid.
//
//nolint:gocritic // hugeParam: Options is intentionally passed by value (contains Config)
func New(opts Options) (*AuthService, error) {
	bus := &eventbus.DeferredEventBus{}

	coreOpts := []pkceflow.Option{pkceflow.WithEventEmitter(bus)}
	if opts.Store != nil {
		coreOpts = append(coreOpts, pkceflow.WithTokenPersistence(opts.Store))
	}
	if opts.Logger != nil {
		coreOpts = append(coreOpts, pkceflow.WithLogger(opts.Logger))
	}

	client, err := pkceflow.New(opts.Config, opts.Flow, coreOpts...)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	deliver := opts.DeepLinkDelivery
	if deliver == nil {
		deliver, _ = opts.Flow.(URLDeliverer)
	}

	return &AuthService{
		client:   client,
		bus:      bus,
		deliver:  deliver,
		autoInit: opts.AutoInit,
		logger:   logger,
		refresh:  client,
	}, nil
}

// Client returns the underlying pkceflow.Client. Use it in the app's API layer,
// for example client.TokenFn(ctx) to inject Bearer tokens into HTTP requests.
// While the Wails service is active, use AuthService Pause and Resume rather
// than calling the client's refresh-loop controls directly.
func (s *AuthService) Client() *pkceflow.Client {
	return s.client
}

// ServiceName implements the Wails ServiceName interface.
func (s *AuthService) ServiceName() string {
	return "pkceflow.AuthService"
}

// ServiceStartup implements the Wails ServiceStartup interface. It wires auth
// events to the Wails application, restores any cached session, starts the
// background refresh loop, subscribes to mobile lifecycle events and (when
// configured) deep-link launch events, and runs OIDC discovery.
func (s *AuthService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	app := application.Get()
	s.startService(ctx, app.Event, platformMobileLifecycleEvents(), func() {
		s.bus.SetTarget(&appEmitter{app: app})
	})
	return nil
}

func (s *AuthService) startService(
	ctx context.Context,
	subscriber applicationEventSubscriber,
	mobileEvents mobileLifecycleEventSet,
	onInstalled func(),
) {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()

	runCtx, installed := s.installLifecycle(ctx)
	if !installed {
		return
	}

	if onInstalled != nil {
		onInstalled()
	}

	cleanup := s.subscribeApplicationEvents(runCtx, subscriber, mobileEvents)
	s.setLifecycleCleanup(runCtx, cleanup)
	if !s.isCurrentLifecycle(runCtx) {
		return
	}

	s.client.RestoreSession()
	s.startRefreshLoop(runCtx)

	if s.autoInit && s.isCurrentLifecycle(runCtx) {
		go func() {
			if err := s.client.Init(runCtx); err != nil {
				if runCtx.Err() == nil {
					s.logger.Warn("pkceflow: background Init failed", "error", err)
				}
			}
		}()
	}
}

// ServiceShutdown implements the Wails ServiceShutdown interface. It stops the
// background refresh loop, cancels pending auth commands, and removes the
// launch-URL and mobile lifecycle subscriptions.
func (s *AuthService) ServiceShutdown() error {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()

	runCtx := s.clearLifecycle()
	s.stopRefreshLoop(runCtx)
	return nil
}

// Login runs the OIDC Authorization Code + PKCE flow. FrontendService exposes
// it to the frontend as a structured AuthResult (never a raw error or token).
// The login timeout comes from the client Config.
func (s *AuthService) Login() AuthResult {
	ctx, rejected, ok := s.beginCommand()
	if !ok {
		return rejected
	}
	defer s.endCommand()
	return newResult(s.client.Login(ctx))
}

// Logout clears in-memory state, attempts persistent deletion, and, when
// supported, performs RP-Initiated Logout. Persistence failures and
// non-cancellation browser logout failures are logged by the core client rather
// than returned; cancellation of the best-effort browser round trip is silent.
// FrontendService exposes this as a structured AuthResult, and the logout
// timeout comes from the client Config.
func (s *AuthService) Logout() AuthResult {
	ctx, rejected, ok := s.beginCommand()
	if !ok {
		return rejected
	}
	defer s.endCommand()
	return newResult(s.client.Logout(ctx))
}

// AuthStatus reports the current authentication state. It makes no network
// calls and is safe to call from the frontend at any time.
func (s *AuthService) AuthStatus() pkceflow.AuthStatusResult {
	return s.client.AuthStatus()
}

// IsAuthenticated reports whether the user currently has a usable session
// (valid token or within the grace period).
func (s *AuthService) IsAuthenticated() bool {
	return s.client.AuthStatus().CanUseApp
}

// Claims returns the decoded ID token claims for the current session. The
// second return value reports success; on failure (for example no active
// session) the AuthResult carries a code and the ClaimsDTO is zero.
func (s *AuthService) Claims() (ClaimsDTO, AuthResult) {
	claims, err := s.client.Claims()
	if err != nil {
		return ClaimsDTO{}, newResult(err)
	}
	return newClaimsDTO(&claims), AuthResult{OK: true}
}

// Pause stops the background token refresh loop until Resume is called. This
// manual pause remains in effect across automatic mobile foreground events.
func (s *AuthService) Pause() {
	runCtx, ok := s.activeLifecycleContext()
	if !ok {
		return
	}
	s.pauseRefreshLoop(runCtx, refreshPauseManual)
}

// Resume releases the manual pause set by Pause. Refresh work restarts only when
// the mobile lifecycle is also in the foreground. The loop continues the
// current session's refresh schedule instead of forcing a refresh; an
// already-due threshold may still run immediately.
func (s *AuthService) Resume() {
	runCtx, ok := s.activeLifecycleContext()
	if !ok {
		return
	}
	s.resumeRefreshLoop(runCtx, refreshPauseManual)
}

// handleLaunchURL routes a deep-link callback URL to the configured deliverer.
// It is separated from the event subscription so it can be unit tested without
// a running Wails application.
func (s *AuthService) handleLaunchURL(url string) {
	if s.deliver != nil {
		s.deliver.DeliverURL(url)
	}
}

func (s *AuthService) installLifecycle(parent context.Context) (context.Context, bool) {
	if parent == nil {
		parent = context.Background()
	}

	s.mu.Lock()
	if s.lifecycleActive && s.runCtx != nil && s.runCtx.Err() == nil {
		runCtx := s.runCtx
		s.mu.Unlock()
		return runCtx, false
	}

	runCtx, cancel := context.WithCancel(parent)
	oldCancel := s.runCancel
	oldCleanup := s.lifecycleCleanup
	s.runCtx = runCtx
	s.runCancel = cancel
	s.lifecycleCleanup = nil
	s.lifecycleStarted = true
	s.lifecycleActive = true
	s.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if oldCleanup != nil {
		oldCleanup()
	}
	return runCtx, true
}

func (s *AuthService) clearLifecycle() context.Context {
	s.mu.Lock()
	if !s.lifecycleActive {
		s.lifecycleStarted = true
		s.mu.Unlock()
		return nil
	}

	runCtx := s.runCtx
	cancel := s.runCancel
	cleanup := s.lifecycleCleanup
	s.runCtx = nil
	s.runCancel = nil
	s.lifecycleCleanup = nil
	s.lifecycleStarted = true
	s.lifecycleActive = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cleanup != nil {
		cleanup()
	}
	return runCtx
}

func (s *AuthService) setLifecycleCleanup(runCtx context.Context, cleanup func()) {
	if cleanup == nil {
		return
	}

	s.mu.Lock()
	if s.lifecycleActive && s.runCtx == runCtx && s.lifecycleCleanup == nil {
		s.lifecycleCleanup = cleanup
		cleanup = nil
	}
	s.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}
}

func (s *AuthService) isCurrentLifecycle(runCtx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runCtx != nil && runCtx.Err() == nil && s.lifecycleActive && s.runCtx == runCtx
}

func (s *AuthService) activeLifecycleContext() (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.lifecycleActive || s.runCtx == nil || s.runCtx.Err() != nil {
		return nil, false
	}
	return s.runCtx, true
}

func (s *AuthService) startRefreshLoop(runCtx context.Context) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	if s.refresh == nil || !s.isCurrentLifecycle(runCtx) {
		return
	}
	if s.refreshCtx == runCtx {
		if s.refreshState == refreshLoopRunning || s.refreshState == refreshLoopPaused {
			return
		}
	}
	if s.refreshState == refreshLoopRunning {
		s.refresh.StopRefreshLoop()
	}

	s.pauseReasons = 0
	s.refresh.StartRefreshLoop(runCtx)
	s.refreshState = refreshLoopRunning
	s.refreshCtx = runCtx
}

func (s *AuthService) pauseRefreshLoop(runCtx context.Context, reason refreshPauseReason) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	if s.refresh == nil || !s.isCurrentLifecycle(runCtx) {
		return
	}
	if s.refreshCtx != runCtx {
		if s.refreshState == refreshLoopRunning {
			s.refresh.StopRefreshLoop()
		}
		s.refreshState = refreshLoopStopped
		s.refreshCtx = nil
		s.pauseReasons = 0
	}
	if s.pauseReasons&reason != 0 {
		return
	}
	if s.refreshState == refreshLoopRunning {
		s.refresh.StopRefreshLoop()
	}
	s.pauseReasons |= reason
	s.refreshState = refreshLoopPaused
	s.refreshCtx = runCtx
}

func (s *AuthService) resumeRefreshLoop(runCtx context.Context, reason refreshPauseReason) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	if s.refresh == nil || !s.isCurrentLifecycle(runCtx) ||
		s.refreshState != refreshLoopPaused || s.refreshCtx != runCtx || s.pauseReasons&reason == 0 {
		return
	}
	s.pauseReasons &^= reason
	if s.pauseReasons != 0 {
		return
	}
	s.refresh.StartRefreshLoop(runCtx)
	s.refreshState = refreshLoopRunning
}

func (s *AuthService) stopRefreshLoop(runCtx context.Context) {
	if runCtx == nil {
		return
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	if s.refresh == nil || s.refreshState == refreshLoopStopped || s.refreshCtx != runCtx {
		return
	}
	if s.refreshState == refreshLoopRunning {
		s.refresh.StopRefreshLoop()
	}
	s.refreshState = refreshLoopStopped
	s.refreshCtx = nil
	s.pauseReasons = 0
}

func (s *AuthService) beginCommand() (context.Context, AuthResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.commandActive {
		return nil, flowInProgressResult(), false
	}
	if s.lifecycleStarted && !s.lifecycleActive {
		return nil, serviceStoppedResult(), false
	}
	ctx := s.runCtx
	if ctx != nil && ctx.Err() != nil {
		return nil, serviceStoppedResult(), false
	}
	s.commandActive = true
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, AuthResult{}, true
}

func (s *AuthService) endCommand() {
	s.mu.Lock()
	s.commandActive = false
	s.mu.Unlock()
}
