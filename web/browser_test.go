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
	"sync"
	"testing"
	"time"

	"github.com/emptyteeth/gocc/cast"
	"github.com/emptyteeth/gocc/core"
	"github.com/emptyteeth/gocc/playlist"
	"github.com/emptyteeth/gocc/radiobrowser"
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
	url, _ := newBrowserServerWith(t, device, entries)
	return url
}

// newBrowserServerWith is newBrowserServer, and returns the engine too.
func newBrowserServerWith(t *testing.T, device *fakeDevice, entries int) (string, *core.Engine) {
	t.Helper()

	engine, err := core.New(core.Options{
		DataDir:  t.TempDir(),
		Stations: newBrowserSource(),
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
		if err := engine.AddFavorite(entry); err != nil {
			t.Fatal(err)
		}
	}
	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool { return !engine.Snapshot().Connecting })

	server := httptest.NewServer(New(engine, Options{}).Handler())
	t.Cleanup(server.Close)
	return server.URL, engine
}

// newBrowserSource is a catalogue for the browser tests, answered without
// a network.
func newBrowserSource() *browserSource {
	return &browserSource{
		countries: []radiobrowser.Country{
			{Name: "Germany", Code: "DE", Stations: 6533},
			{Name: "France", Code: "FR", Stations: 3926},
		},
		stations: []radiobrowser.Station{
			{UUID: "s1", Name: "Rock Antenne", URL: "http://a",
				URLResolved: "https://a/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s2", Name: "Jazz Radio", URL: "http://b",
				URLResolved: "https://b/stream", Codec: "AAC", CountryCode: "DE"},
			{UUID: "s3", Name: "1LIVE", URL: "http://c",
				URLResolved: "https://c/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s4", Name: "WDR 5", URL: "http://d",
				URLResolved: "https://d/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s5", Name: "SWR3", URL: "http://e",
				URLResolved: "https://e/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s6", Name: "Deutschlandfunk", URL: "http://f",
				URLResolved: "https://f/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s7", Name: "Bayern 3", URL: "http://g",
				URLResolved: "https://g/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s8", Name: "NDR 2", URL: "http://h",
				URLResolved: "https://h/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
		},
	}
}

// browserSource is a radio-browser catalogue that answers without a
// network.
type browserSource struct {
	mu        sync.Mutex
	countries []radiobrowser.Country
	stations  []radiobrowser.Station
}

func (f *browserSource) Countries(context.Context) ([]radiobrowser.Country, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]radiobrowser.Country(nil), f.countries...), nil
}

