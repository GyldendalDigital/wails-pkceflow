# wails-pkceflow

A [Wails v3](https://wails.io/) service wrapper for [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow). Provides OIDC PKCE authentication as a Wails service with lifecycle management, event bridging, and deep link routing for desktop and mobile apps.

## Status

**Early development.** The API is not yet stable.

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

Create the service with `New`, register it with your Wails app, and keep the
underlying client for your API calls:

```go
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/desktopflow"
	"github.com/GyldendalDigital/go-pkceflow/filestore"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
)

func main() {
	handler := desktopflow.New(15051) // desktop: localhost callback
	store, _ := filestore.New("com.example.myapp", configDir)

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
		panic(err)
	}

	app := application.New(application.Options{Name: "My App"})
	app.RegisterService(application.NewService(authSvc))

	// Use the client for authenticated API calls:
	//   tokenFn := authSvc.Client().TokenFn(ctx)
	//   req.Header.Set("Authorization", "Bearer "+tokenFn())

	app.NewWebviewWindow()
	_ = app.Run()
}
```

### Frontend-bound methods

The service exposes these methods to the frontend (via Wails bindings). Tokens
are never returned to the frontend.

| Method | Returns | Purpose |
|--------|---------|---------|
| `Login()` | `AuthResult` | Start the OIDC PKCE login flow |
| `Logout()` | `AuthResult` | Clear the session (and RP-Initiated Logout when supported) |
| `AuthStatus()` | `pkceflow.AuthStatusResult` | Current auth state (no network) |
| `IsAuthenticated()` | `bool` | Whether a usable session exists |
| `Claims()` | `(ClaimsDTO, AuthResult)` | Decoded ID token claims |

`AuthResult` carries a stable `code` (`""` on success, or `cancelled`,
`not_initialized`, `not_authenticated`, `session_expired`, an OAuth2 error code,
or `error`) so the frontend can branch without string matching.

### Auth events

The service forwards go-pkceflow events to Wails app events verbatim. Listen for
them in the frontend or in Go:

- `oidcauth:logged-in`
- `oidcauth:logged-out`
- `oidcauth:token-refreshed`
- `oidcauth:session-expired`
- `oidcauth:init-failed`

### Mobile

On mobile, use `mobileflow.Handler` and pass it as `DeepLinkDelivery` so the
wrapper routes the OS deep-link callback into the auth flow. Call `Pause()` when
the app is backgrounded and `Resume()` when it returns to the foreground.

## Related

- [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow) -- The core OIDC library (framework-agnostic)

## License

[MIT](LICENSE)
