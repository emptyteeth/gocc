// Package web serves gocc's interface to a browser.
//
// It is the command line version's front end: an http.Handler over a
// core.Engine, with the page itself embedded in the binary. Nothing of
// mygo or a window is needed, so the same thing runs headless, on a
// server, or over an ssh tunnel.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emptyteeth/gocc/cast"
	"github.com/emptyteeth/gocc/core"
	"github.com/emptyteeth/gocc/playlist"
	"github.com/emptyteeth/gocc/radiobrowser"
)

//go:embed ui
var uiFS embed.FS

// Server serves the page and the API that drives an Engine.
type Server struct {
	engine *core.Engine
	auth   *basicAuth
	hub    *hub
}

// Options configures a Server.
type Options struct {
	// Username and Password ask browsers for HTTP basic authentication.
	// An empty Username turns it off, which is only safe on an address
	// reachable from this machine alone.
	Username string
	Password string
}

// New makes a server over an engine.
func New(engine *core.Engine, opts Options) *Server {
	s := &Server{engine: engine, hub: newHub()}
	if opts.Username != "" {
		s.auth = &basicAuth{username: opts.Username, password: opts.Password}
	}
	// Every change the engine makes, from any goroutine, wakes the
	// browsers that are watching.
	engine.SetOnChange(s.hub.notify)
	return s
}

// Handler returns the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/events", s.events)

	mux.HandleFunc("POST /api/scan", s.scan)
	mux.HandleFunc("POST /api/choose", s.choose)
	mux.HandleFunc("POST /api/disconnect", s.disconnect)

	mux.HandleFunc("POST /api/play", s.play)
	mux.HandleFunc("POST /api/toggle", s.toggle)
	mux.HandleFunc("POST /api/pause", s.pause)
	mux.HandleFunc("POST /api/resume", s.resume)
	mux.HandleFunc("POST /api/stop", s.stop)
	mux.HandleFunc("POST /api/volume", s.volume)
	mux.HandleFunc("POST /api/mute", s.mute)
	mux.HandleFunc("POST /api/refresh", s.refresh)

	mux.HandleFunc("POST /api/playlist", s.addEntry)
	mux.HandleFunc("POST /api/playlist/{i}/move", s.moveEntry)
	mux.HandleFunc("POST /api/playlist/{i}/choose", s.chooseEntry)
	mux.HandleFunc("DELETE /api/playlist/{i}", s.removeEntry)

	mux.HandleFunc("POST /api/catalogue/countries", s.loadCountries)
	mux.HandleFunc("POST /api/catalogue/country", s.setCountry)
	mux.HandleFunc("POST /api/catalogue/order", s.setOrder)
	mux.HandleFunc("POST /api/catalogue/query", s.setQuery)
	mux.HandleFunc("POST /api/catalogue/refresh", s.refreshCatalogue)
	mux.HandleFunc("POST /api/catalogue/{i}/play", s.playStation)
	mux.HandleFunc("POST /api/catalogue/{i}/favorite", s.favoriteStation)

	mux.HandleFunc("POST /api/favorites", s.addFavorite)
	mux.HandleFunc("POST /api/favorites/{i}/play", s.playFavorite)
	mux.HandleFunc("DELETE /api/favorites/{i}", s.removeFavorite)
	mux.HandleFunc("DELETE /api/favorites", s.removeFavoriteByURL)

	if s.auth != nil {
		return s.auth.middleware(mux)
	}
	return mux
}

