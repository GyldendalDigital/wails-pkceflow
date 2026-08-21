// Vanilla ES module. No bundler, no framework, no generated bindings. The Wails
// runtime is served at /wails/runtime.js; we use Events.On to receive backend
// auth events and Call.ByID to invoke bound Go methods.
import * as wails from "/wails/runtime.js";

// Bound method IDs for wailspkceflow.FrontendService, taken from the output of
// `wails3 generate bindings` (each ID is a stable hash of the fully-qualified
// method name). Regenerate and update these if you rename a method, the service
// type, or the package. Using Call.ByID keeps the frontend free of generated
// binding files and bare-specifier imports.
const METHOD = {
  Login: 2989419781,
  Logout: 3735631246,
  AuthStatus: 3317434184,
  Claims: 426501781,
  // IsAuthenticated: 2815496363, // also bound; this demo uses AuthStatus
};
const call = (name, ...args) => wails.Call.ByID(METHOD[name], ...args);

const $ = (id) => document.getElementById(id);

// ---------------------------------------------------------------------------
// Notification center
// ---------------------------------------------------------------------------
function notify({ title, message = "", kind = "info", timeout = 9000 }) {
  const el = document.createElement("div");
  el.className = `toast toast-${kind}`;

  const body = document.createElement("div");
  body.className = "toast-body";
  const t = document.createElement("div");
  t.className = "toast-title";
  t.textContent = title;
  const m = document.createElement("div");
  m.className = "toast-msg";
  m.textContent = message;
  body.append(t, m);

  const close = document.createElement("button");
  close.className = "toast-close";
  close.setAttribute("aria-label", "Dismiss");
  close.textContent = "\u00d7";
  const dismiss = () => el.remove();
  close.addEventListener("click", dismiss);

  el.append(body, close);
  $("notifications").prepend(el);
  if (timeout) setTimeout(dismiss, timeout);
}

// ---------------------------------------------------------------------------
// Auth state rendering
// ---------------------------------------------------------------------------
async function refreshStatus() {
  let status;
  try {
    status = await call("AuthStatus");
  } catch (e) {
    setStatus("off", "Backend unavailable");
    return;
  }

  if (status.valid) {
    setStatus("ok", "Signed in");
    await showClaims();
    toggleAuthed(true);
  } else if (status.grace_mode) {
    setStatus("grace", `Grace mode (${status.grace_days_left} day(s) left)`);
    await showClaims();
    toggleAuthed(true);
  } else {
    setStatus("off", "Not signed in");
    $("claimsSection").hidden = true;
    toggleAuthed(false);
  }
}

function setStatus(kind, text) {
  const dot = $("statusDot");
  dot.className = `dot dot-${kind}`;
  $("statusText").textContent = text;
}

function toggleAuthed(authed) {
  $("loginBtn").hidden = authed;
  $("logoutBtn").hidden = !authed;
  $("loginBtn").disabled = false;
  $("logoutBtn").disabled = false;
}

async function showClaims() {
  let result;
  try {
    result = await call("Claims");
  } catch {
    return;
  }
  // Claims() returns (ClaimsDTO, AuthResult); Wails delivers multiple returns
  // as an array. Be tolerant if a single object is returned instead.
  const claims = Array.isArray(result) ? result[0] : result;
  if (!claims || !claims.subject) return;

  const dl = $("claims");
  dl.replaceChildren();
  const rows = [
    ["Subject", claims.subject],
    ["Name", claims.name],
    ["Username", claims.preferredUsername],
    ["Email", claims.email],
    ["Issuer", claims.issuer],
    ["Expires", claims.expiresAt ? new Date(claims.expiresAt).toLocaleString() : ""],
  ];
  for (const [k, v] of rows) {
    if (!v) continue;
    const dt = document.createElement("dt");
    dt.textContent = k;
    const dd = document.createElement("dd");
    dd.textContent = v;
    dl.append(dt, dd);
  }
  $("claimsSection").hidden = false;
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------
$("loginBtn").addEventListener("click", async () => {
  $("loginBtn").disabled = true;
  const r = await call("Login");
  if (!r.ok && r.code !== "cancelled") {
    notify({ title: "Login failed", message: `${r.code}: ${r.message}`, kind: "err" });
  }
  $("loginBtn").disabled = false;
  refreshStatus();
});

$("logoutBtn").addEventListener("click", async () => {
  $("logoutBtn").disabled = true;
  await call("Logout");
  $("logoutBtn").disabled = false;
  refreshStatus();
});

// ---------------------------------------------------------------------------
// Backend auth events -> notifications
// ---------------------------------------------------------------------------
const on = (name, handler) => wails.Events.On(name, handler);

on("oidcauth:logged-in", () => {
  notify({ title: "Logged in", message: "Session established.", kind: "ok" });
  refreshStatus();
});
on("oidcauth:logged-out", () => {
  notify({ title: "Logged out", message: "Local session cleared.", kind: "info" });
  refreshStatus();
});
on("oidcauth:token-refreshed", () => {
  notify({ title: "Token refreshed", message: "Access token renewed in the background.", kind: "ok", timeout: 5000 });
});
on("oidcauth:session-expired", () => {
  // Fires as soon as the provider refuses the refresh token, which includes a
  // deliberate revocation - the grace period no longer defers this.
  notify({ title: "Session expired", message: "Refresh token no longer valid. Please log in again.", kind: "err", timeout: 0 });
  refreshStatus();
});
on("oidcauth:init-failed", () => {
  notify({ title: "Discovery failed", message: "Could not reach the IdP. Is Keycloak running on :8080?", kind: "warn", timeout: 0 });
});

// Initial render. This is load-bearing, not decorative: oidcauth:session-expired
// is emitted at most once per refusal and can fire during ServiceStartup, before
// this script has run and attached listeners, so a frontend that only listens for
// the event can miss it permanently. AuthStatus() on mount is the authoritative
// signal; the event is only for reacting promptly while the app is running.
refreshStatus();
