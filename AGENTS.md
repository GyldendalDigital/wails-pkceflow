# Agent Guidance for wails-pkceflow

First read the canonical shared guidance from the core repository:

https://raw.githubusercontent.com/GyldendalDigital/go-pkceflow/master/AGENTS.md

When working in the sibling development workspace, the same guidance is usually
available locally at:

```text
../go-pkceflow/AGENTS.md
```

This file adds wrapper-specific constraints for
`github.com/GyldendalDigital/wails-pkceflow`.

## Wrapper Scope

`wails-pkceflow` is a thin Wails v3 adapter around `go-pkceflow`.

It owns:

- Wails service lifecycle integration.
- Deferred event bridge from core `oidcauth:*` events to Wails app events.
- Frontend-safe results and DTOs.
- Deep-link delivery wiring from Wails launch URL events to core `mobileflow`.
- Wails example app and wrapper-specific documentation.

It must not own:

- OIDC discovery or token exchange logic.
- PKCE generation or validation.
- ID token verification or claims parsing.
- Refresh-loop semantics.
- Token persistence encryption logic.
- Provider-specific OAuth workarounds that belong in core config/options.

If a change needs OIDC behavior, implement or fix it in `go-pkceflow` first,
then adapt the wrapper only as needed.

## Binding Surface Rules

- Never expose raw tokens to the frontend.
- Frontend-bound methods should return structured, inspectable results such as
  `AuthResult`, not raw Go errors or tokens.
- Keep `Client()` for Go-side API calls only. Avoid binding it directly to the
  frontend surface in examples.
- Prefer thin app-level delegators in examples when needed to hide unsafe or
  irrelevant backend methods from generated Wails bindings.

## Wails Integration Rules

- Keep the wrapper aligned with Wails v3 APIs used by the repository.
- Treat Wails v3 beta as pre-release. Beta.2 declares the desktop API stable,
  but verify against the pinned version before making API assumptions.
- Keep mobile support conservative: Wails host-level deep-link delivery remains
  experimental and must be proven on an emulator or device.
- Do not add application-specific behavior to the wrapper.
- Keep lifecycle methods predictable:
  - startup wires events, restores session, starts refresh loop, and optionally
    initializes discovery
  - shutdown stops refresh loop
  - pause/resume controls refresh loop for mobile foreground/background

## Testing

Use core `oidctest` helpers for wrapper tests when possible. Wrapper tests should
verify adapter behavior and frontend-safe results, not duplicate core OIDC tests.

Common checks:

```bash
go test ./...
go vet ./...
```

For example app work, remember that `examples/wails-desktop` is a nested module.
Depending on the local workspace, run it with `GOWORK=off` or include the nested
module in the dev `go.work`.

## Cross-Repo Dependency Notes

- Wrapper module currently depends on `go-pkceflow`.
- Example app may pin a newer core release than the wrapper module itself.
- When changing core APIs, update wrapper usage, wrapper docs, and example app
  pins deliberately.

## Wrapper Council Concerns

For non-trivial wrapper work, apply the shared council workflow plus these
wrapper-specific lenses:

- Wails binding safety: no tokens, no accidental backend-only methods exposed.
- Wails lifecycle correctness: startup, shutdown, pause, resume.
- Event ordering: deferred events flush correctly once Wails app is available.
- Mobile delivery: launch URL handling reaches `mobileflow.DeliverURL` without
  blocking or panicking.
- Example quality: examples should hide unsafe surfaces and remain easy to run
  with the dockerized Keycloak demo.
