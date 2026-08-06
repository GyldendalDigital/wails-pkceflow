package wailspkceflow_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	wailspkceflow "github.com/GyldendalDigital/wails-pkceflow"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var initializeWailsTestApp sync.Once

func TestFrontendReturnsStableService(t *testing.T) {
	svc := newTestService(t)
	want := svc.Frontend()

	const callers = 32
	results := make(chan *wailspkceflow.FrontendService, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- svc.Frontend()
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		if got != want {
			t.Fatal("Frontend returned different service instances")
		}
	}
}

func TestFrontendDelegatesAuthOperations(t *testing.T) {
	svc := newTestService(t)
	frontend := svc.Frontend()
	if err := svc.Client().Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if frontend.IsAuthenticated() {
		t.Fatal("IsAuthenticated returned true before login")
	}
	if result := frontend.Login(); !result.OK {
		t.Fatalf("Login: %+v", result)
	}
	if status := frontend.AuthStatus(); !status.Valid || !status.CanUseApp {
		t.Fatalf("AuthStatus after login = %+v", status)
	}
	claims, result := frontend.Claims()
	if !result.OK || claims.Subject == "" {
		t.Fatalf("Claims after login = (%+v, %+v)", claims, result)
	}
	if result := frontend.Logout(); !result.OK {
		t.Fatalf("Logout: %+v", result)
	}
	if frontend.IsAuthenticated() {
		t.Fatal("IsAuthenticated returned true after logout")
	}
}

func TestFrontendServiceHasExactPublicMethodSurface(t *testing.T) {
	typ := reflect.TypeOf((*wailspkceflow.FrontendService)(nil))
	got := make([]string, 0, typ.NumMethod())
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}

	want := []string{
		"AuthStatus",
		"Claims",
		"IsAuthenticated",
		"Login",
		"Logout",
		"ServiceName",
		"ServiceShutdown",
		"ServiceStartup",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FrontendService methods = %v, want %v", got, want)
	}
}

func TestFrontendServiceWailsBindingSurface(t *testing.T) {
	// Wails beta.2 logs binding registration through its global application.
	initializeWailsTestApp.Do(func() {
		_ = application.New(application.Options{})
	})
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(application.NewService(newTestService(t).Frontend())); err != nil {
		t.Fatalf("add frontend service bindings: %v", err)
	}

	const prefix = "github.com/GyldendalDigital/wails-pkceflow.FrontendService."
	for _, name := range []string{"AuthStatus", "Claims", "IsAuthenticated", "Login", "Logout"} {
		method := bindings.Get(&application.CallOptions{MethodName: prefix + name})
		if method == nil {
			t.Errorf("safe method %s was not bound", name)
			continue
		}
		if got := bindings.GetByID(method.ID); got != method {
			t.Errorf("safe method %s was not addressable by binding ID", name)
		}
	}

	for _, name := range []string{
		"Client",
		"Frontend",
		"Pause",
		"Resume",
		"ServiceName",
		"ServiceShutdown",
		"ServiceStartup",
	} {
		if method := bindings.Get(&application.CallOptions{MethodName: prefix + name}); method != nil {
			t.Errorf("backend-only method %s was bound", name)
		}
	}
}
