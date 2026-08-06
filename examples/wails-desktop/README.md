# wails-pkceflow desktop example

A minimal, framework-free Wails v3 desktop app that demonstrates
[wails-pkceflow](https://github.com/GyldendalDigital/wails-pkceflow): OIDC
Authorization Code + PKCE login/logout, live auth status, ID token claims, and a
dismissable **notification center** driven by the backend auth events.

It ships with a **dockerized Keycloak** whose realm is pre-baked so there is zero
setup friction: a public PKCE client, fixed loopback callback, a `demo` / `demo`
user, and a **3-minute access-token lifespan** so the background refresher
visibly cycles while you watch.

```
examples/wails-desktop/
  main.go              Auth service and Wails application wiring
  frontend/            Vanilla HTML + CSS + JS (no bundler, no generated bindings)
    index.html
    app.js             Events.On for notifications, Call.ByID for actions
    style.css
  keycloak/
    docker-compose.yml
    demo-realm.json    Pre-configured realm (client, callback, user, token lifespan)
```

## Prerequisites

- Docker + Docker Compose
- Go 1.25+
- Wails v3 default Linux build deps on Debian/Ubuntu (including Ubuntu 24.04):
  `build-essential`, `pkg-config`, `libgtk-4-dev`, and `libwebkitgtk-6.0-dev`.
  The legacy GTK3 path remains available through Wails v3.0.x for older
  distributions with `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, and `-tags gtk3`.

## 1. Start Keycloak

```bash
cd keycloak
docker compose up -d
# wait ~30s for the realm import; ready when this returns JSON:
curl -s http://localhost:8080/realms/demo/.well-known/openid-configuration | head -c 80
```

Admin console (optional): http://localhost:8080 with `admin` / `admin`.

## 2. Run the app

From this directory:

```bash
# GTK4 / WebKitGTK 6.0 (default):
go run .
# Legacy GTK3 / WebKit2GTK 4.1 fallback:
go run -tags gtk3 .
```

Click **Log in**, authenticate as **demo** / **demo** in the browser, and the app
returns to the signed-in state.

This example is its own nested Go module. If a parent `go.work` contains only
the core and wrapper modules, run with `GOWORK=off`:

```bash
GOWORK=off go run .
```

Override the issuer when Keycloak is reachable at another host:

```bash
GOWORK=off PKCEFLOW_ISSUER=http://10.0.2.2:8080/realms/demo go run .
```

From Windows PowerShell:

```powershell
$env:GOWORK = "off"
$env:PKCEFLOW_ISSUER = "http://10.0.2.2:8080/realms/demo"
go run .
```

`10.0.2.2` is the common VirtualBox NAT address for reaching the host from a
Windows guest. Use the address appropriate to your VM or network. The callback
still uses `127.0.0.1:34115` inside the guest, so the supplied realm's redirect
registration remains unchanged.

## What to watch

- A **"Logged in"** toast appears, and the status dot turns green.
- The **ID token claims** (subject, name, email, issuer, expiry) render.
- About 90 seconds after login, a **"Token refreshed"** toast appears. The
  background loop schedules each 3-minute token from its session timestamps
  and refreshes at the lifetime midpoint.
- **Log out** clears the session, runs RP-initiated logout, and shows a toast.
- Start the app while Keycloak is unavailable to see the initialization-failed
  toast.

## The pre-baked values

The app hardcodes values that match `keycloak/demo-realm.json`:

| Setting | Value |
|---|---|
| Issuer URL | `http://localhost:8080/realms/demo` |
| Client ID | `demo-native` (public, PKCE S256) |
| Redirect URI | `http://127.0.0.1:34115/callback` |
| User / password | `demo` / `demo` |
| Access token lifespan | 180s |

## How the frontend talks to the backend

- **Events (Go -> JS):** `wailspkceflow` bridges `pkceflow` auth events to Wails
  app events. The frontend subscribes with `Events.On("oidcauth:token-refreshed", ...)`
  etc. and renders a toast. No generated code required.
- **Actions (JS -> Go):** the frontend calls bound `FrontendService` methods with
  `Call.ByID(<id>)`. The IDs in `app.js` come from `wails3 generate bindings`
  (each is a stable hash of the method's fully-qualified name). Regenerate and
  update them only if you rename a method, the service type, or the package.

## Notes

- `authSvc.Frontend()` returns the library-provided service with exactly the
  frontend-safe auth methods. It does not expose `Client()`, `Pause()`, or
  `Resume()`, keeping backend controls and tokens out of the webview bindings.
- Tokens are persisted with `filestore.NewDefault`, which resolves a per-user
  config directory without the app knowing the platform specifics.

## Cleanup

```bash
cd keycloak && docker compose down
```
