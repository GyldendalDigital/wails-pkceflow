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
- Startup restore-error policy, Go-only reporting, and latched frontend-safe
  restore status.
- Subscription and forwarding from Wails launch URL events to core
  `mobileflow`. Native Android/iOS event production is owned by Wails.
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
- Keep `Client()`, `Pause()`, and `Resume()` for Go-side use only. Register
  `AuthService.Frontend()` with Wails so generated bindings contain only the
  library's explicitly frontend-safe methods.
- Keep the `FrontendService` method surface minimal and test its exact Wails
  bindings whenever it changes.
- Keep `RestoreStatus` a closed frontend-safe outcome with no native error text;
  restore errors and their unwrap chain remain Go-only.

## Wails Integration Rules

- Keep the wrapper aligned with Wails v3 APIs used by the repository.
- Treat Wails v3 beta as pre-release. Beta.2 declares the desktop API stable,
  but verify against the pinned version before making API assumptions.
- Keep mobile support conservative: the wrapper adapter is implemented and
  unit-tested, but Wails v3.0.0-beta.2 does not produce the required mobile
  launch-URL events. Wrapper issue #8 validates that Wails-specific host path;
  it does not block core or desktop dogfooding and must not drive core changes.
- Do not implement upstream Wails changes, pin an unreleased Wails fork, or
  start emulator work for issue #8 without an explicit maintainer request.
- Do not add application-specific behavior to the wrapper.
- Keep lifecycle methods predictable:
  - startup wires events, restores session, starts refresh loop, and optionally
    initializes discovery
  - strict restore failure happens before installing subscriptions, refresh
    work, the event target, or discovery; continue mode is the default
  - Android/iOS background and foreground events pause/resume refresh work
    automatically; desktop builds register no mobile lifecycle events
  - shutdown stops refresh work and removes all service-owned subscriptions
  - startup after activation and stale pause/resume/shutdown transitions are
    no-ops; overlapping startup returns `ErrServiceStartupInProgress`
  - backend-only `Pause`/`Resume` remain available for explicit application
    policy; manual and mobile lifecycle pause reasons compose
  - while the service is active, consumers must not call the core client's
    refresh-loop controls directly
- Wails beta.2 does not preserve application-event source order and has an
  upstream listener dispatch/unsubscribe race. Keep wrapper state
  generation-guarded and do not claim stronger native ordering or teardown
  guarantees until Wails provides them.

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
- Wrapper restore-error handling requires core `v0.9.0-beta.7` or newer.
- `Options.HTTPClient` passes through to `pkceflow.WithHTTPClient`, which
  requires core `v0.9.0-beta.2` or newer.
- The documented grace, session-expired timing, and logout revocation semantics
  require core `v0.9.0-beta.11` or newer.
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
