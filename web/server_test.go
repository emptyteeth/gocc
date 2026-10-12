package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
)

// fakeDevice stands in for a device, so that the server can be driven in a
// test without one on the network.
//
// A browser test drives it from the goroutine of an HTTP handler while the
// test itself reads what it did, so it guards its own state.
type fakeDevice struct {
	mu       sync.Mutex
	status   cast.Status
	onChange func(cast.Status, error)
	played   []string
	volume   []float64
	muted    []bool
	paused   int
	resumed  int
	stopped  int
	// quiet makes Play record the request without reporting that the
	// device plays it, as a real device does while it buffers.
	quiet bool
}

// playedURLs returns what the device was asked to play.
func (f *fakeDevice) playedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.played...)
}

// counts returns how often the device was paused.
func (f *fakeDevice) pauseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}

// report tells the engine what the device is doing now, without the lock:
// the caller holds it.
func (f *fakeDevice) report() {
	if f.onChange != nil {
		f.onChange(f.status, nil)
	}
}

func (f *fakeDevice) Status() cast.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeDevice) Play(url, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.played = append(f.played, url)
	if f.quiet {
		return
	}
	f.status.State = cast.StatePlaying
	f.status.URL = url
	f.report()
}

// catchUp has the device report that it plays url, after a quiet Play.
func (f *fakeDevice) catchUp(url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.State = cast.StatePlaying
	f.status.URL = url
	f.report()
}

func (f *fakeDevice) Pause() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused++
	f.status.State = cast.StatePaused
	f.report()
}

func (f *fakeDevice) Resume() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed++
	f.status.State = cast.StatePlaying
	f.report()
}

func (f *fakeDevice) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	f.status = cast.Status{State: cast.StateIdle}
	f.report()
}

func (f *fakeDevice) Refresh() {}
func (f *fakeDevice) Close()   {}

func (f *fakeDevice) SetVolume(level float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volume = append(f.volume, level)
	f.status.Volume = level
	f.report()
}

func (f *fakeDevice) SetMuted(muted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.muted = append(f.muted, muted)
	f.status.Muted = muted
	f.report()
}

var testDevice = cast.Device{UUID: "uuid-1", Name: "Living Room", Addr: "10.0.0.5", Port: 8009}

// newTestServer makes a server over an engine whose device is the fake
// above, without mDNS or a network.
func newTestServer(t *testing.T, opts Options, entries ...playlist.Entry) (*Server, *fakeDevice) {
	t.Helper()
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

	for _, entry := range entries {
		if err := engine.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool { return !engine.Snapshot().Connecting })

	return New(engine, opts), device
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the engine")
}

// do sends a request to the handler and returns the response.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeState reads the state a response carries.
func decodeState(t *testing.T, rec *httptest.ResponseRecorder) stateDTO {
	t.Helper()
	var state stateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return state
}

