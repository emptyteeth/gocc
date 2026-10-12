package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
)

// These tests drive the page in a real browser, over a real HTTP server,
// against a device that is not real. They check what a Go test of the
// handler cannot: that the page renders, that its clicks reach the API,
// and above all that a state arriving while the playlist is scrolled or
// the volume knob is held does not rebuild those elements under the user.
//
// They need chromedriver and a Chromium it can drive. Where either is
// missing, they say so and pass, so that a machine without a browser can
// still run the rest of the suite.

// newBrowserServer starts an HTTP server over an engine whose device is a
// fake, and returns its address and the device.
func newBrowserServer(t *testing.T, device *fakeDevice, entries int) string {
	t.Helper()

	engine, err := core.New(core.Options{
		DataDir: t.TempDir(),
		NewController: func(_ cast.Device, onChange func(cast.Status, error)) (core.Controller, error) {
			device.onChange = onChange
			return device, nil
		},
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice})
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)

	for i := range entries {
		entry := playlist.Entry{
			Name: fmt.Sprintf("station%d", i+1),
			URL:  fmt.Sprintf("http://radio%d.example/stream.mp3", i+1),
		}
		if err := engine.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool { return !engine.Snapshot().Connecting })

	server := httptest.NewServer(New(engine, Options{}).Handler())
	t.Cleanup(server.Close)
	return server.URL
}

// driver is a browser driven over chromedriver's HTTP API, which is the
// WebDriver protocol.
type driver struct {
	t     *testing.T
	cmd   *exec.Cmd
	port  int
	sess  string
	alive bool
}

// newDriver starts chromedriver and a browser, or skips the test when
// either is missing.
func newDriver(t *testing.T) *driver {
	t.Helper()
	path, err := exec.LookPath("chromedriver")
	if err != nil {
		t.Skip("chromedriver is not installed; skipping the browser test")
	}

	// A port the driver can have: take one the kernel gives and give it
	// back, which is enough for a test that runs alone.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	cmd := exec.Command(path, fmt.Sprintf("--port=%d", port))
	if err := cmd.Start(); err != nil {
		t.Skipf("chromedriver did not start: %v", err)
	}

	d := &driver{t: t, cmd: cmd, port: port}
	t.Cleanup(d.stop)

	// Wait for the driver to answer.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := d.call("GET", "/status", nil); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Start a browser. Headless, so that it needs no display.
	var out struct {
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	err = d.callInto("POST", "/session", map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"browserName": "chrome",
				"goog:chromeOptions": map[string]any{
					"args": []string{
						"--headless=new", "--no-sandbox", "--disable-gpu",
						"--disable-dev-shm-usage", "--window-size=1280,560",
					},
				},
			},
		},
	}, &out)
	if err != nil {
		t.Skipf("a browser could not be started: %v", err)
	}
	if out.Value.SessionID == "" {
		t.Skip("the browser gave no session")
	}
	d.sess, d.alive = out.Value.SessionID, true
	return d
}

func (d *driver) stop() {
	if d.alive {
		d.call("DELETE", "/session/"+d.sess, nil)
	}
	if d.cmd.Process != nil {
		d.cmd.Process.Kill()
		d.cmd.Wait()
	}
}

// call sends a request to the driver and returns what it said.
func (d *driver) call(method, path string, body any) (map[string]any, error) {
	var out map[string]any
	err := d.callInto(method, path, body, &out)
	return out, err
}

func (d *driver) callInto(method, path string, body, into any) error {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", d.port, path), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("the driver answered %d", res.StatusCode)
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(into)
}

// eval runs JavaScript in the page and returns what it produced.
func (d *driver) eval(script string) any {
	d.t.Helper()
	var out struct {
		Value any `json:"value"`
	}
	if err := d.callInto("POST", "/session/"+d.sess+"/execute/sync",
		map[string]any{"script": script, "args": []any{}}, &out); err != nil {
		d.t.Fatalf("running %q: %v", firstLine(script), err)
	}
	return out.Value
}

// evalString runs JavaScript that returns a string.
func (d *driver) evalString(script string) string {
	d.t.Helper()
	value := d.eval(script)
	if value == nil {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		d.t.Fatalf("%q returned %T, wanted a string", firstLine(script), value)
	}
	return s
}

// evalBool runs JavaScript that returns a boolean.
func (d *driver) evalBool(script string) bool {
	d.t.Helper()
	value, ok := d.eval(script).(bool)
	if !ok {
		d.t.Fatalf("%q returned %T, wanted a boolean", firstLine(script), value)
	}
	return value
}

// goTo loads a page.
func (d *driver) goTo(url string) {
	d.t.Helper()
	if err := d.callInto("POST", "/session/"+d.sess+"/url", map[string]any{"url": url}, nil); err != nil {
		d.t.Fatalf("loading %s: %v", url, err)
	}
}

