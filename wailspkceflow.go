package wailspkceflow

import (
	"context"
	"fmt"
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

	// RestoreErrorPolicy controls whether an operational persistence Load
	// failure keeps the service running or fails ServiceStartup. Optional;
	// defaults to RestoreErrorContinue.
	RestoreErrorPolicy RestoreErrorPolicy

	// OnRestoreError receives operational persistence Load failures. It runs
	// synchronously on the startup goroutine outside service lifecycle locks, so
	// it must return promptly, and is never exposed to frontend bindings. The
	// error text is safely redacted, while its backend cause remains available
	// through errors.Is and errors.As. Applications must not forward the cause or
	// its text to the frontend.
	OnRestoreError func(error)
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

	restorePolicy  RestoreErrorPolicy
	onRestoreError func(error)
	restoreMu      sync.RWMutex
	restoreStatus  RestoreStatus

	serviceMu         sync.Mutex // coordinates Wails startup and shutdown
	startupInProgress bool
	startupStopped    bool
	refresh           refreshLoopController
	refreshMu         sync.Mutex // serializes the core loop's separate stop/start steps
	refreshState      refreshLoopState
	refreshCtx        context.Context
	pauseReasons      refreshPauseReason

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
// as pkceflow.New when the configuration is invalid, or an error when
// RestoreErrorPolicy is not a supported value.
//
//nolint:gocritic // hugeParam: Options is intentionally passed by value (contains Config)
func New(opts Options) (*AuthService, error) {
	if !opts.RestoreErrorPolicy.valid() {
		return nil, fmt.Errorf(
			"wailspkceflow: unsupported restore error policy %d",
			opts.RestoreErrorPolicy,
		)
	}

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
		client:         client,
		bus:            bus,
		deliver:        deliver,
		autoInit:       opts.AutoInit,
		logger:         logger,
		restorePolicy:  opts.RestoreErrorPolicy,
		onRestoreError: opts.OnRestoreError,
		restoreStatus:  RestoreStatusPending,
		refresh:        client,
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
	return s.startService(ctx, app.Event, platformMobileLifecycleEvents(), func() {
		s.bus.SetTarget(&appEmitter{app: app})
	})
}

func (s *AuthService) startService(
	ctx context.Context,
	subscriber applicationEventSubscriber,
	mobileEvents mobileLifecycleEventSet,
	onInstalled func(),
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	started, teardown, err := s.beginServiceStart()
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	defer s.completeServiceStart()

	staleCtx := teardown.run()
	s.stopRefreshLoop(staleCtx)
	if err := serviceStartupContextError(ctx); err != nil {
		return err
	}
	if s.serviceStartWasStopped() {
		return nil
	}

	s.setRestoreStatus(RestoreStatusPending)
	restored, restoreErr := s.client.RestoreSession()
	switch {
	case restoreErr != nil:
		s.setRestoreStatus(RestoreStatusPersistenceUnavailable)
	case restored:
		s.setRestoreStatus(RestoreStatusRestored)
	default:
		s.setRestoreStatus(RestoreStatusNoSession)
	}

	runCtx, startupErr := s.installServiceLifecycle(ctx, restoreErr)
	if runCtx != nil {
		var current bool
		current, startupErr = s.ensureCurrentServiceStart(ctx, runCtx)
		if !current {
			runCtx = nil
		}
	}

	if runCtx != nil && onInstalled != nil {
		// Wails callouts stay outside serviceMu so event delivery can re-enter
		// shutdown. The post-call generation check prevents lasting startup work.
		onInstalled()
		var current bool
		current, startupErr = s.ensureCurrentServiceStart(ctx, runCtx)
		if !current {
			runCtx = nil
		}
	}

	if runCtx != nil {
		// A concurrent shutdown may race with host registration. Callbacks are
		// generation-gated, and setLifecycleCleanup immediately removes any
		// subscriptions registered after that generation was detached.
		cleanup := s.subscribeApplicationEvents(runCtx, subscriber, mobileEvents)
		s.setLifecycleCleanup(runCtx, cleanup)
		var current bool
		current, startupErr = s.ensureCurrentServiceStart(ctx, runCtx)
		if !current {
			runCtx = nil
		}
	}

	if restoreErr != nil {
		s.reportRestoreError(restoreErr, runCtx)
	}

	if runCtx != nil {
		var current bool
		current, startupErr = s.ensureCurrentServiceStart(ctx, runCtx)
		if !current {
			runCtx = nil
		}
	}
	if startupErr != nil {
		return startupErr
	}
	if restoreErr != nil && s.restorePolicy == RestoreErrorFailStartup {
		return fmt.Errorf("wailspkceflow: service startup failed: %w", restoreErr)
	}
	if runCtx == nil {
		return nil
	}

	s.startRefreshLoop(runCtx)
	if current, err := s.ensureCurrentServiceStart(ctx, runCtx); !current {
		return err
	}
	if s.autoInit && s.isCurrentLifecycle(runCtx) {
		go func() {
			if err := s.client.Init(runCtx); err != nil {
				if runCtx.Err() == nil {
					s.logger.Warn("pkceflow: background Init failed", "error", err)
				}
			}
		}()
	}
	return nil
}

func (s *AuthService) beginServiceStart() (bool, lifecycleTeardown, error) {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()

	if s.startupInProgress {
		return false, lifecycleTeardown{}, ErrServiceStartupInProgress
	}
	if _, active := s.activeLifecycleContext(); active {
		return false, lifecycleTeardown{}, nil
	}

	s.startupInProgress = true
	s.startupStopped = false
	teardown := s.detachLifecycle()
	return true, teardown, nil
}

func (s *AuthService) serviceStartWasStopped() bool {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()
	return s.startupStopped
}

func (s *AuthService) completeServiceStart() {
	s.serviceMu.Lock()
	s.startupInProgress = false
	s.startupStopped = false
	s.serviceMu.Unlock()
}

func (s *AuthService) installServiceLifecycle(
	ctx context.Context,
	restoreErr error,
) (context.Context, error) {
	s.serviceMu.Lock()
	defer s.serviceMu.Unlock()

	if s.startupStopped {
		return nil, nil
	}
	if err := serviceStartupContextError(ctx); err != nil {
		return nil, err
	}
	if restoreErr != nil && s.restorePolicy == RestoreErrorFailStartup {
		return nil, nil
	}

	runCtx, installed := s.installLifecycle(ctx)
	if !installed {
		return nil, nil
	}
	return runCtx, nil
}

func (s *AuthService) ensureCurrentServiceStart(
	ctx context.Context,
	runCtx context.Context,
) (bool, error) {
	if s.isCurrentLifecycle(runCtx) {
		return true, nil
	}

	s.stopServiceGeneration(runCtx)
	return false, serviceStartupContextError(ctx)
}

func (s *AuthService) stopServiceGeneration(runCtx context.Context) {
	s.serviceMu.Lock()
	teardown := s.detachLifecycleGeneration(runCtx)
	s.serviceMu.Unlock()

	cleared := teardown.run()
	s.stopRefreshLoop(cleared)
}

func serviceStartupContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	return fmt.Errorf("wailspkceflow: service startup cancelled: %w", ctx.Err())
}