// index serves the page.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := fs.ReadFile(uiFS, "ui/index.html")
	if err != nil {
		http.Error(w, "the page is missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(page)
}

// stateDTO is what the browser is sent. It is built from an engine
// snapshot, with what the page shows worked out here rather than in
// JavaScript, so that both front ends say the same thing.
type stateDTO struct {
	Devices     []deviceDTO `json:"devices"`
	Interfaces  []string    `json:"interfaces"`
	Iface       string      `json:"iface"`
	Selected    string      `json:"selected"`
	Connecting  bool        `json:"connecting"`
	Description string      `json:"description"`
	State       string      `json:"state"`
	Playing     bool        `json:"playing"`
	Idle        bool        `json:"idle"`
	Title       string      `json:"title"`
	Artist      string      `json:"artist"`
	URL         string      `json:"url"`
	Error       string      `json:"error"`
	Playlist    []entryDTO  `json:"playlist"`
	Chosen      int         `json:"chosen"`
	Volume      float64     `json:"volume"`
	Muted       bool        `json:"muted"`

	// Loading says an entry was asked for and the device is not playing it
	// yet, and LoadingReason is what to tell the user meanwhile.
	Loading       bool   `json:"loading"`
	LoadingReason string `json:"loadingReason"`

	// The catalogue and the favorites, as the page browses them.
	Countries   []countryDTO `json:"countries"`
	CountryCode string       `json:"countryCode"`
	Order       string       `json:"order"`
	Stations    []stationDTO `json:"stations"`
	Query       string       `json:"query"`
	Searching   bool         `json:"searching"`
	SearchErr   string       `json:"searchError"`
	Favorites   []entryDTO   `json:"favorites"`
}

type countryDTO struct {
	Name     string `json:"name"`
	Code     string `json:"code"`
	Stations int    `json:"stations"`
}

type stationDTO struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	CountryCode string `json:"countryCode"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	HLS         bool   `json:"hls"`
	Favicon     string `json:"favicon"`
	Playing     bool   `json:"playing"`
	Saved       bool   `json:"saved"`
}

type deviceDTO struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	Connected bool   `json:"connected"`
}

type entryDTO struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Playing     bool   `json:"playing"`
}

// snapshotOf turns an engine snapshot into what the page reads.
func snapshotOf(s core.Snapshot) stateDTO {
	dto := stateDTO{
		Interfaces:    make([]string, 0, len(s.Interfaces)),
		Iface:         s.Iface,
		Selected:      s.Selected,
		Connecting:    s.Connecting,
		Description:   s.Status.Describe(),
		State:         string(s.Status.State),
		Playing:       s.Status.Playing(),
		Idle:          s.Status.State == cast.StateIdle,
		Title:         s.Status.Title,
		Artist:        s.Status.Artist,
		URL:           s.Status.URL,
		Error:         s.Err,
		Devices:       make([]deviceDTO, 0, len(s.Devices)),
		Playlist:      make([]entryDTO, 0, len(s.Playlist)),
		Chosen:        s.Chosen,
		Volume:        s.Volume,
		Muted:         s.Muted,
		Loading:       s.Loading,
		LoadingReason: s.LoadingReason,
		Countries:     make([]countryDTO, 0, len(s.Countries)),
		CountryCode:   s.CountryCode,
		Order:         string(s.Order),
		Stations:      make([]stationDTO, 0, len(s.Stations)),
		Query:         s.Query,
		Searching:     s.Searching,
		SearchErr:     s.SearchErr,
		Favorites:     make([]entryDTO, 0, len(s.Favorites)),
	}
	for _, iface := range s.Interfaces {
		dto.Interfaces = append(dto.Interfaces, iface.Name)
	}
	for _, d := range s.Devices {
		name := d.Name
		if name == "" {
			name = d.Model
		}
		if name == "" {
			name = d.Addr
		}
		dto.Devices = append(dto.Devices, deviceDTO{
			UUID:      d.UUID,
			Name:      name,
			Addr:      d.Addr,
			Connected: d.UUID == s.Selected,
		})
	}
	for _, e := range s.Playlist {
		dto.Playlist = append(dto.Playlist, entryDTO{
			Name:        e.Name,
			URL:         e.URL,
			ContentType: e.ContentType,
			Playing:     e.URL != "" && e.URL == s.Status.URL,
		})
	}
	for _, c := range s.Countries {
		dto.Countries = append(dto.Countries, countryDTO{
			Name: c.Name, Code: c.Code, Stations: c.Stations,
		})
	}
	saved := make(map[string]bool, len(s.Favorites))
	for _, e := range s.Favorites {
		saved[e.URL] = true
		dto.Favorites = append(dto.Favorites, entryDTO{
			Name:        e.Name,
			URL:         e.URL,
			ContentType: e.ContentType,
			Playing:     e.URL != "" && e.URL == s.Status.URL,
		})
	}
	for _, st := range s.Stations {
		playURL := st.URLToPlay()
		dto.Stations = append(dto.Stations, stationDTO{
			UUID:        st.UUID,
			Name:        st.Name,
			URL:         playURL,
			CountryCode: st.CountryCode,
			Codec:       st.Codec,
			Bitrate:     st.Bitrate,
			HLS:         st.HLS != 0,
			Favicon:     st.Favicon,
			Playing:     playURL != "" && playURL == s.Status.URL,
			Saved:       saved[playURL],
		})
	}
	return dto
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, snapshotOf(s.engine.Snapshot()))
}

// events streams the state as it changes, as server-sent events, which
// every browser has and which needs no library on either side.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// A subscription holds at most one pending wake-up: a burst of changes
	// while the browser is behind collapses into the one state it is sent
	// when it catches up, which is all it needs.
	wake, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	send := func() bool {
		state := snapshotOf(s.engine.Snapshot())
		body, err := json.Marshal(state)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	// A heartbeat keeps the connection open through a proxy that would
	// otherwise time out a quiet stream.
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case _, ok := <-wake:
			if !ok {
				return
			}
			if !send() {
				return
			}
		}
	}
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	s.engine.Rediscover()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) choose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UUID string `json:"uuid"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.engine.ChooseByUUID(body.UUID); err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	s.engine.Disconnect()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index *int   `json:"index"`
		URL   string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch {
	case body.URL != "":
		// A favorite row knows its stream, not its place in the list.
		if err := s.engine.PlayURL(body.URL); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	case body.Index == nil:
		// No index: play whatever row is chosen.
		s.engine.PlayChosen()
	default:
		if err := s.engine.PlayIndex(*body.Index); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) toggle(w http.ResponseWriter, r *http.Request) {
	s.engine.TogglePlay()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	s.engine.Pause()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.engine.Resume()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	s.engine.Stop()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	s.engine.Refresh()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) volume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level *float64 `json:"level"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Level == nil {
		writeError(w, errors.New("a level is needed"), http.StatusBadRequest)
		return
	}
	s.engine.SetVolume(*body.Level)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) mute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Muted *bool `json:"muted"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Muted == nil {
		writeError(w, errors.New("whether to mute is needed"), http.StatusBadRequest)
		return
	}
	s.engine.SetMuted(*body.Muted)
	w.WriteHeader(http.StatusNoContent)
}

