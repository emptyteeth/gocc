package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
)

// view builds the window: the devices on the left, what the chosen device
// is doing in the middle, and the playlist on the right.
func (a *App) view(c *ui.Context) {
	t := c.Theme()
	s := a.engine.Snapshot()

	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.devicesPane(c, t, s)
		separator(c, t)
		a.controlPane(c, t, s)
		separator(c, t)
		a.playlistPane(c, t, s)
	})
}

// separator draws the line between two panes.
func separator(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(1).FillHeight().Background(t.Border)
}

// devicesPane lists the devices found on the network, and lets one be
// chosen to control.
func (a *App) devicesPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Width(250).FillHeight().Padding(12).Gap(8).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Devices").FontSize(15).Bold().Grow(1)
			ui.Button(c, "Scan").Tooltip("Look for devices again").OnClick(a.engine.Rediscover)
		})

		// mDNS answers on every interface, so on a machine with more
		// than one the devices behind a VPN or a container bridge show
		// up beside the real ones. Let the user say which to listen on.
		if len(s.Interfaces) > 1 {
			names := make([]string, 0, len(s.Interfaces)+1)
			names = append(names, "All interfaces")
			for _, iface := range s.Interfaces {
				names = append(names, iface.Name)
			}
			pick := "All interfaces"
			if s.Iface != "" {
				pick = s.Iface
			}
			sel := ui.Select(c, &pick, names).Label("Network interface")
			if sel.Changed() && pick != s.Iface {
				if pick == "All interfaces" {
					a.engine.UseInterface(nil)
				} else if iface, err := cast.InterfaceByName(pick); err == nil {
					a.engine.UseInterface(iface)
				}
			}
		}

		if a.deviceSel >= len(s.Devices) {
			a.deviceSel = -1
		}
		a.deviceList.Key = func(i int) any { return s.Devices[i].UUID }
		a.deviceList.Label = func(i int) string { return deviceName(s.Devices[i]) }
		a.deviceList.Selected = &a.deviceSel

		list := ui.List(c, &a.deviceList, len(s.Devices), func(i int) {
			deviceRow(c, t, s.Devices[i], s.Selected == s.Devices[i].UUID)
		}).Grow(1)
		list.Children(func() {
			if len(s.Devices) == 0 {
				ui.Column(c).Padding(8).Gap(6).Children(func() {
					ui.Text(c, "Looking for devices…").TextColor(t.TextMuted).FontSize(12)
					ui.Text(c, "Make sure the device is on the same network.").
						TextColor(t.TextMuted).FontSize(11)
				})
			}
		})
		// A click chooses a device and connects to it.
		if list.Changed() && a.deviceSel >= 0 && a.deviceSel < len(s.Devices) {
			a.engine.Choose(s.Devices[a.deviceSel])
		}
	})
}

// deviceRow shows one device, marked when it is the one being controlled.
func deviceRow(c *ui.Context, t *ui.Theme, device cast.Device, connected bool) {
	text, muted := rowColors(t, connected)
	ui.Column(c).Padding(6, 8).Gap(2).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if connected {
				ui.Text(c, "●").FontSize(11)
			}
			ui.Text(c, deviceName(device)).TextColor(text).SingleLine()
		})
		ui.Text(c, device.Addr).FontSize(11).TextColor(muted).SingleLine()
	})
}

// rowColors picks the colors of a row's text. A chosen row is filled with
// the accent color, so its text, secondary lines included, must be
// readable on it rather than the muted color of the page.
func rowColors(t *ui.Theme, chosen bool) (text, muted ui.Color) {
	if chosen {
		return t.AccentText, t.AccentText
	}
	return t.Text, t.TextMuted
}

// deviceName names a device, falling back to its address when it does not
// say what it is called.
func deviceName(device cast.Device) string {
	if device.Name != "" {
		return device.Name
	}
	if device.Model != "" {
		return device.Model
	}
	return device.Addr
}

