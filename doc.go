// Package wailspkceflow adapts the framework-agnostic go-pkceflow OIDC client to
// a Wails v3 service.
//
// It provides a single facade, AuthService, that owns a *pkceflow.Client, bridges
// the client's auth lifecycle events to Wails application events, manages the
// background token refresh loop across the Wails service lifecycle, and (on
// mobile) routes deep-link callbacks into the client's flow handler. When Flow
// implements URLDeliverer, as mobileflow.Handler does, New wires delivery
// automatically.
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
//	    Config:   pkceflow.Config{IssuerURL: "https://idp.example.com", ClientID: "my-app"},
//	    Flow:     handler,
//	    Store:    store,
//	    AutoInit: true,
//	})
//	if err != nil {
//	    return err
//	}
//
//	client := authSvc.Client() // client.TokenFn(ctx) for the app's API calls
//
//	// Bind an app-owned delegator that forwards only frontend-safe methods.
//	app := application.New(application.Options{Name: "My App"})
//	app.RegisterService(application.NewService(&App{auth: authSvc}))
//
// App is a thin service owned by the application. It delegates Login, Logout,
// AuthStatus, Claims, IsAuthenticated, and lifecycle methods to authSvc without
// exposing Client(). See the repository README and desktop example for the
// complete type. Access, ID, and refresh tokens stay in the Go backend.
package wailspkceflow
