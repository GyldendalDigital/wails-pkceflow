# Contributing to wails-pkceflow

Thank you for your interest in contributing to wails-pkceflow. This document explains how to get involved.

## Getting Started

1. Fork the repository
2. Clone your fork locally
3. Create a feature branch from `development`
4. Make your changes
5. Submit a pull request targeting `development`

## Branch Model

This project uses a Gitflow variant:

- `master` - production releases (tagged `v*`)
- `development` - integration branch, all PRs target here
- `feature/*` - new functionality
- `fix/*` - bug fixes
- `chore/*` - maintenance (deps, CI, tooling)

## Commit Messages

Format: `type(scope): description`

Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`

Examples:
```
feat(service): add ServiceStartup lifecycle hook
fix(events): flush deferred events on SetApp
chore: bump go-pkceflow dependency
```

## Development Requirements

- Go 1.26 or later
- Wails v3 CLI (for building example apps)

## Running Tests

```bash
go test ./...
```

## Code Guidelines

- This is a thin integration layer between go-pkceflow and Wails v3
- No OIDC/OAuth logic that belongs in the core library
- No application-specific logic that belongs in the end developer's Wails app
- The scope is strictly: service lifecycle, event bridging, and deep link routing
- Follow the same conventions as go-pkceflow (see [go-pkceflow CONTRIBUTING](https://github.com/GyldendalDigital/go-pkceflow/blob/master/CONTRIBUTING.md))

## Security

If you discover a security vulnerability, please report it privately via GitHub's security advisory feature rather than opening a public issue.

## Pull Request Process

1. Ensure all tests pass (`go test ./...`)
2. Ensure code passes `go vet ./...`
3. Update documentation if your change affects the public API
4. Fill out the PR template completely
5. One logical change per PR

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