// controlPane shows what the chosen device is doing, and the controls for
// it: play and pause, stop, and the volume.
func (a *App) controlPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Grow(1).FillHeight().Padding(16).Gap(12).Children(func() {
		switch {
		case s.Selected == "":
			a.emptyControlPane(c, t)
		default:
			if device, ok := findDevice(s.Devices, s.Selected); ok {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, deviceName(device)).FontSize(16).Bold().SingleLine()
						ui.Text(c, device.Addr).FontSize(11).TextColor(t.TextMuted).SingleLine()
					})
					ui.Button(c, "Disconnect").OnClick(a.engine.Disconnect)
				})
			} else {
				ui.Text(c, "The device is gone").FontSize(16).Bold()
				ui.Text(c, "It stopped answering. Scan again, or choose another.").
					TextColor(t.TextMuted)
			}

			if s.Connecting {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Connecting…").TextColor(t.TextMuted)
				})
			} else {
				a.statusCard(c, t, s)
				a.transport(c, t, s)
				a.volumeRow(c, t, s)
			}
		}

		// An error is shown whatever else the pane holds: the mistake of
		// playing with no device chosen must not be swallowed by the pane
		// saying that no device is chosen.
		if s.Err != "" {
			ui.Column(c).Gap(2).Padding(8).Radius(6).Background(t.Surface).Children(func() {
				ui.Text(c, "Last error").FontSize(11).Bold().TextColor(t.Danger)
				ui.Text(c, s.Err).FontSize(11).TextColor(t.TextMuted).Selectable()
			})
		}
	})
}

// emptyControlPane is what the middle pane shows before a device is chosen.
func (a *App) emptyControlPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Center().Gap(8).Children(func() {
		ui.Text(c, "No device selected").FontSize(16).Bold()
		ui.Text(c, "Choose a device on the left to control it.").
			TextColor(t.TextMuted).TextAlign(ui.Center)
	})
}

// statusCard shows what the device is playing.
func (a *App) statusCard(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Gap(6).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.Status.Describe()).FontSize(14).Bold()
			if s.Status.Muted {
				ui.Badge(c, "Muted")
			}
		})
		if title := mediaTitle(s.Status); title != "" {
			ui.Text(c, title).FontSize(13).SingleLine()
		}
		if s.Status.URL != "" {
			ui.Text(c, s.Status.URL).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		// The hint is for a device the user has not asked anything of; it
		// would only be noise while the wait for a stream is on.
		if s.Status.State == cast.StateIdle && !s.Loading {
			ui.Text(c, "Nothing is playing. Double-click an entry of the playlist.").
				FontSize(11).TextColor(t.TextMuted)
		}
	})
}

// mediaTitle names what plays, from the device's own metadata.
func mediaTitle(status cast.Status) string {
	switch {
	case status.Title != "" && status.Artist != "":
		return status.Artist + " — " + status.Title
	case status.Title != "":
		return status.Title
	case status.Artist != "":
		return status.Artist
	}
	return ""
}

// transport draws the buttons that control playback.
func (a *App) transport(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	playing := s.Status.Playing()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		label := "Play"
		if playing {
			label = "Pause"
		}
		ui.PrimaryButton(c, label).Width(96).OnClick(a.engine.TogglePlay)
		ui.Button(c, "Stop").Width(96).Disabled(s.Status.State == cast.StateIdle).
			OnClick(a.engine.Stop)
		ui.Button(c, "Refresh").Tooltip("Read the device's state again").
			OnClick(a.engine.Refresh)
	})

	// A device buffers a stream before it plays it, and the app is
	// otherwise quiet for that moment: say what is being waited for, on a
	// line of its own so that it is not cut short.
	if s.Loading {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, s.LoadingReason).TextColor(t.TextMuted).SingleLine()
		})
	}
}

// volumeRow draws the volume slider and the mute check box.
func (a *App) volumeRow(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	// The engine keeps the level the device reports; the slider follows it
	// unless the user has just moved it.
	if time.Since(a.volumeAt) > 2*time.Second {
		a.volume = s.Volume
	}
	if time.Since(a.mutedAt) > 2*time.Second {
		a.muted = s.Muted
	}

	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			slider := ui.Slider(c, &a.volume, 0, 1).Label("Volume").Grow(1)
			ui.Textf(c, "%d%%", int(a.volume*100+0.5)).Width(44).TextColor(t.TextMuted)
			if slider.Changed() {
				a.volumeAt = time.Now()
				a.engine.SetVolume(a.volume)
			}
		})
		mute := ui.Checkbox(c, &a.muted, "Mute")
		if mute.Changed() {
			a.mutedAt = time.Now()
			a.engine.SetMuted(a.muted)
		}
	})
}

