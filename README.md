# wails-pkceflow

A [Wails v3](https://wails.io/) service wrapper for [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow). Provides OIDC PKCE authentication as a Wails service with lifecycle management, event bridging, and deep link routing for desktop and mobile apps.

## Status

**Early development.** The API is not stable and the library is not yet ready for use.

## Features (planned)

- Wails v3 service adapter with `ServiceStartup` / `ServiceShutdown` lifecycle
- Automatic session restore, OIDC discovery, and background token refresh
- Event bridge from go-pkceflow auth events to Wails application events
- Deep link router for mobile auth callbacks (iOS Universal Links, Android App Links)
- Deferred event bus for Wails startup ordering

## Installation

```bash
go get github.com/GyldendalDigital/wails-pkceflow
```

## Related

- [go-pkceflow](https://github.com/GyldendalDigital/go-pkceflow) -- The core OIDC library (framework-agnostic)

## License

[MIT](LICENSE)
