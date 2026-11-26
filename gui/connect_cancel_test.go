package main

// Cancelling a connect and reconnecting are frontend wiring over a backend
// that can already do both, so the contract pinned here is the wiring:
// which lever the button pulls, and what the store does with the answer.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readGuiFile reads a file from the GUI package's own directory — the Go
// side of a contract that has a frontend half.
func readGuiFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// TestConnectButtonTurnsIntoCancel: a connect can legitimately take half a
// minute — Aether waits 30s for a usable port and a Psiphon-fronted chain
// waits 120s for its tunnel — and until now the button that said
// "Connecting…" was also disabled, so the only way out was to wait it out
// or kill the app. The abort itself already existed: App.Disconnect bumps
// the core manager's epoch, which is exactly what makes a Start in flight
// return the abort error and reap the half-built engine (that is how a
// mid-connect Disconnect has always been honoured). What was missing was
// that the UI could not reach it, because the store's disconnect() refuses
// while busy and busy is precisely the state a connect leaves it in.
//
// So cancelConnect must call backend.disconnect() itself rather than go
// through the store's disconnect(), and the card must show Cancel and keep
// the button clickable while the connect is in flight.
func TestConnectButtonTurnsIntoCancel(t *testing.T) {
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))

	fn := regexp.MustCompile(`(?s)async function cancelConnect\(\).*?\n  \}`).FindString(store)
	if fn == "" {
		t.Fatal("connection.ts defines no cancelConnect(); the connecting button has nothing to call")
	}
	if !strings.Contains(fn, "backend.disconnect()") {
		t.Error("cancelConnect() does not call backend.disconnect(), which is the lever that aborts a Start in flight")
	}
	// The refusal in disconnect() is the bug; re-entering it from the
	// cancel path would reproduce it.
	if regexp.MustCompile(`(?s)await disconnect\(`).FindString(fn) != "" {
		t.Error("cancelConnect() routes through the store's disconnect(), which refuses while a connect holds busy")
	}
	if regexp.MustCompile(`(?m)^\s*cancelConnect,`).FindString(store) == "" {
		t.Error("the connection store does not return cancelConnect")
	}
	// The button needs one flag that means "a connect the user could still
	// abort is in flight": busy also covers Disconnect, and connectBusy
	// alone lags the click by up to the 1s status poll.
	for _, want := range []string{"const connecting = computed(", "cancelling"} {
		if !strings.Contains(store, want) {
			t.Errorf("connection.ts exposes no %q for the button to read", want)
		}
	}

	card := readFrontendSrc(t, filepath.Join("components", "ConnectionCard.vue"))
	if !strings.Contains(card, "connection.cancelConnect()") {
		t.Error("the connection card never calls cancelConnect()")
	}
	if regexp.MustCompile(`'Cancel'`).FindString(card) == "" {
		t.Error("the connect button never offers the Cancel label")
	}
	// A Cancel that greys out the moment it is needed is no cancel: the
	// disabled expression has to stop treating an in-flight connect as a
	// reason to block the button.
	toggle := regexp.MustCompile(`(?s)async function toggle\(\).*?\n\}`).FindString(card)
	if toggle == "" {
		t.Fatal("ConnectionCard.vue no longer defines toggle(); update this test with the handler")
	}
	if !strings.Contains(toggle, "connection.connecting") {
		t.Error("toggle() does not branch on connection.connecting, so a press while connecting is still swallowed")
	}
	if i, j := strings.Index(toggle, "connection.connecting"), strings.Index(toggle, "if (connectDisabled"); i < 0 || j < 0 || i > j {
		t.Errorf("toggle() must branch on connection.connecting before its connectDisabled guard:\n%s", toggle)
	}
}