// playlistPane lists the entries, and lets them be played, added, moved
// and removed.
func (a *App) playlistPane(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Width(330).FillHeight().Padding(12).Gap(8).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Playlist").FontSize(15).Bold().Grow(1)
			ui.Button(c, "Up").Tooltip("Move the entry up").Disabled(a.chosen <= 0).
				OnClick(func() { a.engine.MoveEntry(a.chosen, a.chosen-1) })
			ui.Button(c, "Down").Tooltip("Move the entry down").
				Disabled(a.chosen < 0 || a.chosen >= len(s.Playlist)-1).
				OnClick(func() { a.engine.MoveEntry(a.chosen, a.chosen+2) })
			ui.Button(c, "Remove").Tooltip("Take the entry out").
				Disabled(a.chosen < 0).OnClick(func() { a.engine.RemoveEntry(a.chosen) })
		})

		// The list writes the chosen row through the pointer it is given,
		// so it points at a field of the app; the engine is told after a
		// click, and is followed when it changes the choice itself, as
		// removing an entry does.
		if a.chosen != s.Chosen {
			a.chosen = s.Chosen
		}
		a.list.Key = func(i int) any { return s.Playlist[i].Name + "\x00" + s.Playlist[i].URL }
		a.list.Label = func(i int) string { return s.Playlist[i].Name }
		a.list.Selected = &a.chosen

		list := ui.List(c, &a.list, len(s.Playlist), func(i int) {
			a.playlistRow(c, t, s.Playlist[i], a.chosen == i, s.Status.URL)
		}).Grow(1)
		list.Children(func() {
			if len(s.Playlist) == 0 {
				ui.Text(c, "No entries yet. Add one below.").
					TextColor(t.TextMuted).FontSize(12).Padding(8)
			}
		})
		if list.Changed() {
			a.engine.ChooseEntry(a.chosen)
		}
		// A double click on an entry sends its URL to the device.
		if list.Submitted() && a.chosen >= 0 {
			a.engine.PlayIndex(a.chosen)
		}

		a.addForm(c, t, s)
	})
}

// playlistRow shows one entry, marked when it is what plays.
func (a *App) playlistRow(c *ui.Context, t *ui.Theme, e playlist.Entry, chosen bool, playingURL string) {
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

// addForm adds an entry to the playlist.
func (a *App) addForm(c *ui.Context, t *ui.Theme, s core.Snapshot) {
	ui.Column(c).Gap(6).Padding(8).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, "Add an entry").FontSize(11).Bold().TextColor(t.TextMuted)
		ui.TextInput(c, &a.newName).Placeholder("Name").Label("Name")
		ui.TextInput(c, &a.newURL).Placeholder("http://radio.example/stream.mp3").Label("URL")
		ui.TextInput(c, &a.newType).Placeholder("Content type (optional)").Label("Content type")
		ui.Row(c).Gap(6).Justify(ui.End).Children(func() {
			ui.PrimaryButton(c, "Add").OnClick(a.submitNewEntry)
		})
	})
}

// submitNewEntry checks the form and adds the entry.
func (a *App) submitNewEntry() {
	entry := playlist.Entry{
		Name:        strings.TrimSpace(a.newName),
		URL:         strings.TrimSpace(a.newURL),
		ContentType: strings.TrimSpace(a.newType),
	}
	if err := core.ValidateEntry(entry); err != nil {
		a.engine.ReportError(err)
		return
	}
	if err := a.engine.AddEntry(entry); err != nil {
		return
	}
	a.newName, a.newURL, a.newType = "", "", ""
	a.engine.ChooseEntry(len(a.engine.Snapshot().Playlist) - 1)
}

// findDevice returns the device with this UUID among those found.
func findDevice(devices []cast.Device, uuid string) (cast.Device, bool) {
	for _, device := range devices {
		if device.UUID == uuid {
			return device, true
		}
	}
	return cast.Device{}, false
}