// waitFor polls JavaScript until it is true, or fails the test.
func (d *driver) waitFor(script, what string) {
	d.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if d.evalBool(script) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	d.t.Fatalf("timed out waiting for %s", what)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// TestBrowserPage drives the whole page: it renders, its clicks reach the
// API, and a state arriving does not disturb what the user is doing.
func TestBrowserPage(t *testing.T) {
	device := &fakeDevice{}
	base := newBrowserServer(t, device, 12)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#devices li').length > 0", "the devices")
	d.waitFor("return document.querySelectorAll('#playlist li').length === 12", "the playlist")

	if got := d.evalString("return document.getElementById('header-status').textContent"); !strings.Contains(got, "device") {
		t.Errorf("the header says %q", got)
	}
	if got := d.evalString("return document.querySelector('#playlist li .name').textContent.trim()"); got != "station1" {
		t.Errorf("the first row is %q", got)
	}

	// A click chooses the row, through the API.
	d.eval("document.querySelectorAll('#playlist li')[1].click()")
	d.waitFor("return document.querySelectorAll('#playlist li.chosen').length === 1", "a chosen row")
	if got := d.evalString("return document.querySelector('#playlist li.chosen .name').textContent.trim()"); got != "station2" {
		t.Errorf("the chosen row is %q, want station2", got)
	}

	// The controls the user is holding are not replaced by a state push,
	// which is asked for by choosing another row.
	d.eval("window.__slider = document.querySelector('.volume input[type=range]')")
	d.eval("window.__mute = document.querySelector('.check input')")
	if d.eval("return window.__slider") == nil {
		t.Fatal("there is no volume slider to hold")
	}
	d.eval("document.querySelectorAll('#playlist li')[0].click()")
	time.Sleep(600 * time.Millisecond)
	if !d.evalBool("return document.querySelector('.volume input[type=range]') === window.__slider") {
		t.Error("a state push replaced the volume slider")
	}
	if !d.evalBool("return document.querySelector('.check input') === window.__mute") {
		t.Error("a state push replaced the mute box")
	}

	// A scrolled playlist keeps its place through a state push.
	d.eval("document.getElementById('playlist').scrollTop = 120")
	before := d.evalString("return String(document.getElementById('playlist').scrollTop)")
	if before == "0" {
		t.Fatal("the playlist did not scroll; the check would prove nothing")
	}
	d.eval("document.querySelectorAll('#playlist li')[3].click()")
	time.Sleep(500 * time.Millisecond)
	if after := d.evalString("return String(document.getElementById('playlist').scrollTop)"); after != before {
		t.Errorf("the playlist moved from %s to %s on a state push", before, after)
	}

	// A slider being dragged is not moved by a state push.
	d.eval(`
		const r = document.querySelector('.volume input[type=range]');
		r.dragging = true;
		r.value = '0.9';
		r.dispatchEvent(new Event('input', {bubbles: true}));
	`)
	d.eval("document.querySelectorAll('#playlist li')[0].click()")
	time.Sleep(500 * time.Millisecond)
	if got := d.evalString("return document.querySelector('.volume input[type=range]').value"); got != "0.9" {
		t.Errorf("a state push moved the slider being dragged to %s", got)
	}

	// A mistake shows, and does not replace the controls either.
	d.eval(`
		document.getElementById('add-name').value = 'bad';
		document.getElementById('add-url').value = 'not a url';
		document.getElementById('add').dispatchEvent(
			new Event('submit', {bubbles: true, cancelable: true}));
	`)
	d.waitFor("return document.querySelector('.error') !== null", "the error box")
	if !d.evalBool("return document.querySelector('.volume input[type=range]') === window.__slider") {
		t.Error("the error replaced the volume slider")
	}

	// Double-clicking an entry plays it, once. The mark appears on the row
	// that plays, which is the one double-clicked.
	d.eval(`
		const li = document.querySelectorAll('#playlist li')[1];
		li.dispatchEvent(new MouseEvent('dblclick', {bubbles: true}));
	`)
	d.waitFor("return document.querySelectorAll('#playlist li')[1].querySelector('.name').textContent.includes('▶')",
		"the playing mark")
	waitFor(t, func() bool { return len(device.playedURLs()) == 1 })
	if got := device.playedURLs()[0]; got != "http://radio2.example/stream.mp3" {
		t.Errorf("played %q, want the second entry", got)
	}

	// Playing again through the transport reaches the device.
	d.eval("document.querySelector('.transport button').click()")
	waitFor(t, func() bool { return device.pauseCount() == 1 })
}

// TestBrowserBasicAuth checks that the page asks for the credentials a
// browser can give, and that the wrong ones are refused.
func TestBrowserBasicAuth(t *testing.T) {
	device := &fakeDevice{}
	engine, err := core.New(core.Options{
		DataDir: t.TempDir(),
		NewController: func(_ cast.Device, onChange func(cast.Status, error)) (core.Controller, error) {
			device.onChange = onChange
			return device, nil
		},
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice})
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool { return !engine.Snapshot().Connecting })

	server := httptest.NewServer(New(engine, Options{Username: "alice", Password: "s3cret"}).Handler())
	t.Cleanup(server.Close)

	// Without credentials the page is refused, which a browser answers
	// with its own login prompt for a real person.
	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the page was served without credentials: %d", res.StatusCode)
	}
	if got := res.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("the challenge is %q", got)
	}

	// With them, it is served.
	req, _ := http.NewRequest("GET", server.URL+"/api/state", nil)
	req.SetBasicAuth("alice", "s3cret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("the right credentials got %d", ok.StatusCode)
	}
	var state stateDTO
	if err := json.NewDecoder(ok.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if len(state.Devices) != 1 {
		t.Errorf("the state holds %d devices", len(state.Devices))
	}
}
