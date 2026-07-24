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
  main.go              App service (delegates to wailspkceflow.AuthService) + wiring
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
- Wails v3 Linux build deps (on Ubuntu 24.04): `libgtk-3-dev libwebkit2gtk-4.1-dev`
  and run with `-tags gtk3` (newer distros use the default gtk4 backend with
  `libgtk-4-dev libwebkitgtk-6.0-dev`).

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
# Ubuntu <= 24.04 (gtk3):
go run -tags gtk3 .
# newer distros (gtk4 default):
go run .
```

Click **Log in**, authenticate as **demo** / **demo** in the browser, and the app
returns to the signed-in state.

## What to watch

- A **"Logged in"** toast appears, and the status dot turns green.
- The **ID token claims** (subject, name, email, issuer, expiry) render.
- Every ~90 seconds a **"Token refreshed"** toast appears: the background loop
  refreshes the 3-minute access token at roughly T/2 (DHCP-style timing).
- **Log out** clears the session, runs RP-initiated logout, and shows a toast.
- Stop Keycloak while signed in to see the discovery/refresh error toasts.

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
- **Actions (JS -> Go):** the frontend calls bound `App` methods with
  `Call.ByID(<id>)`. The IDs in `app.js` come from `wails3 generate bindings`
  (each is a stable hash of the method's fully-qualified name). Regenerate and
  update them only if you rename a method, the `App` struct, or the package.

## Notes

- `App` is a thin `package main` service that delegates to
  `wailspkceflow.AuthService` and exposes only frontend-safe methods. It does not
  expose `Client()` (that accessor is for Go-side API calls via `TokenFn`, not the
  webview), which keeps the bound surface and tokens off the frontend.
- Tokens are persisted with `filestore.NewDefault`, which resolves a per-user
  config directory without the app knowing the platform specifics.

## Cleanup

```bash
cd keycloak && docker compose down
```
