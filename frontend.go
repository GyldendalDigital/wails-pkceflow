package wailspkceflow

import (
	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// frontendAuth defines the methods exposed to Wails frontend bindings.
// AuthService must satisfy this interface; violations are caught at compile time.
type frontendAuth interface {
	Login() AuthResult
	Logout() AuthResult
	AuthStatus() pkceflow.AuthStatusResult
	IsAuthenticated() bool
	Claims() (ClaimsDTO, AuthResult)
	RestoreStatus() RestoreStatus
}

// serviceLifecycle defines the Wails service lifecycle contract.
type serviceLifecycle interface {
	application.ServiceName
	application.ServiceStartup
	application.ServiceShutdown
}

// Compile-time checks: AuthService satisfies both contracts.
var (
	_ frontendAuth     = (*AuthService)(nil)
	_ serviceLifecycle = (*AuthService)(nil)
)

// FrontendService is the Wails-bindable, frontend-safe view of AuthService.
// It embeds the frontend method contract and the service lifecycle contract,
// delegating all calls to the backing AuthService via interface promotion.
// Only methods declared in [frontendAuth] appear in generated frontend bindings;
// backend-only methods (Client, Pause, Resume) are not reachable from the
// webview. The [serviceLifecycle] methods are recognized by Wails for startup,
// shutdown, and naming but are excluded from generated bindings by convention.
type FrontendService struct {
	frontendAuth
	serviceLifecycle
}

// Frontend returns the stable service instance applications should register
// with Wails. Wails binds the promoted frontendAuth methods (Login, Logout,
// AuthStatus, IsAuthenticated, Claims, RestoreStatus) and manages lifecycle
// via the promoted serviceLifecycle methods (ServiceName, ServiceStartup,
// ServiceShutdown).
func (s *AuthService) Frontend() *FrontendService {
	s.frontendOnce.Do(func() {
		s.frontend = &FrontendService{
			frontendAuth:     s,
			serviceLifecycle: s,
		}
	})
	return s.frontend
}
