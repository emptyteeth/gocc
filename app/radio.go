package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
	"github.com/free/gocc/radiobrowser"
)

// The icons of a station card, drawn in currentColor so that the button's
// text color paints them, as an icon set does.
var (
	playIcon        = cardIcon(`<path d="M8 5v14l11-7z" fill="currentColor" stroke="none"/>`)
	heartIcon       = cardIcon(`<path d="M12 20.5 4.7 13.3a4.8 4.8 0 0 1 6.8-6.8l.5.5.5-.5a4.8 4.8 0 0 1 6.8 6.8z"/>`)
	heartFilledIcon = cardIcon(`<path d="M12 20.5 4.7 13.3a4.8 4.8 0 0 1 6.8-6.8l.5.5.5-.5a4.8 4.8 0 0 1 6.8 6.8z" fill="currentColor"/>`)
)

// cardIcon parses a 24×24 icon drawn in currentColor.
func cardIcon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

// The panes of the right column.
const (
	paneFavorites = iota
	paneBrowse
)

// The orders the browse pane offers, and their labels.
var browseOrders = []struct {
	label string
	order radiobrowser.Order
}{
	{"Votes", radiobrowser.OrderVotes},
	{"Popular", radiobrowser.OrderClickCount},
	{"Name", radiobrowser.OrderName},
}

// rightPane draws the right column: the favorites, or the catalogue to
// browse, chosen by the segmented control at its top.
func (a *App) rightPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Grow(2).Basis(0).MinHeight(0).FillHeight().Padding(12).Gap(8).Children(func() {
		ui.Row(c).Children(func() {
			ui.Segmented(c, &a.pane, "Favorites", "Browse").Label("Right column")
		})
		if a.pane == paneFavorites {
			a.favoritesPane(c, t, s)
		} else {
			a.browsePane(c, t, s)
		}
	})
}

// favoritesPane lists the saved stations and hand written entries.
func (a *App) favoritesPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Favorites").FontSize(15).Bold().Grow(1)
		ui.Button(c, "Play").Label("Play favorite").Tooltip("Play the chosen favorite").
			Disabled(a.favChosen < 0).OnClick(func() { a.engine.PlayFavorite(a.favChosen) })
		ui.Button(c, "Remove").Label("Remove favorite").Tooltip("Take the favorite out").
			Disabled(a.favChosen < 0).OnClick(func() { a.engine.RemoveFavorite(a.favChosen) })
	})

	if a.favChosen != s.Chosen && false {
		// (the favorites have their own choice; nothing to sync)
	}
	a.favList.Key = func(i int) any { return s.Favorites[i].URL }
	a.favList.Label = func(i int) string { return s.Favorites[i].Name }
	a.favList.Selected = &a.favChosen

	list := ui.List(c, &a.favList, len(s.Favorites), func(i int) {
		a.favoriteRow(c, t, s.Favorites[i], a.favChosen == i, s.Status.URL)
	}).Grow(1)
	list.Children(func() {
		if len(s.Favorites) == 0 {
			ui.Column(c).Padding(8).Gap(4).Children(func() {
				ui.Text(c, "No favorites yet.").TextColor(t.TextMuted).FontSize(12)
				ui.Text(c, "Save a station from Browse, or add a URL below.").
					TextColor(t.TextMuted).FontSize(11)
			})
		}
	})
	// A double click plays the favorite.
	if list.Submitted() && a.favChosen >= 0 {
		a.engine.PlayFavorite(a.favChosen)
	}

	a.favoriteForm(c, t)
}

// favoriteRow shows one saved entry, marked when it is what plays.
func (a *App) favoriteRow(c *ui.Context, t *ui.Theme, e playlist.Entry, chosen bool, playingURL string) {
	text, muted := rowColors(t, chosen)
	ui.Column(c).Padding(6, 8).Gap(2).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if e.URL != "" && e.URL == playingURL {
				ui.Text(c, "▶").FontSize(11).TextColor(text)
			}
			ui.Text(c, e.Name).TextColor(text).SingleLine()
		})
		ui.Text(c, e.URL).FontSize(11).TextColor(muted).SingleLine()
	})
}

// favoriteForm adds a hand written entry to the favorites.
func (a *App) favoriteForm(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(6).Padding(8).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, "Add by URL").FontSize(11).Bold().TextColor(t.TextMuted)
		ui.TextInput(c, &a.newName).Placeholder("Name").Label("Name")
		ui.TextInput(c, &a.newURL).Placeholder("http://radio.example/stream.mp3").Label("URL")
		ui.TextInput(c, &a.newType).Placeholder("Content type (optional)").Label("Content type")
		ui.Row(c).Gap(6).Justify(ui.End).Children(func() {
			ui.PrimaryButton(c, "Add").OnClick(a.submitNewFavorite)
		})
	})
}

