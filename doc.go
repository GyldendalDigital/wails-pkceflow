// Package wailspkceflow adapts the framework-agnostic go-pkceflow OIDC client to
// a Wails v3 service.
//
// AuthService owns a *pkceflow.Client, bridges the client's auth lifecycle
// events to Wails application events, manages the background token refresh loop
// across the Wails service lifecycle, automatically pauses and resumes refresh
// work for Android/iOS background and foreground events, and (on mobile)
// subscribes to Wails launch-URL events and forwards them into the client's flow
// handler. Native Android/iOS event production remains the Wails host's
// responsibility. When Flow implements URLDeliverer, as mobileflow.Handler
// does, New wires event forwarding automatically.
//
// Applications register AuthService.Frontend with Wails. That dedicated
// service exposes only Login, Logout, AuthStatus, IsAuthenticated, Claims, and
// RestoreStatus to generated bindings. AuthService.Client, Pause, and Resume
// remain available only to Go code. Operational persistence Load errors can be
// handled through a Go-only callback or strict startup policy; no backend error
// text crosses the frontend boundary.
//
// Usage:
//
//	handler := desktopflow.New(15051) // or mobileflow.New(uri, openURL)
//	store, err := filestore.NewDefault("com.example.myapp")
//	if err != nil {
//	    return err
//	}
//
//	authSvc, err := wailspkceflow.New(wailspkceflow.Options{
//	    Config:     pkceflow.Config{IssuerURL: "https://idp.example.com", ClientID: "my-app"},
//	    Flow:       handler,
//	    Store:      store,
//	    AutoInit:   true,
//	    HTTPClient: &http.Client{Timeout: 30 * time.Second}, // the default client has none
//	})
//	if err != nil {
//	    return err
//	}
//
//	client := authSvc.Client() // client.TokenFn(ctx) for the app's API calls
//
//	// Bind the library-provided frontend-safe service.
//	app := application.New(application.Options{Name: "My App"})
//	app.RegisterService(application.NewService(authSvc.Frontend()))
//
// Access, ID, and refresh tokens stay in the Go backend.
package wailspkceflow
