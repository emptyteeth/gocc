package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/emptyteeth/gocc/radiobrowser"
)

// pngBytes is a small valid PNG, for a test server to serve.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// logoServer counts the requests for each path.
type logoServer struct {
	mu     sync.Mutex
	hits   map[string]int
	fail   bool
	server *httptest.Server
}

func newLogoServer(t *testing.T, fail bool) *logoServer {
	t.Helper()
	ls := &logoServer{hits: map[string]int{}, fail: fail}
	ls.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		ls.hits[r.URL.Path]++
		fail := ls.fail
		ls.mu.Unlock()
		if fail {
			http.Error(w, "no logo", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(t))
	}))
	t.Cleanup(ls.server.Close)
	return ls
}

func (l *logoServer) count(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits[path]
}

func (l *logoServer) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.hits {
		n += c
	}
	return n
}

// testAppForLogos makes an App whose window is a tester, so that onMain
// runs, and with the logo cache ready.
func testAppForLogos(t *testing.T) *App {
	t.Helper()
	a, _ := newTestApp(t)
	return a
}

// waitLogo waits until the logo for url is cached, or the attempt failed.
func waitLogo(t *testing.T, a *App, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.logoMu.Lock()
		state, ok := a.logos[url]
		a.logoMu.Unlock()
		if ok && (state.bitmap != nil || !state.failed.IsZero()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the logo %s was never read", url)
}

func TestSharedLogoIsReadOnce(t *testing.T) {
	srv := newLogoServer(t, false)
	a := testAppForLogos(t)

	station := radiobrowser.Station{UUID: "u1", Favicon: srv.server.URL + "/shared.png"}
	// Many stations share one picture.
	for range 10 {
		a.stationLogo(station)
	}
	waitLogo(t, a, station.Favicon)
	time.Sleep(50 * time.Millisecond)

	if got := srv.count("/shared.png"); got != 1 {
		t.Errorf("the shared logo was read %d times, want 1", got)
	}
	// The bitmap is there for every card that asks.
	if a.stationLogo(station) == nil {
		t.Error("the loaded logo is not handed to the card")
	}
}

func TestFailedLogoIsNotRetriedAtOnce(t *testing.T) {
	srv := newLogoServer(t, true)
	a := testAppForLogos(t)

	station := radiobrowser.Station{UUID: "u1", Favicon: srv.server.URL + "/missing.png"}
	a.stationLogo(station)
	waitLogo(t, a, station.Favicon)

	// Building the card again must not send another request.
	for range 5 {
		a.stationLogo(station)
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.count("/missing.png"); got != 1 {
		t.Errorf("a failed logo was read %d times, want 1", got)
	}

	// Once the wait has passed, it is tried again.
	a.logoMu.Lock()
	a.logos[station.Favicon] = logoState{failed: time.Now().Add(-logoRetryWait - time.Second)}
	a.logoMu.Unlock()

	a.stationLogo(station)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && srv.count("/missing.png") < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.count("/missing.png"); got != 2 {
		t.Errorf("the retry did not happen: %d requests", got)
	}
}

func TestLogoCacheDropsTheOldest(t *testing.T) {
	srv := newLogoServer(t, false)
	a := testAppForLogos(t)

	// Fill the cache past its bound.
	for i := range maxLogos + 5 {
		url := srv.server.URL + "/" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".png"
		a.stationLogo(radiobrowser.Station{UUID: url, Favicon: url})
		waitLogo(t, a, url)
	}

	a.logoMu.Lock()
	n := len(a.logos)
	order := len(a.logoOrder)
	a.logoMu.Unlock()
	if n > maxLogos {
		t.Errorf("the cache holds %d logos, want at most %d", n, maxLogos)
	}
	if order != n {
		t.Errorf("the order holds %d entries for %d logos", order, n)
	}
}

func TestLogoCountDoesNotLeak(t *testing.T) {
	srv := newLogoServer(t, true)
	a := testAppForLogos(t)

	// Every attempt must give its slot back, whether it worked or not.
	for i := range 12 {
		url := srv.server.URL + "/bad" + string(rune('a'+i)) + ".png"
		a.stationLogo(radiobrowser.Station{UUID: url, Favicon: url})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.logoMu.Lock()
		n := a.logoLoading
		a.logoMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.logoMu.Lock()
	n := a.logoLoading
	a.logoMu.Unlock()
	t.Fatalf("%d logo loads are still counted, want 0", n)
}

func TestLogoLoadingIsBounded(t *testing.T) {
	// A slow server keeps the requests in flight, so the bound shows.
	var inFlight, peak int
	var mu sync.Mutex
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-block
		mu.Lock()
		inFlight--
		mu.Unlock()
		w.Write(pngBytes(t))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })

	a := testAppForLogos(t)
	for i := range 30 {
		url := srv.URL + "/slow" + string(rune('a'+i)) + ".png"
		a.stationLogo(radiobrowser.Station{UUID: url, Favicon: url})
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	got := peak
	mu.Unlock()
	if got > maxLogoLoads {
		t.Errorf("%d logo requests were in flight at once, want at most %d", got, maxLogoLoads)
	}
}