// submitNewFavorite checks the form and saves the entry.
func (a *App) submitNewFavorite() {
	entry := playlist.Entry{
		Name:        strings.TrimSpace(a.newName),
		URL:         strings.TrimSpace(a.newURL),
		ContentType: strings.TrimSpace(a.newType),
	}
	if err := core.ValidateEntry(entry); err != nil {
		a.engine.ReportError(err)
		return
	}
	if err := a.engine.AddFavorite(entry); err != nil {
		a.engine.ReportError(err)
		return
	}
	a.newName, a.newURL, a.newType = "", "", ""
}

// browsePane browses the radio-browser catalogue: a country, an order and
// a word to search, and the stations they lead to.
func (a *App) browsePane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	// The country list is read once, when the pane first shows.
	if !a.countriesAsked {
		a.countriesAsked = true
		a.engine.LoadCountries()
	}

	ui.Column(c).Gap(6).Children(func() {
		a.countryPicker(c, t, s)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			order := a.orderIndex(s.Order)
			if ui.Segmented(c, &order, orderLabels()...).Label("Order").Changed() {
				a.engine.SetOrder(browseOrders[order].order)
			}
			ui.Button(c, "Refresh").Label("Refresh catalogue").
				Tooltip("Read the catalogue again, past what is cached").
				OnClick(func() {
					a.engine.InvalidateCatalogue()
					// The country chosen stays; only what was read of the
					// catalogue goes, and the page is asked for again.
					a.engine.LoadStations()
				})
		})
		a.searchBox(c, t)
	})

	if s.Searching {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "Reading the catalogue…").TextColor(t.TextMuted).FontSize(12)
		})
	}
	if s.SearchErr != "" {
		ui.Column(c).Gap(2).Padding(8).Radius(6).Background(t.Surface).Children(func() {
			ui.Text(c, "The catalogue could not be read").FontSize(11).Bold().TextColor(t.Danger)
			ui.Text(c, s.SearchErr).FontSize(11).TextColor(t.TextMuted).Selectable()
		})
	}

	a.stationGrid(c, t, s)
}

// countryPicker chooses the country to list stations of.
func (a *App) countryPicker(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	if len(s.Countries) == 0 {
		return
	}
	names := make([]string, len(s.Countries))
	for i, country := range s.Countries {
		names[i] = fmt.Sprintf("%s (%d)", country.Name, country.Stations)
	}
	pick := ""
	for i, country := range s.Countries {
		if country.Code == s.CountryCode {
			pick = names[i]
			break
		}
	}
	// A combobox rather than a select: 250 countries are more than a
	// drop-down one scrolls through comfortably, and typing narrows them.
	box := ui.Combobox(c, &pick, names).Label("Country")
	if box.Changed() {
		for i, name := range names {
			if name == pick {
				a.engine.SetCountry(s.Countries[i].Code)
				break
			}
		}
	}
}

// searchBox searches the catalogue by name, after the typing pauses.
func (a *App) searchBox(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Children(func() {
		field := ui.SearchField(c, &a.stationQuery).Grow(1).Label("Search stations")
		if field.Changed() {
			a.searchChangedAt = c.Now()
			// After only asks for another frame; the search itself runs
			// in a later frame, once the typing has stopped.
			c.After(300 * time.Millisecond)
		}
		if !a.searchChangedAt.IsZero() && c.Now().Sub(a.searchChangedAt) >= 300*time.Millisecond {
			a.searchChangedAt = time.Time{}
			a.engine.SetStationQuery(a.stationQuery)
		}
	})
}

// stationGrid draws the stations as cards.
func (a *App) stationGrid(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	if len(s.Stations) == 0 && !s.Searching && s.SearchErr == "" {
		ui.Column(c).Fill().Center().Gap(6).Children(func() {
			ui.Text(c, "No stations").FontSize(15).Bold()
			ui.Text(c, "Try another country, or clear the search.").
				TextColor(t.TextMuted).FontSize(12)
		})
		return
	}

	if a.stationSel >= len(s.Stations) {
		a.stationSel = -1
	}
	a.grid.Selected = &a.stationSel
	a.grid.Key = func(i int) any { return s.Stations[i].UUID }
	a.grid.Label = func(i int) string { return s.Stations[i].Name }

	// Square cards: the cell is as tall as the column is wide, so the
	// picture fills it and the text floats over its bottom.
	grid := ui.GridView(c, &a.grid, len(s.Stations), 150, 164, func(i int) {
		a.stationCard(c, t, i, s.Stations[i], s.Status.URL)
	}).Grow(1)
	// A double click, or Enter, plays the station.
	if grid.Submitted() && a.stationSel >= 0 {
		a.engine.PlayStation(a.stationSel)
	}
}

