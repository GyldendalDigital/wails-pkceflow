# wails-pkceflow

A [Wails v3](https://wails.io/) service wrapper for [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow). Provides OIDC PKCE authentication as a Wails service with lifecycle management, event bridging, and deep link routing for desktop and mobile apps.

## Status

**Pre-1.0 alpha.** The API may still change. A vanilla Keycloak run has covered
login, token exchange, refresh, and logout on Linux, plus login, exchange, and
refresh on Windows. Mobile, macOS, and Windows logout still need manual
validation.

## Features

- Wails v3 service adapter with `ServiceStartup` / `ServiceShutdown` lifecycle
- Automatic session restore, optional background OIDC discovery, and background token refresh
- Event bridge from go-pkceflow auth events (`oidcauth:*`) to Wails application events
- Deep link routing: connects the Wails `ApplicationLaunchedWithUrl` event to go-pkceflow's mobile flow handler
- Structured, frontend-friendly results (no tokens ever cross to the frontend)
- `Pause` / `Resume` for mobile background/foreground refresh control

## Installation

```bash
go get github.com/GyldendalDigital/wails-pkceflow
```

## Usage

Create the auth facade with `New`, keep its core client for Go-side API calls,
and bind a thin app-owned delegator to Wails. The delegator keeps the
backend-only `Client()` accessor off the generated frontend surface:

```go
package main

import (
	"context"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/desktopflow"
	"github.com/GyldendalDigital/go-pkceflow/filestore"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
)

type App struct {
	auth *wailspkceflow.AuthService
}

func (a *App) ServiceName() string {
	return "Auth"
}

func (a *App) ServiceStartup(
	ctx context.Context,
	opts application.ServiceOptions,
) error {
	return a.auth.ServiceStartup(ctx, opts)
}

func (a *App) ServiceShutdown() error {
	return a.auth.ServiceShutdown()
}

func (a *App) Login() wailspkceflow.AuthResult {
	return a.auth.Login()
}

func (a *App) Logout() wailspkceflow.AuthResult {
	return a.auth.Logout()
}

func (a *App) AuthStatus() pkceflow.AuthStatusResult {
	return a.auth.AuthStatus()
}

func (a *App) IsAuthenticated() bool {
	return a.auth.IsAuthenticated()
}

func (a *App) Claims() (
	wailspkceflow.ClaimsDTO,
	wailspkceflow.AuthResult,
) {
	return a.auth.Claims()
}

func main() {
	handler := desktopflow.New(15051) // desktop: localhost callback
	store, err := filestore.NewDefault("com.example.myapp")
	if err != nil {
		log.Fatal(err)
	}

	authSvc, err := wailspkceflow.New(wailspkceflow.Options{
		Config: pkceflow.Config{
			IssuerURL: "https://login.example.com/realms/myapp",
			ClientID:  "myapp-desktop",
		},
		Flow:     handler,
		Store:    store,
		AutoInit: true, // run OIDC discovery in the background on startup
	})
	if err != nil {
		log.Fatal(err)
	}

	// Keep this in the Go API layer; do not expose it through App.
	client := authSvc.Client()
	_ = client // e.g. client.TokenFn(requestContext)

	app := application.New(application.Options{
		Name: "My App",
		Services: []application.Service{
			application.NewService(&App{auth: authSvc}),
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "My App",
		Width:  900,
		Height: 700,
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

The complete [desktop example](examples/wails-desktop) uses this pattern with a
Dockerized Keycloak realm, event-driven notifications, and ID token claims.
Registering `AuthService` itself is convenient but may expose its Go-only
`Client()` method to Wails binding generation, so it is not the recommended
application boundary.

### Frontend-bound methods

Forward only the methods your frontend needs from the app-owned delegator.
Tokens are never returned by these methods.

| Method | Returns | Purpose |
|--------|---------|---------|
| `Login()` | `AuthResult` | Start the OIDC PKCE login flow |
| `Logout()` | `AuthResult` | Clear in-memory state, attempt persistence deletion, and run RP-Initiated Logout when supported |
| `AuthStatus()` | `pkceflow.AuthStatusResult` | Current auth state (no network) |
| `IsAuthenticated()` | `bool` | Whether a usable session exists |
| `Claims()` | `(ClaimsDTO, AuthResult)` | Decoded ID token claims |

`AuthResult` carries a stable `code` (`""` on success, or `cancelled`,
`flow_in_progress`, `not_initialized`, `not_authenticated`, `session_expired`,
an OAuth2 error code, or `error`) so the frontend can branch without string
matching. `flow_in_progress` means another frontend login or logout command is
still active.

### Auth events

The service forwards go-pkceflow events to Wails app events verbatim. Listen for
them in the frontend or in Go:

- `oidcauth:logged-in`
- `oidcauth:logged-out`
- `oidcauth:token-refreshed`
- `oidcauth:session-expired`
- `oidcauth:init-failed`

### Mobile

On mobile, pass a `mobileflow.Handler` as `Flow`. Because the handler also
implements `URLDeliverer`, the wrapper automatically routes Wails
`ApplicationLaunchedWithUrl` events to it:

```go
handler := mobileflow.New(
	"https://app.example.com/auth/callback",
	openURL,
)
authSvc, err := wailspkceflow.New(wailspkceflow.Options{
	Config: pkceflow.Config{
		IssuerURL: "https://login.example.com",
		ClientID:  "my-mobile-app",
	},
	Flow: handler,
})
```

`DeepLinkDelivery` remains available as an explicit override when delivery must
go somewhere other than `Flow`. The wrapper forwards every launch URL unchanged
and never parses or logs it; hardened callback URI and state correlation belongs
to core `mobileflow`, which safely ignores unrelated links.

Call `Pause()` when the app is backgrounded and `Resume()` when it returns to
the foreground. Service shutdown cancels a pending login/logout and removes the
Wails launch-URL subscription.

The complete OS-to-Wails delivery path still requires emulator/device
validation under [issue #8](https://github.com/GyldendalDigital/wails-pkceflow/issues/8).
Do not claim mobile-ready support until that check passes for the pinned Wails
version.

### Running the example in a paired workspace

`examples/wails-desktop` is a nested Go module. If a parent `go.work` includes
only the two library modules, either add the example module to that workspace
or run it with:

```bash
GOWORK=off go run .
```

PowerShell equivalent:

```powershell
$env:GOWORK = "off"
go run .
```

## Related

- [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow) -- The core OIDC library (framework-agnostic)
- [Core provider setup guides](https://github.com/GyldendalDigital/go-pkceflow#documentation)
- [Desktop example](examples/wails-desktop)

## License

[MIT](LICENSE)
