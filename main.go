// Command gocc controls Chromecast devices on the local network: it finds
// them, plays the streams of a playlist on the one chosen, and sets what
// it plays and how loud.
//
// This is the desktop app, whose window MyGo draws itself. The command
// line version, gocc-cli, serves the same thing to a browser instead.
//
//	gocc
package main

import (
	"context"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/app"
)

func main() {
	// Name the app before anything asks it where its data lives: the
	// playlist is stored under a directory named after the app, and a
	// name set later would send it somewhere else.
	mygo.App.SetName("gocc")

	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Printf("gocc: finding the data directory: %v", err)
	}

	application, err := app.New(dataDir)
	if err != nil {
		log.Fatalf("gocc: %v", err)
	}
	defer application.Close()

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "gocc",
			Width:     1080,
			Height:    660,
			MinWidth:  840,
			MinHeight: 480,
			Content:   ui.View(application.View),
		})
		application.Attach(win)
		application.Start(context.Background())
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatalf("gocc: %v", err)
	}
}
