package wailspkceflow

import (
	"context"
	"log/slog"
	"sync"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/eventbus"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// URLDeliverer receives a deep-link callback URL captured by the OS. It is
// implemented by *mobileflow.Handler (its DeliverURL method). Provide one via
// Options.Flow or Options.DeepLinkDelivery to route mobile Universal Link / App
// Link callbacks into the auth flow. Implementations must be non-blocking and
// safe for concurrent calls.
type URLDeliverer interface {
	DeliverURL(url string)
}

type refreshLoopController interface {
	StartRefreshLoop(context.Context)
	StopRefreshLoop()
}

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

// AuthService adapts a pkceflow.Client to Wails v3. Construct it with New and
// keep Client for Go-side API calls. Applications should normally bind a thin
// delegator that forwards only frontend-safe methods, leaving Client off the
// generated binding surface. See the package README and desktop example.
type AuthService struct {
	client   *pkceflow.Client
	bus      *eventbus.DeferredEventBus
	deliver  URLDeliverer
	autoInit bool
	logger   *slog.Logger

	refresh   refreshLoopController
	refreshMu sync.Mutex // serializes the core loop's separate stop/start steps

	mu                   sync.Mutex
	runCtx               context.Context
	runCancel            context.CancelFunc
	unsubscribeLaunchURL func()
	lifecycleStarted     bool
	lifecycleActive      bool
	commandActive        bool
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
func (s *AuthService) Client() *pkceflow.Client {
	return s.client
}

// ServiceName implements the Wails ServiceName interface.
func (s *AuthService) ServiceName() string {
	return "pkceflow.AuthService"
}

// ServiceStartup implements the Wails ServiceStartup interface. It wires auth
// events to the Wails application, restores any cached session, starts the
// background refresh loop, and (when configured) subscribes to deep-link launch
// events and runs OIDC discovery.
func (s *AuthService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	runCtx := s.installLifecycle(ctx, nil)

	app := application.Get()
	s.bus.SetTarget(&appEmitter{app: app})

	if s.deliver != nil {
		unsubscribe := app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl,
			func(e *application.ApplicationEvent) {
				s.handleLaunchURL(e.Context().URL())
			})
		s.setLifecycleSubscription(runCtx, unsubscribe)
	}

	s.client.RestoreSession()
	s.refreshMu.Lock()
	if s.isCurrentLifecycle(runCtx) {
		s.refresh.StartRefreshLoop(runCtx)
	}
	s.refreshMu.Unlock()

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

// ServiceShutdown implements the Wails ServiceShutdown interface. It stops the
// background refresh loop, cancels pending auth commands, and removes the
// application launch-URL subscription.
func (s *AuthService) ServiceShutdown() error {
	s.clearLifecycle()
	s.stopRefreshLoop()
	return nil
}

// Login runs the OIDC Authorization Code + PKCE flow. It is bound to the
// frontend and returns a structured AuthResult (never a raw error, never a
// token). The login timeout comes from the client Config.
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
// This frontend-bound method returns a structured AuthResult, and the logout
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

// Pause stops the background token refresh loop. Call it when the app is
// backgrounded (mobile) to avoid needless network activity and battery drain.
func (s *AuthService) Pause() {
	s.stopRefreshLoop()
}

// Resume restarts the background token refresh loop after Pause. Call it when
// the app returns to the foreground. The loop continues the current session's
// refresh schedule instead of forcing a refresh; an already-due threshold may
// still run immediately.
func (s *AuthService) Resume() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	ctx, ok := s.serviceContext()
	if !ok {
		return
	}
	s.refresh.StartRefreshLoop(ctx)
}

// handleLaunchURL routes a deep-link callback URL to the configured deliverer.
// It is separated from the event subscription so it can be unit tested without
// a running Wails application.
func (s *AuthService) handleLaunchURL(url string) {
	if s.deliver != nil {
		s.deliver.DeliverURL(url)
	}
}

func (s *AuthService) installLifecycle(parent context.Context, unsubscribe func()) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	oldCancel := s.runCancel
	oldUnsubscribe := s.unsubscribeLaunchURL
	s.runCtx = runCtx
	s.runCancel = cancel
	s.unsubscribeLaunchURL = unsubscribe
	s.lifecycleStarted = true
	s.lifecycleActive = true
	s.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if oldUnsubscribe != nil {
		oldUnsubscribe()
	}
	return runCtx
}

func (s *AuthService) clearLifecycle() {
	s.mu.Lock()
	cancel := s.runCancel
	unsubscribe := s.unsubscribeLaunchURL
	s.runCtx = nil
	s.runCancel = nil
	s.unsubscribeLaunchURL = nil
	s.lifecycleStarted = true
	s.lifecycleActive = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (s *AuthService) setLifecycleSubscription(runCtx context.Context, unsubscribe func()) {
	if unsubscribe == nil {
		return
	}

	s.mu.Lock()
	if s.lifecycleActive && s.runCtx == runCtx {
		s.unsubscribeLaunchURL, unsubscribe = unsubscribe, s.unsubscribeLaunchURL
	}
	s.mu.Unlock()

	if unsubscribe != nil {
		unsubscribe()
	}
}

func (s *AuthService) isCurrentLifecycle(runCtx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycleActive && s.runCtx == runCtx
}

func (s *AuthService) serviceContext() (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lifecycleStarted && !s.lifecycleActive {
		return nil, false
	}
	ctx := s.runCtx
	if ctx != nil && ctx.Err() != nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, true
}

func (s *AuthService) stopRefreshLoop() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.refresh.StopRefreshLoop()
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
