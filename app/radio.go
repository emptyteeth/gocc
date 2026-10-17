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
	{"Bitrate", radiobrowser.OrderBitrate},
	{"Name", radiobrowser.OrderName},
}

// rightPane draws the right column: the favorites, or the catalogue to
// browse, chosen by the segmented control at its top.
func (a *App) rightPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Width(360).FillHeight().Padding(12).Gap(8).Children(func() {
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

	grid := ui.GridView(c, &a.grid, len(s.Stations), 150, 172, func(i int) {
		a.stationCard(c, t, s.Stations[i], s.Status.URL)
	}).Grow(1)
	// A double click, or Enter, plays the station.
	if grid.Submitted() && a.stationSel >= 0 {
		a.engine.PlayStation(a.stationSel)
	}
}

// stationCard draws one station: its logo, its name, and what it plays.
func (a *App) stationCard(c *ui.Context, t *ui.Theme, station radiobrowser.Station, playingURL string) {
	ui.Column(c).Gap(4).Fill().Padding(4).Radius(6).Background(t.Surface).Children(func() {
		ui.Box(c).Height(80).Radius(6).Clip().Background(t.SurfaceHover).Children(func() {
			if bitmap := a.stationLogo(station); bitmap != nil {
				ui.Image(c, bitmap).Fit(ui.Cover).Grow(1)
			} else {
				// No logo yet, or none to be had: the station's initials
				// on a color of its own, as an avatar does.
				ui.Text(c, initialsOf(station.Name)).FontSize(26).Bold().
					TextColor(t.TextMuted).AlignSelf(ui.Center).Margin(0, ui.Auto)
			}
		})
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if station.URLToPlay() != "" && station.URLToPlay() == playingURL {
				ui.Text(c, "▶").FontSize(10).TextColor(t.Accent)
			}
			ui.Text(c, station.Name).FontSize(12).Grow(1).MaxLines(1)
			if a.engine.IsFavorite(station.URLToPlay()) {
				ui.Text(c, "★").FontSize(11).TextColor(t.Accent)
			}
		})
		ui.Text(c, stationDetail(station)).FontSize(10).TextColor(t.TextMuted).SingleLine()
		ui.Row(c).Gap(4).Children(func() {
			ui.Button(c, "Play").FontSize(11).Label("Play " + station.Name).OnClick(func() {
				if i := a.stationIndex(station.UUID); i >= 0 {
					a.engine.PlayStation(i)
				}
			})
			saved := a.engine.IsFavorite(station.URLToPlay())
			fav := "Save"
			if saved {
				fav = "Saved"
			}
			label := "Save " + station.Name
			if saved {
				label = "Saved " + station.Name
			}
			ui.Button(c, fav).FontSize(11).Label(label).Tooltip("Save to favorites").OnClick(func() {
				if err := a.engine.ToggleFavorite(station.URLToPlay()); err != nil {
					a.engine.ReportError(err)
				}
			})
		})
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

// stationLogo returns the station's logo if it has been loaded, and starts
// loading it when it has not.
func (a *App) stationLogo(station radiobrowser.Station) *ui.Bitmap {
	if station.Favicon == "" {
		return nil
	}
	if bitmap, ok := a.logos[station.UUID]; ok {
		return bitmap
	}
	if a.loading[station.UUID] || a.logoLoading >= maxLogoLoads {
		return nil
	}
	a.loading[station.UUID] = true
	a.logoLoading++
	go a.loadLogo(station.UUID, station.Favicon)
	return nil
}

// maxLogoLoads bounds the logo requests in flight at once, so that a grid
// of stations does not make a hundred connections at once.
const maxLogoLoads = 6

// loadLogo reads a station's logo and hands the bitmap to the window,
// where it must be made.
func (a *App) loadLogo(uuid, url string) {
	bitmap, err := fetchBitmap(url)
	if err != nil {
		// A station with no reachable logo keeps its initials.
		a.onMain(func() {
			a.loading[uuid] = false
			a.logoLoading--
		})
		return
	}
	a.onMain(func() {
		a.loading[uuid] = false
		a.logoLoading--
		if a.logos == nil {
			a.logos = map[string]*ui.Bitmap{}
		}
		a.logos[uuid] = bitmap
	})
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