func TestIndexServesThePage(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := do(t, s.Handler(), "GET", "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "gocc") {
		t.Error("the page does not look like gocc")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
}

func TestStateReportsTheDeviceAndPlaylist(t *testing.T) {
	s, _ := newTestServer(t, Options{}, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	state := decodeState(t, do(t, s.Handler(), "GET", "/api/state", ""))

	if len(state.Devices) != 1 || state.Devices[0].Name != "Living Room" {
		t.Fatalf("devices = %+v", state.Devices)
	}
	if !state.Devices[0].Connected {
		t.Error("the chosen device is not marked connected")
	}
	if len(state.Playlist) != 1 || state.Playlist[0].Name != "radio1" {
		t.Fatalf("playlist = %+v", state.Playlist)
	}
	if state.State != string(cast.StateIdle) || !state.Idle {
		t.Errorf("state = %q idle=%v", state.State, state.Idle)
	}
}

func TestPlayByIndex(t *testing.T) {
	s, device := newTestServer(t, Options{},
		playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"},
		playlist.Entry{Name: "radio2", URL: "http://radio2.com/2.mp3"},
	)
	h := s.Handler()

	if rec := do(t, h, "POST", "/api/play", `{"index":1}`); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}
	if got := device.playedURLs(); len(got) != 1 || got[0] != "http://radio2.com/2.mp3" {
		t.Fatalf("played %v", got)
	}
	// The state says what plays, and marks the row.
	state := decodeState(t, do(t, h, "GET", "/api/state", ""))
	if !state.Playing || !state.Playlist[1].Playing {
		t.Errorf("state = playing %v, playlist %+v", state.Playing, state.Playlist)
	}
}

func TestPlayOutOfRangeIsRefused(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := do(t, s.Handler(), "POST", "/api/play", `{"index":5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestTransport(t *testing.T) {
	s, device := newTestServer(t, Options{}, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	h := s.Handler()

	do(t, h, "POST", "/api/play", `{"index":0}`)
	if rec := do(t, h, "POST", "/api/toggle", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("toggle code = %d", rec.Code)
	}
	if device.paused != 1 {
		t.Fatalf("paused %d times", device.paused)
	}
	do(t, h, "POST", "/api/stop", "")
	if device.stopped != 1 {
		t.Fatalf("stopped %d times", device.stopped)
	}
}

func TestVolumeAndMute(t *testing.T) {
	s, device := newTestServer(t, Options{})
	h := s.Handler()

	if rec := do(t, h, "POST", "/api/volume", `{"level":0.35}`); rec.Code != http.StatusNoContent {
		t.Fatalf("volume code = %d, body %s", rec.Code, rec.Body)
	}
	if len(device.volume) != 1 || device.volume[0] != 0.35 {
		t.Fatalf("volume sent %v", device.volume)
	}
	if rec := do(t, h, "POST", "/api/mute", `{"muted":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("mute code = %d", rec.Code)
	}
	if len(device.muted) != 1 || !device.muted[0] {
		t.Fatalf("mute sent %v", device.muted)
	}

	// A volume out of the range the device accepts is clamped, not refused.
	do(t, h, "POST", "/api/volume", `{"level":9}`)
	if got := device.volume[len(device.volume)-1]; got != 1 {
		t.Errorf("volume 9 was sent as %v, want it clamped to 1", got)
	}
}

func TestVolumeWithoutALevelIsRefused(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	if rec := do(t, s.Handler(), "POST", "/api/volume", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestPlaylistAddChooseMoveRemove(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	h := s.Handler()

	// Add.
	rec := do(t, h, "POST", "/api/playlist", `{"name":"radio1","url":"http://radio1.com/1.mp3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add code = %d, body %s", rec.Code, rec.Body)
	}
	if state := decodeState(t, rec); len(state.Playlist) != 1 {
		t.Fatalf("after add: %+v", state.Playlist)
	}
	do(t, h, "POST", "/api/playlist", `{"name":"radio2","url":"http://radio2.com/2.mp3"}`)
	do(t, h, "POST", "/api/playlist", `{"name":"radio3","url":"http://radio3.com/3.mp3"}`)

	// Choose the middle row, then move it to the end.
	if rec := do(t, h, "POST", "/api/playlist/1/choose", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("choose code = %d", rec.Code)
	}
	if state := decodeState(t, do(t, h, "GET", "/api/state", "")); state.Chosen != 1 {
		t.Fatalf("chosen = %d", state.Chosen)
	}
	if rec := do(t, h, "POST", "/api/playlist/1/move", `{"to":3}`); rec.Code != http.StatusNoContent {
		t.Fatalf("move code = %d, body %s", rec.Code, rec.Body)
	}
	if names := playlistNames(t, h); names != "radio1,radio3,radio2" {
		t.Fatalf("after move: %v", names)
	}

	// Remove the first row.
	if rec := do(t, h, "DELETE", "/api/playlist/0", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove code = %d", rec.Code)
	}
	if names := playlistNames(t, h); names != "radio3,radio2" {
		t.Fatalf("after remove: %v", names)
	}
}

func TestAddRejectsWhatThePlaylistWould(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := do(t, s.Handler(), "POST", "/api/playlist", `{"name":"bad","url":"not a url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" {
		t.Error("no reason was given")
	}
}

func TestChooseAnUnknownDeviceIsRefused(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := do(t, s.Handler(), "POST", "/api/choose", `{"uuid":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestBasicAuth(t *testing.T) {
	s, _ := newTestServer(t, Options{Username: "alice", Password: "s3cret"})
	h := s.Handler()

	// Without credentials, a challenge.
	rec := do(t, h, "GET", "/api/state", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("challenge = %q", got)
	}

	// With the wrong ones, still a challenge.
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.SetBasicAuth("alice", "wrong")
	wrong := httptest.NewRecorder()
	h.ServeHTTP(wrong, req)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password got %d", wrong.Code)
	}

	// With the right ones, the state.
	req = httptest.NewRequest("GET", "/api/state", nil)
	req.SetBasicAuth("alice", "s3cret")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("the right credentials got %d", ok.Code)
	}
	// The page is guarded too.
	if rec := do(t, h, "GET", "/", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("the page was served without credentials: %d", rec.Code)
	}
}

func TestWithoutAuthNothingIsAsked(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	if rec := do(t, s.Handler(), "GET", "/api/state", ""); rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 with no authentication set", rec.Code)
	}
}

// TestEventsStreamsTheState checks that a browser watching the state is
// sent it at once, and again when it changes.
func TestEventsStreamsTheState(t *testing.T) {
	s, device := newTestServer(t, Options{}, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	h := s.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	rec := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	// The first message is the state as it is now.
	first := rec.nextEvent(t)
	if len(first.Playlist) != 1 {
		t.Fatalf("first event holds %+v", first.Playlist)
	}

	// A change, from another goroutine, reaches the stream.
	go device.Play("http://radio1.com/1.mp3", "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state := rec.nextEvent(t); state.Playing {
			cancel()
			<-done
			return
		}
	}
	t.Fatal("a change never reached the stream")
}

// streamRecorder is a ResponseRecorder that lets a test read the events a
// stream writes while it is still running.
type streamRecorder struct {
	*httptest.ResponseRecorder
	events chan stateDTO
}

func newStreamRecorder() *streamRecorder {
	r := &streamRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		events:           make(chan stateDTO, 16),
	}
	return r
}

// Write takes each "data: {...}" line of the stream and hands it on.
func (r *streamRecorder) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var state stateDTO
			if err := json.Unmarshal([]byte(data), &state); err == nil {
				select {
				case r.events <- state:
				default:
				}
			}
		}
	}
	return r.ResponseRecorder.Write(p)
}

func (r *streamRecorder) Flush() {}

func (r *streamRecorder) nextEvent(t *testing.T) stateDTO {
	t.Helper()
	select {
	case state := <-r.events:
		return state
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived")
		return stateDTO{}
	}
}

func playlistNames(t *testing.T, h http.Handler) string {
	t.Helper()
	state := decodeState(t, do(t, h, "GET", "/api/state", ""))
	var names []string
	for _, entry := range state.Playlist {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}
