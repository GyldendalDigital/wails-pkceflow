# wails-pkceflow

A [Wails v3](https://wails.io/) service wrapper for
[go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow). It provides
OIDC PKCE authentication as a Wails service with lifecycle management, event
bridging, automatic mobile refresh-loop control, and an adapter from Wails
launch-URL events to the core mobile callback handler.

## Status

**Pre-1.0 alpha wrapper, tested with Wails v3.0.0-beta.2.** The wrapper API may
still change. A vanilla Keycloak run has covered login, token exchange, refresh,
and logout on Linux, plus login, exchange, and refresh on Windows. macOS and
Windows logout still need manual validation. The wrapper's mobile adapter is
unit-tested, but Wails beta.2 does not produce the required launch-URL events on
Android or iOS, so end-to-end Wails mobile support is not available on the
pinned release. This does not limit core `mobileflow` or Wails desktop use.

## Features

- Wails v3 service adapter with `ServiceStartup` / `ServiceShutdown` lifecycle
- Automatic session restore, optional background OIDC discovery, and background
  token refresh
- Event bridge from go-pkceflow auth events (`oidcauth:*`) to Wails application events
- Mobile event adapter: forwards `ApplicationLaunchedWithUrl` events when the
  Wails host supplies them; beta.2 does not yet do so on Android or iOS
- Automatic refresh pause/resume from Android and iOS application lifecycle
  events; desktop behavior is unchanged
- Library-provided frontend service with no raw OAuth token or core-client
  access
- Structured, frontend-friendly results with fixed, redacted error summaries
- Backend-only `Pause` / `Resume` methods for explicit application policy

## Installation

```bash
go get github.com/GyldendalDigital/wails-pkceflow
```

## Usage

Create the auth service with `New`, keep its core client for Go-side API calls,
and register the service returned by `Frontend()` with Wails. The frontend
service deliberately omits the backend-only `Client()`, `Pause()`, and
`Resume()` methods:

```go
package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/desktopflow"
	"github.com/GyldendalDigital/go-pkceflow/filestore"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
)

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

	// Keep this in the Go API layer. Frontend() has no Client accessor.
	client := authSvc.Client()
	_ = client // e.g. client.TokenFn(requestContext)

	app := application.New(application.Options{
		Name: "My App",
		Services: []application.Service{
			application.NewService(authSvc.Frontend()),
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
Register `authSvc.Frontend()`, not `authSvc`: binding `AuthService` itself would
also expose its Go-only methods to Wails binding generation.

While the service is active, let it own `Client().StartRefreshLoop` and
`Client().StopRefreshLoop`; calling those core controls directly bypasses the
wrapper's lifecycle state. Use the backend-only `authSvc.Pause()` and
`authSvc.Resume()` methods for application policy.

### Frontend-bound methods

`FrontendService` exposes exactly these application methods. Tokens are never
returned by them; Wails consumes the service lifecycle methods internally.

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
still active. `message` is a fixed frontend-safe summary; backend and provider
error text is never forwarded.

`ClaimsDTO.Raw` contains all verified provider-issued ID-token claims, but not
the encoded ID token or OAuth tokens. Configure custom claims with the webview
audience in mind.

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

The application configures its Android intent filters or iOS URL
types/associated domains and supplies the external-browser opener. On a Wails
release with mobile host delivery, Wails turns native callbacks into
`ApplicationLaunchedWithUrl`; this wrapper forwards that event unchanged; core
`mobileflow` validates and correlates it. The pinned Wails release does not yet
provide that native event path.

`DeepLinkDelivery` remains available as an explicit override when delivery must
go somewhere other than `Flow`. The wrapper forwards every launch URL unchanged
and never parses or logs it; hardened callback URI and state correlation belongs
to core `mobileflow`, which safely ignores unrelated links.

The wrapper automatically stops refresh work for Android `ActivityPaused` and
iOS `ApplicationDidEnterBackground`, then resumes it for Android
`ActivityResumed` and iOS `ApplicationWillEnterForeground`. Mobile applications
do not need to duplicate those subscriptions. Desktop builds register no mobile
lifecycle events. The backend-only `Pause()` and `Resume()` methods remain
available for explicit application policy; repeated calls are no-ops. Manual
and mobile pause reasons compose: `Pause()` remains in effect across automatic
foreground events until Go code calls `Resume()`, and `Resume()` does not start
refresh work while the application is still backgrounded.

Service shutdown cancels a pending login/logout and removes the launch-URL and
mobile lifecycle subscriptions. Callbacks that observe a stopped service
generation are ignored.

Wails beta.2 does not preserve source order when dispatching application events:
it starts independent goroutines for events and listeners. The wrapper
serializes refresh transitions but cannot reconstruct pause/resume order after
Wails has discarded it. Beta.2 also registers native iOS listeners
asynchronously, so it can miss an initial background event in a narrow startup
window, and its listener teardown does not snapshot listeners before concurrent
unsubscribe. Wrapper state remains generation-guarded; complete ordering and
native dispatch/teardown race guarantees require upstream Wails changes.

The active core flow exists only in process memory. A cold-launch URL can prove
that the host delivered an event, but it cannot resume a login or logout whose
process was killed; the application must start that flow again.

The wrapper subscription and forwarding behavior is implemented and
unit-tested, and core `mobileflow` validates callbacks once delivered. The
missing step is native OS-to-Wails event production: Wails beta.2 does not yet
include the proposed mobile deep-link delivery and Android warm-start changes
([wailsapp/wails#5808](https://github.com/wailsapp/wails/pull/5808),
[wailsapp/wails#5727](https://github.com/wailsapp/wails/pull/5727)).

[Issue #8](https://github.com/GyldendalDigital/wails-pkceflow/issues/8) tracks
only that Wails mobile host integration and its emulator/device acceptance
test. It does not block go-pkceflow or Wails desktop dogfooding. Do not claim
turnkey Wails mobile support until the host path exists in an official pinned
Wails release and the device check passes.

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
