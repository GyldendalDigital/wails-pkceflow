// Command wails-desktop is a minimal Wails v3 desktop app demonstrating
// wails-pkceflow: OIDC PKCE login/logout, live auth status, ID token claims,
// and a dismissable notification center driven by the backend auth events.
//
// Run the dockerized Keycloak in ./keycloak first (see README), then:
//
//	go run .
//
// Log in with demo / demo. The demo realm issues a 3-minute access token so
// the background refresher visibly cycles (watch for oidcauth:token-refreshed
// notifications roughly every 90 seconds).
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/desktopflow"
	"github.com/GyldendalDigital/go-pkceflow/filestore"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend
var assetsFS embed.FS

// These values match the pre-baked demo realm in ./keycloak/demo-realm.json.
// Override PKCEFLOW_ISSUER to point at a remote Keycloak (e.g., via VirtualBox
// NAT: PKCEFLOW_ISSUER=http://10.0.2.2:8080/realms/demo).
const (
	defaultIssuerURL = "http://localhost:8080/realms/demo"
	clientID         = "demo-native"
	callbackPort     = 34115 // desktopflow listens on http://127.0.0.1:34115/callback
	appID            = "go-pkceflow-demo"
)

// App is the bound Wails service the frontend calls. It delegates to the
// wails-pkceflow AuthService but exposes only the frontend-safe methods (no
// Client() accessor, which is for Go-side API calls, not the webview).
type App struct {
	auth *wailspkceflow.AuthService
}

func (a *App) ServiceName() string { return "AuthDemo" }

func (a *App) ServiceStartup(ctx context.Context, opts application.ServiceOptions) error {
	return a.auth.ServiceStartup(ctx, opts)
}

func (a *App) ServiceShutdown() error { return a.auth.ServiceShutdown() }

// Login starts the OIDC PKCE flow (opens the system browser).
func (a *App) Login() wailspkceflow.AuthResult { return a.auth.Login() }

// Logout clears the session and runs RP-initiated logout.
func (a *App) Logout() wailspkceflow.AuthResult { return a.auth.Logout() }

// AuthStatus reports the current session state (no network).
func (a *App) AuthStatus() pkceflow.AuthStatusResult { return a.auth.AuthStatus() }

// Claims returns the decoded ID token claims for display.
func (a *App) Claims() (wailspkceflow.ClaimsDTO, wailspkceflow.AuthResult) { return a.auth.Claims() }

// IsAuthenticated is a convenience boolean for the UI.
func (a *App) IsAuthenticated() bool { return a.auth.IsAuthenticated() }

func main() {
	issuer := defaultIssuerURL
	if env := os.Getenv("PKCEFLOW_ISSUER"); env != "" {
		issuer = env
	}

	// NewDefault hides the per-platform config-directory resolution.
	store, err := filestore.NewDefault(appID)
	if err != nil {
		log.Fatalf("token store: %v", err)
	}

	authSvc, err := wailspkceflow.New(wailspkceflow.Options{
		Config: pkceflow.Config{
			IssuerURL: issuer,
			ClientID:  clientID,
		},
		Flow:     desktopflow.New(callbackPort),
		Store:    store,
		AutoInit: true, // run OIDC discovery in the background on startup
	})
	if err != nil {
		log.Fatalf("auth service: %v", err)
	}

	assets, err := fs.Sub(assetsFS, "frontend")
	if err != nil {
		log.Fatalf("assets: %v", err)
	}

	app := application.New(application.Options{
		Name: "go-pkceflow demo",
		Services: []application.Service{
			application.NewService(&App{auth: authSvc}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "go-pkceflow demo",
		Width:  520,
		Height: 760,
	})

	if err := app.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}