func (f *browserSource) Stations(_ context.Context, q radiobrowser.Query) ([]radiobrowser.Station, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]radiobrowser.Station, 0, len(f.stations))
	for _, st := range f.stations {
		if q.CountryCode != "" && st.CountryCode != q.CountryCode {
			continue
		}
		if q.Name != "" && !strings.Contains(strings.ToLower(st.Name), strings.ToLower(q.Name)) {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// driver is a browser driven over chromedriver's HTTP API, which is the
// WebDriver protocol.
type driver struct {
	t       *testing.T
	cmd     *exec.Cmd
	port    int
	sess    string
	alive   bool
	browser string
}

// newDriver starts chromedriver and a browser, or skips the test when
// either is missing.
func newDriver(t *testing.T) *driver {
	t.Helper()
	return newDriverOf(t, "chrome")
}

// browser is one a test can drive: its driver, and what its session asks
// for. The grid tests run in both, because browsers lay out a square card
// in a grid differently, and one of them has got it wrong before.
type browser struct {
	name      string
	driver    string
	cap       map[string]any
	extraArgs []string
}

var browsers = map[string]browser{
	"chrome": {
		name:   "chrome",
		driver: "chromedriver",
		cap: map[string]any{
			"browserName": "chrome",
			"goog:chromeOptions": map[string]any{
				"args": []string{
					"--headless=new", "--no-sandbox", "--disable-gpu",
					"--disable-dev-shm-usage", "--window-size=1280,560",
				},
			},
		},
	},
	"firefox": {
		name:   "firefox",
		driver: "geckodriver",
		cap: map[string]any{
			"browserName":         "firefox",
			"acceptInsecureCerts": true,
			"moz:firefoxOptions":  map[string]any{"args": []string{"-headless"}},
		},
	},
}

// newDriverOf starts the driver and browser named by name, or skips the
// test when they are not installed.
func newDriverOf(t *testing.T, name string) *driver {
	t.Helper()
	browser, ok := browsers[name]
	if !ok {
		t.Fatalf("unknown browser %q", name)
	}
	path, err := exec.LookPath(browser.driver)
	if err != nil {
		t.Skipf("%s is not installed; skipping the browser test", browser.driver)
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
		t.Skipf("%s did not start: %v", browser.driver, err)
	}

	d := &driver{t: t, cmd: cmd, port: port, browser: name}
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
		"capabilities": map[string]any{"alwaysMatch": browser.cap},
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

// setWindow sets the browser window's size, for a phone layout.
func (d *driver) setWindow(width, height int) {
	d.t.Helper()
	body := map[string]any{
		"width": width, "height": height,
		"x": 0, "y": 0,
	}
	if err := d.callInto("POST", "/session/"+d.sess+"/window/rect", body, nil); err != nil {
		d.t.Fatalf("resizing the window: %v", err)
	}
	// Also tell the page, so that the CSS width matches without waiting
	// for a desktop window manager.
	d.eval("document.documentElement.style.width = '" + fmt.Sprint(width) + "px'")
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
	base, engine := newBrowserServerWith(t, device, 12)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#devices li').length > 0", "the devices")
	d.waitFor("return document.querySelectorAll('#favorites li').length === 12", "the favorites")

	if got := d.evalString("return document.getElementById('header-status').textContent"); !strings.Contains(got, "device") {
		t.Errorf("the header says %q", got)
	}
	if got := d.evalString("return document.querySelector('#favorites li .name').textContent.trim()"); got != "station1" {
		t.Errorf("the first row is %q", got)
	}

	// The controls the user is holding are not replaced by a state push.
	d.eval("window.__slider = document.querySelector('.volume input[type=range]')")
	d.eval("window.__mute = document.querySelector('.check input')")
	if d.eval("return window.__slider") == nil {
		t.Fatal("there is no volume slider to hold")
	}
	// A state push: the favorites pane re-renders from a new state.
	d.eval("document.querySelector('#favorites li .play').click()")
	time.Sleep(600 * time.Millisecond)
	if !d.evalBool("return document.querySelector('.volume input[type=range]') === window.__slider") {
		t.Error("a state push replaced the volume slider")
	}
	if !d.evalBool("return document.querySelector('.check input') === window.__mute") {
		t.Error("a state push replaced the mute box")
	}

	// A scrolled favorites list keeps its place through a state push.
	d.eval("document.getElementById('favorites').scrollTop = 120")
	before := d.evalString("return String(document.getElementById('favorites').scrollTop)")
	if before == "0" {
		t.Fatal("the favorites did not scroll; the check would prove nothing")
	}
	d.eval("document.querySelectorAll('#favorites li')[3].querySelector('.play').click()")
	time.Sleep(500 * time.Millisecond)
	if after := d.evalString("return String(document.getElementById('favorites').scrollTop)"); after != before {
		t.Errorf("the favorites moved from %s to %s on a state push", before, after)
	}

	// A slider being dragged is not moved by a state push.
	d.eval(`
		const r = document.querySelector('.volume input[type=range]');
		r.dragging = true;
		r.value = '0.9';
		r.dispatchEvent(new Event('input', {bubbles: true}));
	`)
	d.eval("document.querySelector('#favorites li .play').click()")
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

	// The Play button of a favorite plays it: the last thing the device
	// was asked for is that row's stream.
	playedBefore := len(device.playedURLs())
	d.eval("document.querySelectorAll('#favorites li')[1].querySelector('.play').click()")
	waitFor(t, func() bool { return len(device.playedURLs()) > playedBefore })
	played := device.playedURLs()
	if got := played[len(played)-1]; got != "http://radio2.example/stream.mp3" {
		t.Errorf("played %q, want the second favorite", got)
	}

	// Playing again through the transport reaches the device.
	device.status.State = cast.StatePlaying
	device.report()
	d.waitFor("return document.querySelector('.transport button').textContent === 'Pause'", "the Pause button")
	d.eval("document.querySelector('.transport button').click()")
	waitFor(t, func() bool { return device.pauseCount() == 1 })

	_ = engine
}

// TestBrowserBrowse drives the catalogue: read it, change the country and
// the order, search, and play and save a station.
func TestBrowserBrowse(t *testing.T) {
	device := &fakeDevice{}
	base, _ := newBrowserServerWith(t, device, 0)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#tabs button').length === 2", "the tabs")
	d.eval("document.getElementById('tab-browse').click()")
	d.waitFor("return document.querySelectorAll('#stations .station').length > 0", "the stations")

	// The station's actions play and save it.
	d.eval("document.querySelectorAll('#stations .station')[0].querySelector('.actions button').click()")
	waitFor(t, func() bool { return len(device.playedURLs()) == 1 })
	if got := device.playedURLs()[0]; got != "https://a/stream.mp3" {
		t.Errorf("played %q, want the resolved URL", got)
	}

	d.eval("document.querySelectorAll('#stations .station')[1].querySelectorAll('.actions button')[1].click()")
	waitFor(t, func() bool { return engineHasFavorite(t, base, "https://b/stream") })

	// Changing the order asks the catalogue again.
	d.eval("document.querySelector('#orders button[data-order=name]').click()")
	time.Sleep(300 * time.Millisecond)
	if got := d.evalString("return document.querySelector('#orders button.on').dataset.order"); got != "name" {
		t.Errorf("the order is %q, want name", got)
	}

	// The search narrows the stations to the ones whose name holds it.
	d.eval("document.getElementById('search').value = 'jazz'")
	d.eval("document.getElementById('search').dispatchEvent(new Event('input', {bubbles: true}))")
	time.Sleep(700 * time.Millisecond)
	d.waitFor("return document.querySelectorAll('#stations .station').length === 1", "the search result")
	if got := d.evalString("return document.querySelector('#stations .station .title').textContent"); got != "Jazz Radio" {
		t.Errorf("the search left %q, want Jazz Radio", got)
	}
}

// engineHasFavorite asks the page's state whether a favorite was saved.
func engineHasFavorite(t *testing.T, base, url string) bool {
	t.Helper()
	res, err := http.Get(base + "/api/state")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var state stateDTO
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		return false
	}
	for _, f := range state.Favorites {
		if f.URL == url {
			return true
		}
	}
	return false
}

// TestBrowserMobile checks the phone layout: one column, and the search
// field on its own line.
func TestBrowserMobile(t *testing.T) {
	device := &fakeDevice{}
	base, _ := newBrowserServerWith(t, device, 3)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#favorites li').length === 3", "the favorites")

	// A phone's width stacks the rows: the bottom row is below the top,
	// across the whole width.
	d.setWindow(390, 844)
	time.Sleep(300 * time.Millisecond)

	ok := d.evalBool(`
		const top = document.getElementById('top').getBoundingClientRect();
		const bottom = document.getElementById('bottom').getBoundingClientRect();
		const search = document.getElementById('search').getBoundingClientRect();
		const tabs = document.getElementById('tabs').getBoundingClientRect();
		return bottom.top >= top.bottom - 2 && bottom.width >= window.innerWidth - 2
			&& search.top > tabs.bottom - 2;
	`)
	if !ok {
		t.Error("the phone layout is not one column with the search on its own line")
	}
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

// TestBrowserLoadingAndPlayButton checks what a user needs when a device
// is slow to start a stream: that the app says it is working on it, and
// that playing does not need a double click, which a touchpad's light taps
// do not always reach a browser as.
func TestBrowserLoadingAndPlayButton(t *testing.T) {
	device := &fakeDevice{quiet: true}
	base := newBrowserServer(t, device, 3)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#favorites li').length === 3", "the favorites")

	if d.evalBool("return document.querySelector('.loading') !== null") {
		t.Fatal("the app waits for something before anything was asked for")
	}

	// The Play button of the second row starts it, with one click.
	clicked := d.evalBool(`
		const b = document.querySelectorAll('#favorites li')[1].querySelector('button.play');
		if (!b) return false;
		b.click();
		return true;
	`)
	if !clicked {
		t.Fatal("a row has no Play button")
	}

	// The wait is reported at once, while the device says nothing.
	d.waitFor("return document.querySelector('.loading') !== null", "the loading notice")
	reason := d.evalString("return document.querySelector('.loading').textContent.trim()")
	if reason == "" {
		t.Error("the notice says nothing")
	}

	waitFor(t, func() bool { return len(device.playedURLs()) == 1 })
	if got := device.playedURLs()[0]; got != "http://radio2.example/stream.mp3" {
		t.Errorf("played %q, want the second entry", got)
	}

	// The device catching up ends the notice.
	device.catchUp("http://radio2.example/stream.mp3")
	d.waitFor("return document.querySelector('.loading') === null", "the notice to go")
}

// TestBrowserStationCards checks the catalogue cards: the actions float
// over the picture, the text sits over its bottom, and the card plays.
func TestBrowserStationCards(t *testing.T) {
	device := &fakeDevice{}
	base := newBrowserServer(t, device, 0)
	d := newDriver(t)

	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#tabs button').length === 2", "the tabs")
	d.eval("document.getElementById('tab-browse').click()")
	d.waitFor("return document.querySelectorAll('#stations .station').length > 0", "the stations")

	// The card is a square: its height matches its width.
	square := d.evalBool(`
		const r = document.querySelector('#stations .station').getBoundingClientRect();
		return Math.abs(r.width - r.height) < 2;
	`)
	if !square {
		t.Error("the station card is not square")
	}

	// The name floats over the picture's bottom.
	over := d.evalBool(`
		const card = document.querySelector('#stations .station');
		const meta = card.querySelector('.meta').getBoundingClientRect();
		const r = card.getBoundingClientRect();
		return meta.bottom <= r.bottom + 1 && meta.top > r.top + r.height / 2;
	`)
	if !over {
		t.Error("the text does not float over the bottom of the picture")
	}

	// The actions are hidden until the card is hovered.
	if d.evalString("return getComputedStyle(document.querySelector('#stations .station .actions')).opacity") != "0" {
		t.Error("the actions show without hover")
	}

	// A double click plays the card.
	d.eval(`
		document.querySelectorAll('#stations .station')[0].dispatchEvent(
			new MouseEvent('dblclick', {bubbles: true}));
	`)
	waitFor(t, func() bool { return len(device.playedURLs()) == 1 })
}

// TestBrowserGridRowsDoNotOverlap checks the catalogue grid's rows: each
// card is square, and a row is as tall as its cards, with the gap between
// rows the grid asks for. Without that, the rows overlap like a card game.
func TestBrowserGridRowsDoNotOverlap(t *testing.T) {
	for _, name := range []string{"chrome", "firefox"} {
		t.Run(name, func(t *testing.T) {
			testGridRows(t, name)
		})
	}
}

func testGridRows(t *testing.T, browser string) {
	device := &fakeDevice{}
	base, _ := newBrowserServerWith(t, device, 0)
	d := newDriverOf(t, browser)

	d.setWindow(900, 700)
	d.goTo(base + "/")
	d.waitFor("return document.querySelectorAll('#tabs button').length === 2", "the tabs")
	d.eval("document.getElementById('tab-browse').click()")
	d.waitFor("return document.querySelectorAll('#stations .station').length === 8", "the stations")

	rows := d.eval(`
		const cards = [...document.querySelectorAll('#stations .station')]
			.map(c => c.getBoundingClientRect());
		const byTop = new Map();
		for (const r of cards) byTop.set(Math.round(r.top), r);
		const tops = [...byTop.keys()].sort((a, b) => a - b);
		const gap = parseFloat(getComputedStyle(document.getElementById('stations')).gap);
		return { tops, height: cards[0].height, gap, count: cards.length };
	`)
	m := rows.(map[string]any)
	tops := m["tops"].([]any)
	height := m["height"].(float64)
	gap := m["gap"].(float64)

	if len(tops) < 2 {
		t.Fatalf("the grid made %d row(s); the check needs two", len(tops))
	}
	first, second := tops[0].(float64), tops[1].(float64)
	want := height + gap
	got := second - first
	if diff := got - want; diff > 1 || diff < -1 {
		t.Errorf("rows are %vpt apart, want %v (card %v + gap %v): they overlap or leave a hole",
			got, want, height, gap)
	}
}

// TestBrowserAddFormOneRow checks the favorites form: a name, a URL and
// the button on one row on a desktop, and no content type field.
func TestBrowserAddFormOneRow(t *testing.T) {
	device := &fakeDevice{}
	base, _ := newBrowserServerWith(t, device, 0)
	d := newDriver(t)

	d.setWindow(1200, 800)
	d.goTo(base + "/")
	d.waitFor("return document.getElementById('add') !== null", "the form")

	if d.evalBool("return document.getElementById('add-type') !== null") {
		t.Error("the form still has a content type field")
	}

	ok := d.evalBool(`
		const n = document.getElementById('add-name').getBoundingClientRect();
		const u = document.getElementById('add-url').getBoundingClientRect();
		const b = document.querySelector('#add button').getBoundingClientRect();
		return Math.abs(n.top - u.top) < 4 && Math.abs(n.top - b.top) < 4;
	`)
	if !ok {
		t.Error("the name, URL and Add are not on one row")
	}
}

// TestBrowserAddFormOnAPhone checks that the form wraps to two lines on a
// phone, with the name and the button together.
func TestBrowserAddFormOnAPhone(t *testing.T) {
	device := &fakeDevice{}
	base, _ := newBrowserServerWith(t, device, 0)
	d := newDriver(t)

	d.setWindow(390, 844)
	d.goTo(base + "/")
	d.waitFor("return document.getElementById('add') !== null", "the form")

	ok := d.evalBool(`
		const n = document.getElementById('add-name').getBoundingClientRect();
		const u = document.getElementById('add-url').getBoundingClientRect();
		const b = document.querySelector('#add button').getBoundingClientRect();
		return Math.abs(n.top - b.top) < 4 && u.top > n.bottom - 2;
	`)
	if !ok {
		t.Error("on a phone the name and the button should share a row, with the URL below")
	}
}