// ServiceShutdown implements the Wails ServiceShutdown interface. It stops the
// background refresh loop, cancels pending auth commands, and removes the
// launch-URL and mobile lifecycle subscriptions.
func (s *AuthService) ServiceShutdown() error {
	s.serviceMu.Lock()
	if s.startupInProgress {
		s.startupStopped = true
	}
	teardown := s.detachLifecycle()
	s.serviceMu.Unlock()

	runCtx := teardown.run()
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
	return s.detachLifecycle().run()
}

type lifecycleTeardown struct {
	runCtx  context.Context
	cancel  context.CancelFunc
	cleanup func()
}

func (t lifecycleTeardown) run() context.Context {
	if t.cancel != nil {
		t.cancel()
	}
	if t.cleanup != nil {
		t.cleanup()
	}
	return t.runCtx
}

func (s *AuthService) detachLifecycle() lifecycleTeardown {
	return s.detachLifecycleMatching(context.Background(), false)
}

func (s *AuthService) detachLifecycleGeneration(expected context.Context) lifecycleTeardown {
	return s.detachLifecycleMatching(expected, true)
}

func (s *AuthService) detachLifecycleMatching(
	expected context.Context,
	matchExpected bool,
) lifecycleTeardown {
	s.mu.Lock()
	if matchExpected && (!s.lifecycleActive || s.runCtx != expected) {
		s.mu.Unlock()
		return lifecycleTeardown{}
	}
	if !s.lifecycleActive {
		if !matchExpected {
			s.lifecycleStarted = true
		}
		s.mu.Unlock()
		return lifecycleTeardown{}
	}

	teardown := lifecycleTeardown{
		runCtx:  s.runCtx,
		cancel:  s.runCancel,
		cleanup: s.lifecycleCleanup,
	}
	s.runCtx = nil
	s.runCancel = nil
	s.lifecycleCleanup = nil
	s.lifecycleStarted = true
	s.lifecycleActive = false
	s.mu.Unlock()
	return teardown
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