// loadCountries reads the country list, once.
func (s *Server) loadCountries(w http.ResponseWriter, r *http.Request) {
	s.engine.LoadCountries()
	w.WriteHeader(http.StatusNoContent)
}

// setCountry limits the catalogue to one country.
func (s *Server) setCountry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.engine.SetCountry(strings.ToUpper(body.Code))
	w.WriteHeader(http.StatusNoContent)
}

// setOrder changes how the catalogue is sorted.
func (s *Server) setOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order string `json:"order"`
	}
	if !decode(w, r, &body) {
		return
	}
	order := radiobrowser.Order(body.Order)
	if !order.Valid() {
		writeError(w, fmt.Errorf("unknown order %q", body.Order), http.StatusBadRequest)
		return
	}
	s.engine.SetOrder(order)
	w.WriteHeader(http.StatusNoContent)
}

// setQuery searches the catalogue by name.
func (s *Server) setQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.engine.SetStationQuery(body.Query)
	w.WriteHeader(http.StatusNoContent)
}

// refreshCatalogue drops what was cached and reads the page again.
func (s *Server) refreshCatalogue(w http.ResponseWriter, r *http.Request) {
	s.engine.InvalidateCatalogue()
	s.engine.LoadStations()
	w.WriteHeader(http.StatusNoContent)
}