// TestCancelledConnectIsNotAFailure: pulling the abort makes App.Connect
// return the epoch-abort error, so the frontend's awaited connect rejects
// with the backend's wording. That sentence is what the user asked for, not
// a fault — reporting it put "connect aborted (concurrent stop)" in red
// under a button the user had just pressed, and worse, it ran the
// elevation heuristic over it. Both halves are pinned: the store must
// swallow the rejection it caused itself, and it must key off the same
// words the manager actually emits, which is why the Go side is read here
// rather than paraphrased.
func TestCancelledConnectIsNotAFailure(t *testing.T) {
	cm := readGuiFile(t, "core_manager.go")
	abort := regexp.MustCompile(`"(connect aborted[^"]*)"`).FindString(cm)
	if abort == "" {
		t.Fatal("core_manager.go no longer aborts a superseded connect with a \"connect aborted…\" error")
	}
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))
	if !strings.Contains(store, strings.Trim(abort, `"`)) {
		t.Errorf("nothing in connection.ts mentions %s: the cancel the user pressed arrives as that error", abort)
	}

	// The connect handler is shared now (a Reconnect raises the tunnel
	// through the same one), so the cancel-awareness is pinned where the
	// awaited call actually happens rather than at the one-line wrapper.
	connect := regexp.MustCompile(`(?s)async function connect\(id: string\).*?\n  \}`).FindString(store)
	if connect == "" || !strings.Contains(connect, "runConnect(") {
		t.Fatalf("connection.ts no longer routes connect(id) through its shared connect handler:\n%s", connect)
	}
	run := regexp.MustCompile(`(?s)async function runConnect\(.*?\n  \}`).FindString(store)
	if run == "" {
		t.Fatal("connection.ts defines no runConnect(); the connect handler was expected there")
	}
	msgAt := strings.Index(run, "const msg = String(e)")
	elevateAt := strings.Index(run, "requires administrator rights")
	abortAt := strings.Index(run, "connect aborted")
	if msgAt < 0 || elevateAt < 0 || abortAt < 0 {
		t.Fatalf("the connect handler no longer reads like the cancel-aware handler: msg=%d elevate=%d abort=%d", msgAt, elevateAt, abortAt)
	}
	if elevateAt < abortAt {
		t.Error("connect() runs the elevation heuristic before it recognises its own cancel")
	}
	// And the aborted error must not be handed to error.value: the row
	// above the button renders it as the failure it is not.
	setAt := strings.Index(run, "error.value = msg")
	if setAt < 0 {
		t.Fatal("connect() no longer reports failures through error.value; this test follows that line")
	}
	if setAt < abortAt {
		t.Error("connect() publishes the error before checking whether it was the user's cancel")
	}
}

// TestReconnectRestartsTheSameProfile: the ask is one explicit control for
// "the tunnel is up but wedged" — no automatic retry on a drop, no reuse of
// an Aether/Psiphon/Tor endpoint that worked last time. Reconnect is still
// exactly the two actions the app already has, in the only order that
// works: Stop first, because a second Connect while the engine is alive
// would have to take its ports. That order used to be the store's
// disconnect(); connect(id); it is App.Reconnect's now, because a chain leg
// the next connect is meant to inherit cannot survive a teardown the client
// makes in its own call (see reconnect.go). Everything else the legs
// remember (the stored profile, its chain, its Psiphon/Tor options) is
// re-read by Connect on the way back up, which is the "last setting" the
// report asked for.
func TestReconnectRestartsTheSameProfile(t *testing.T) {
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))

	fn := regexp.MustCompile(`(?s)async function reconnect\(id: string\).*?\n  \}`).FindString(store)
	if fn == "" {
		t.Fatal("connection.ts defines no reconnect(id); the Reconnect button has nothing to call")
	}
	if !strings.Contains(fn, "backend.reconnect") {
		t.Fatalf("reconnect() does not call the backend's Reconnect:\n%s", fn)
	}
	if strings.Contains(fn, "await disconnect()") {
		t.Error("reconnect() disconnects client-side, which reaps a chain leg sticky mode promised to hand over")
	}
	if regexp.MustCompile(`(?m)^\s*reconnect,`).FindString(store) == "" {
		t.Error("the connection store does not return reconnect")
	}

	app := readGuiFile(t, "app.go")
	rpc := regexp.MustCompile(`(?s)func \(a \*App\) Reconnect\(id string\) error \{.*?\n\}`).FindString(app)
	if rpc == "" {
		t.Fatal("app.go defines no Reconnect(id); the store's call has no backend half")
	}
	stop, start := strings.Index(rpc, "a.disconnectCores("), strings.Index(rpc, "a.Connect(id)")
	if stop < 0 || start < 0 {
		t.Fatalf("App.Reconnect is not stop-then-connect:\n%s", rpc)
	}
	if stop > start {
		t.Error("App.Reconnect connects before disconnecting, so it races the running engine for its ports")
	}

	card := readFrontendSrc(t, filepath.Join("components", "ConnectionCard.vue"))
	if !strings.Contains(card, "connection.reconnect(") {
		t.Error("the connection card renders no Reconnect control")
	}
	if regexp.MustCompile(`\bReconnect\b`).FindString(card) == "" {
		t.Error("the Reconnect control has no label a user can read")
	}
}
