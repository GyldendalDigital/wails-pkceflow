package wailspkceflow

import (
	"context"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	_ application.ServiceName     = (*FrontendService)(nil)
	_ application.ServiceStartup  = (*FrontendService)(nil)
	_ application.ServiceShutdown = (*FrontendService)(nil)
)

// FrontendService is the Wails-bindable, frontend-safe view of AuthService.
// Obtain it from [AuthService.Frontend]; its zero value is not usable. It
// deliberately exposes no core client, manual pause/resume controls, or
// token-bearing API.
type FrontendService struct {
	auth *AuthService
}

// Frontend returns the stable service instance applications should register
// with Wails. The returned service exposes only Login, Logout, AuthStatus,
// IsAuthenticated, Claims, and RestoreStatus to generated frontend bindings.
// Wails handles its ServiceName, ServiceStartup, and ServiceShutdown methods
// internally.
func (s *AuthService) Frontend() *FrontendService {
	s.frontendOnce.Do(func() {
		s.frontend = &FrontendService{auth: s}
	})
	return s.frontend
}

// ServiceName implements the Wails ServiceName interface.
func (s *FrontendService) ServiceName() string {
	return s.auth.ServiceName()
}

// ServiceStartup implements the Wails ServiceStartup interface.
func (s *FrontendService) ServiceStartup(ctx context.Context, opts application.ServiceOptions) error {
	return s.auth.ServiceStartup(ctx, opts)
}

// ServiceShutdown implements the Wails ServiceShutdown interface.
func (s *FrontendService) ServiceShutdown() error {
	return s.auth.ServiceShutdown()
}

// Login starts the OIDC Authorization Code + PKCE flow.
func (s *FrontendService) Login() AuthResult {
	return s.auth.Login()
}

// Logout clears the current session and performs best-effort provider logout.
func (s *FrontendService) Logout() AuthResult {
	return s.auth.Logout()
}

// AuthStatus reports the current authentication state without making a
// network request.
func (s *FrontendService) AuthStatus() pkceflow.AuthStatusResult {
	return s.auth.AuthStatus()
}

// IsAuthenticated reports whether the user currently has a usable session.
func (s *FrontendService) IsAuthenticated() bool {
	return s.auth.IsAuthenticated()
}

// Claims returns frontend-safe ID token claims for the current session.
func (s *FrontendService) Claims() (ClaimsDTO, AuthResult) {
	return s.auth.Claims()
}

// RestoreStatus returns the frontend-safe, latched outcome of the latest
// session restoration attempt.
func (s *FrontendService) RestoreStatus() RestoreStatus {
	return s.auth.RestoreStatus()
}