// playStation plays station i of the catalogue.
func (s *Server) playStation(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	if err := s.engine.PlayStation(i); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// favoriteStation saves station i of the catalogue, or takes it out.
func (s *Server) favoriteStation(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	var body struct {
		Saved bool `json:"saved"`
	}
	if !decode(w, r, &body) {
		return
	}
	var err error
	if body.Saved {
		err = s.engine.AddStationFavorite(i)
	} else {
		err = s.engine.RemoveFavoriteByURL(s.stationURL(i))
	}
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stationURL is the stream URL of station i of the catalogue.
func (s *Server) stationURL(i int) string {
	stations := s.engine.Snapshot().Stations
	if i < 0 || i >= len(stations) {
		return ""
	}
	return stations[i].URLToPlay()
}

// addFavorite saves a hand written entry to the favorites.
func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		ContentType string `json:"contentType"`
	}
	if !decode(w, r, &body) {
		return
	}
	entry := playlist.Entry{
		Name:        strings.TrimSpace(body.Name),
		URL:         strings.TrimSpace(body.URL),
		ContentType: strings.TrimSpace(body.ContentType),
	}
	if err := core.ValidateEntry(entry); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := s.engine.AddFavorite(entry); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, snapshotOf(s.engine.Snapshot()))
}

// playFavorite plays favorite i.
func (s *Server) playFavorite(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	if err := s.engine.PlayFavorite(i); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeFavoriteByURL takes out the favorite whose stream is in the body.
func (s *Server) removeFavoriteByURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.URL == "" {
		writeError(w, fmt.Errorf("a URL is needed"), http.StatusBadRequest)
		return
	}
	if err := s.engine.RemoveFavoriteByURL(body.URL); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeFavorite takes favorite i out.
func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	if err := s.engine.RemoveFavorite(i); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		ContentType string `json:"contentType"`
	}
	if !decode(w, r, &body) {
		return
	}
	entry := playlist.Entry{
		Name:        strings.TrimSpace(body.Name),
		URL:         strings.TrimSpace(body.URL),
		ContentType: strings.TrimSpace(body.ContentType),
	}
	if err := core.ValidateEntry(entry); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := s.engine.AddEntry(entry); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, snapshotOf(s.engine.Snapshot()))
}

func (s *Server) removeEntry(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	if err := s.engine.RemoveEntry(i); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) moveEntry(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	var body struct {
		To *int `json:"to"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.To == nil {
		writeError(w, errors.New("a destination is needed"), http.StatusBadRequest)
		return
	}
	if err := s.engine.MoveEntry(i, *body.To); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) chooseEntry(w http.ResponseWriter, r *http.Request) {
	i, ok := pathIndex(w, r)
	if !ok {
		return
	}
	s.engine.ChooseEntry(i)
	w.WriteHeader(http.StatusNoContent)
}

// pathIndex reads the {i} of a route.
func pathIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil {
		writeError(w, fmt.Errorf("%q is not a row", r.PathValue("i")), http.StatusBadRequest)
		return 0, false
	}
	return i, true
}

// decode reads a JSON body, answering the request itself when it cannot.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, fmt.Errorf("the request could not be read: %w", err), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("gocc-cli: writing a response: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// hub wakes the browsers watching the state.
type hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newHub() *hub {
	return &hub{subs: map[chan struct{}]struct{}{}}
}

// subscribe adds a browser, and returns a function that removes it.
func (h *hub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// notify wakes every browser. It never blocks, so a browser that is behind
// misses a wake-up rather than holding up the others, and is sent the
// current state when it catches up.
func (h *hub) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// basicAuth guards the API with HTTP basic authentication.
type basicAuth struct {
	username string
	password string
}

// middleware asks for the credentials before serving anything, and answers
// with a challenge a browser turns into its own login prompt when they are
// missing or wrong.
func (a *basicAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		// Compare both in a constant time, so that the answer does not
		// say how much of the credentials was right.
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="gocc", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
