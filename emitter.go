package wailspkceflow

import (
	pkceflow "github.com/GyldendalDigital/go-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// appEmitter forwards go-pkceflow auth events to Wails application events. It is
// the target set on the deferred event bus once the Wails app is available.
type appEmitter struct {
	app *application.App
}

// compile-time check that appEmitter satisfies the core emitter interface.
var _ pkceflow.EventEmitter = (*appEmitter)(nil)

// Emit forwards a named event and its data to all Wails listeners.
func (e *appEmitter) Emit(event string, data any) {
	e.app.Event.Emit(event, data)
}