// stationCard draws one station as a square: its logo fills the card,
// and its name and details float over the bottom of the picture. The Play
// and Save buttons float over the top corner, and only while the pointer
// is on the card, so that the card is a picture until it is used.
func (a *App) stationCard(c *ui.Context, t *ui.Theme, i int, station radiobrowser.Station, playingURL string) {
	playing := station.URLToPlay() != "" && station.URLToPlay() == playingURL
	saved := a.engine.IsFavorite(station.URLToPlay())
	chosen := a.stationSel == i

	card := ui.Box(c).Fill().Radius(6).Clip().Background(t.SurfaceHover)
	// The buttons show while the pointer is on the card, and on the card
	// the keyboard chose, which cannot be hovered.
	showButtons := card.Hovered() || chosen
	card.Children(func() {
		if bitmap := a.stationLogo(station); bitmap != nil {
			ui.Image(c, bitmap).Fit(ui.Cover).Fill()
		} else if !showButtons {
			// No logo yet, or none to be had: the station's initials,
			// centered on the empty card.
			ui.Text(c, initialsOf(station.Name)).FontSize(30).Bold().
				TextColor(t.TextMuted).AlignSelf(ui.Center).Margin(0, ui.Auto)
		}

		// A shade behind the text, so that it stays readable over any
		// picture: clear at the top, dark at the bottom. It is deep
		// enough to carry white text over a bright logo too.
		ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(60).
			Gradient(ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0.88), 180).
			PassThrough()

		ui.Column(c).Absolute().Left(8).Right(8).Bottom(6).Gap(1).Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				if playing {
					ui.Text(c, "▶").FontSize(10).TextColor(ui.RGB(255, 255, 255))
				}
				ui.Text(c, station.Name).FontSize(12).Grow(1).MaxLines(1).
					TextColor(ui.RGB(255, 255, 255))
				if saved {
					ui.Text(c, "♥").FontSize(11).TextColor(ui.RGB(255, 255, 255))
				}
			})
			ui.Text(c, stationDetail(station)).FontSize(10).
				TextColor(ui.RGBA(255, 255, 255, 0.78)).SingleLine()
		})

		if showButtons {
			ui.Row(c).Absolute().Top(6).Right(6).Gap(4).Children(func() {
				overlayButton(c, playIcon, "Play "+station.Name, "Play", func() {
					a.engine.PlayStation(i)
				})
				label := "Save " + station.Name
				if saved {
					label = "Saved " + station.Name
				}
				icon := heartIcon
				if saved {
					icon = heartFilledIcon
				}
				overlayButton(c, icon, label, "Save to favorites", func() {
					if err := a.engine.ToggleFavorite(station.URLToPlay()); err != nil {
						a.engine.ReportError(err)
					}
				})
			})
		}
	})
}

// overlayButton is a round icon button that floats over a card's logo.
func overlayButton(c *ui.Context, icon *ui.SVG, label, tooltip string, onClick func()) {
	ui.ButtonBase(c).Size(26, 26).Radius(13).Center().
		Background(ui.RGBA(0, 0, 0, 0.55)).TextColor(ui.RGB(255, 255, 255)).
		Label(label).Tooltip(tooltip).OnClick(onClick).Children(func() {
		ui.Icon(c, icon).Size(15, 15)
	})
}

// stationDetail says what a station plays, as "DE · MP3 · 128 kbps".
func stationDetail(station radiobrowser.Station) string {
	var parts []string
	if station.CountryCode != "" {
		parts = append(parts, station.CountryCode)
	}
	if codec := station.Codec; codec != "" && !strings.EqualFold(codec, "UNKNOWN") {
		parts = append(parts, codec)
	}
	if station.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", station.Bitrate))
	}
	if station.HLS != 0 {
		parts = append(parts, "HLS")
	}
	if len(parts) == 0 {
		return "Radio"
	}
	return strings.Join(parts, " · ")
}

// initialsOf is the first letters of a name's first and last words, for a
// station without a logo.
func initialsOf(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "?"
	}
	first := []rune(words[0])
	if len(words) == 1 {
		return strings.ToUpper(string(first[0]))
	}
	last := []rune(words[len(words)-1])
	return strings.ToUpper(string(first[0]) + string(last[0]))
}

