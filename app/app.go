// Package app is gocc's desktop window: what core.Engine does, drawn with
// mygo's own user interface.
//
// Everything the window shows comes from an Engine, which knows nothing of
// windows or frames; this package only keeps what belongs to the controls
// themselves, such as which row is chosen and what the form holds. The
// engine's state is guarded for goroutines, so the view reads a snapshot of
// it on the main thread and never has to think about locking.
package app

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/core"
)

// App is the window and the controls that belong to it.
type App struct {
	engine *core.Engine
	win    *mygo.Window

	// What belongs to the controls, on the main thread alone: the view
	// binds its controls to these and reads them back in the same frame.
	list       ui.ListState
	deviceList ui.ListState
	deviceSel  int
	// chosen is the playlist row the list shows as chosen. The list writes
	// it in place, so it must be a field here and not in a snapshot, which
	// is a copy; the view hands it to the engine after a click.
	chosen   int
	volume   float64
	muted    bool
	volumeAt time.Time
	mutedAt  time.Time
	newName  string
	newURL   string
	newType  string

	// The right column: which pane shows, and what belongs to each.
	pane            int
	favList         ui.ListState
	favChosen       int
	grid            ui.GridState
	stationSel      int
	stationQuery    string
	searchChangedAt time.Time
	countriesAsked  bool

	// Station logos, loaded in the background. They are kept by URL, not
	// by station: many stations share one picture, and a bitmap is made
	// once and shown in as many cards as need it. The mutex guards the
	// maps and the count, which background goroutines write; a bitmap
	// itself is made on the main thread, as MyGo asks.
	logoMu       sync.Mutex
	logos        map[string]logoState
	logoOrder    []string
	logoInFlight map[string]bool
	logoLoading  int
}

// New makes the app and its engine, reading the playlist and listing the
// network interfaces, without touching the network yet.
func New(dataDir string) (*App, error) {
	engine, err := core.New(core.Options{DataDir: dataDir})
	if err != nil {
		return nil, err
	}
	return &App{
		engine:     engine,
		deviceSel:  -1,
		chosen:     -1,
		favChosen:  -1,
		stationSel: -1,
		logos:      map[string]logoState{},
	}, nil
}

// Engine is what the window drives.
func (a *App) Engine() *core.Engine { return a.engine }

// Attach gives the app its window, which the engine asks for new frames.
func (a *App) Attach(win *mygo.Window) {
	a.win = win
	// Any change the engine makes, from any goroutine, redraws the window;
	// the view reads what changed from a snapshot of its own.
	a.engine.SetOnChange(win.Invalidate)
}

// View builds the window, and is its content.
func (a *App) View(c *ui.Context) { a.view(c) }

// onMain runs fn on the main thread, through the window, and redraws.
func (a *App) onMain(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
	}
}

// Start begins discovering devices.
func (a *App) Start(ctx context.Context) { a.engine.Start(ctx) }

// Close lets go of the device and stops discovery.
func (a *App) Close() { a.engine.Close() }
