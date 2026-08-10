// Command wails-desktop is a minimal Wails v3 desktop app demonstrating
// wails-pkceflow: OIDC PKCE login/logout, live auth status, ID token claims,
// and a dismissable notification center driven by the backend auth events.
//
// Run the dockerized Keycloak in ./keycloak first (see README), then:
//
//	go run .
//
// Log in with demo / demo. The demo realm issues a 3-minute access token so
// the background refresher visibly cycles (watch for oidcauth:token-refreshed
// notifications roughly every 90 seconds).
package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/GyldendalDigital/go-pkceflow/desktopflow"
	"github.com/GyldendalDigital/go-pkceflow/filestore"
	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend
var assetsFS embed.FS

// These values match the pre-baked demo realm in ./keycloak/demo-realm.json.
// Override PKCEFLOW_ISSUER to point at a remote Keycloak (e.g., via VirtualBox
// NAT: PKCEFLOW_ISSUER=http://10.0.2.2:8080/realms/demo).
const (
	defaultIssuerURL = "http://localhost:8080/realms/demo"
	clientID         = "demo-native"
	callbackPort     = 34115 // desktopflow listens on http://127.0.0.1:34115/callback
	appID            = "go-pkceflow-demo"
)

func main() {
	issuer := defaultIssuerURL
	if env := os.Getenv("PKCEFLOW_ISSUER"); env != "" {
		issuer = env
	}

	// NewDefault hides the per-platform config-directory resolution.
	store, err := filestore.NewDefault(appID)
	if err != nil {
		log.Fatalf("token store: %v", err)
	}

	flow := desktopflow.New(callbackPort)
	if err := flow.SetLogoutPath("/logout-callback"); err != nil {
		log.Fatalf("configure logout callback path: %v", err)
	}

	authSvc, err := wailspkceflow.New(wailspkceflow.Options{
		Config: pkceflow.Config{
			IssuerURL: issuer,
			ClientID:  clientID,
		},
		Flow:     flow,
		Store:    store,
		AutoInit: true, // run OIDC discovery in the background on startup
	})
	if err != nil {
		log.Fatalf("auth service: %v", err)
	}

	assets, err := fs.Sub(assetsFS, "frontend")
	if err != nil {
		log.Fatalf("assets: %v", err)
	}

	app := application.New(application.Options{
		Name: "go-pkceflow demo",
		Services: []application.Service{
			application.NewService(authSvc.Frontend()),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "go-pkceflow demo",
		Width:  520,
		Height: 760,
	})

	if err := app.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}