// orderIndex is the position of order in the segmented control.
func (a *App) orderIndex(order radiobrowser.Order) int {
	for i, o := range browseOrders {
		if o.order == order {
			return i
		}
	}
	return 0
}

func orderLabels() []string {
	labels := make([]string, len(browseOrders))
	for i, o := range browseOrders {
		labels[i] = o.label
	}
	return labels
}

// stationIndex finds a station by its UUID among those shown.
func (a *App) stationIndex(uuid string) int {
	s := a.engine.Snapshot()
	for i, station := range s.Stations {
		if station.UUID == uuid {
			return i
		}
	}
	return -1
}

// logoState is a logo URL's picture, or the failure that stood in for it.
type logoState struct {
	bitmap *ui.Bitmap
	// failed is when the last attempt failed; zero means none did. A
	// URL that failed is left alone for a while, so that a broken one
	// does not send a request every time its card is built.
	failed time.Time
}

// The logo cache is bounded: a long scroll must not keep every picture it
// passed on the GPU.
const (
	maxLogos      = 96
	logoRetryWait = 5 * time.Minute
)

// maxLogoLoads bounds the logo requests in flight at once, so that a grid
// of stations does not make a hundred connections at once.
const maxLogoLoads = 6

// stationLogo returns the station's logo if it has been loaded, and starts
// loading it when it has not.
//
// A logo is kept by its URL: many stations share one picture, and it is
// downloaded once for all of them.
func (a *App) stationLogo(station radiobrowser.Station) *ui.Bitmap {
	url := station.Favicon
	if url == "" {
		return nil
	}

	a.logoMu.Lock()
	defer a.logoMu.Unlock()

	if state, ok := a.logos[url]; ok {
		if state.bitmap != nil {
			a.touchLogoLocked(url)
			return state.bitmap
		}
		if !state.failed.IsZero() && time.Since(state.failed) < logoRetryWait {
			return nil
		}
	}
	if a.logoLoading >= maxLogoLoads {
		return nil
	}
	// Claim the URL before the goroutine starts, so that a second card
	// with the same picture does not ask for it again.
	if _, loading := a.logoInFlight[url]; loading {
		return nil
	}
	if a.logoInFlight == nil {
		a.logoInFlight = map[string]bool{}
	}
	a.logoInFlight[url] = true
	a.logoLoading++
	go a.loadLogo(url)
	return nil
}

// touchLogoLocked makes url the most recently used picture.
func (a *App) touchLogoLocked(url string) {
	for i, u := range a.logoOrder {
		if u == url {
			a.logoOrder = append(a.logoOrder[:i], a.logoOrder[i+1:]...)
			break
		}
	}
	a.logoOrder = append(a.logoOrder, url)
}

// storeLogoLocked remembers a picture and drops the least recently used
// one when the cache is full.
func (a *App) storeLogoLocked(url string, bitmap *ui.Bitmap) {
	a.logos[url] = logoState{bitmap: bitmap}
	a.touchLogoLocked(url)
	for len(a.logoOrder) > maxLogos {
		oldest := a.logoOrder[0]
		a.logoOrder = a.logoOrder[1:]
		delete(a.logos, oldest)
	}
}

// loadLogo reads a logo and hands the bitmap to the window, where it must
// be made. It runs on a goroutine of its own.
func (a *App) loadLogo(url string) {
	// The count of loads must go down whatever happens, including a
	// window that has closed and drops the callback that would redraw it.
	defer func() {
		a.logoMu.Lock()
		delete(a.logoInFlight, url)
		a.logoLoading--
		a.logoMu.Unlock()
	}()

	bitmap, err := fetchBitmap(url)

	if err != nil {
		a.logoMu.Lock()
		a.logos[url] = logoState{failed: time.Now()}
		a.logoMu.Unlock()
		return
	}

	// The bitmap is plain pixels until a frame paints it, so it is kept
	// here and not through the window: a window that has gone drops its
	// callbacks, and what was read would be lost with it.
	a.logoMu.Lock()
	a.storeLogoLocked(url, bitmap)
	a.logoMu.Unlock()
	a.onMain(func() {}) // asks for a frame, when there is a window
}

// fetchBitmap reads an image from url, on the caller's goroutine. The
// bitmap itself is made on the main thread, where MyGo expects it.
func fetchBitmap(url string) (*ui.Bitmap, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gocc")
	res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("logo: %d", res.StatusCode)
	}
	// Favicons are small; a page that answers instead of one is not.
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ui.DecodeBitmap(data)
}
