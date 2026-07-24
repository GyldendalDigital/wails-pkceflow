// Package wailspkceflow adapts the framework-agnostic go-pkceflow OIDC client to
// a Wails v3 service.
//
// It provides a single facade, AuthService, that owns a *pkceflow.Client, bridges
// the client's auth lifecycle events to Wails application events, manages the
// background token refresh loop across the Wails service lifecycle, and (on
// mobile) routes deep-link callbacks into the client's flow handler.
//
// Usage:
//
//	handler := desktopflow.New(15051) // or mobileflow.New(uri, openURL)
//	store, _ := filestore.New("com.example.myapp", configDir)
//
//	authSvc, _ := wailspkceflow.New(wailspkceflow.Options{
//	    Config:   pkceflow.Config{IssuerURL: "https://idp.example.com", ClientID: "my-app"},
//	    Flow:     handler,
//	    Store:    store,
//	    AutoInit: true,
//	})
//
//	app := application.New(application.Options{Name: "My App"})
//	app.RegisterService(application.NewService(authSvc))
//
//	client := authSvc.Client() // client.TokenFn(ctx) for the app's API calls
//
// The frontend calls the bound methods Login, Logout, AuthStatus, Claims, and
// IsAuthenticated. Access, ID, and refresh tokens are never exposed to the
// frontend; the Go backend performs authenticated requests via the client.
package wailspkceflow
