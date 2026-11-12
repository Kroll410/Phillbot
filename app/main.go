// СабДей — програма для збору замовлень через нагороду за бали каналу Twitch (Wails: Go + WebView2).
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// version задається при збірці: -ldflags "-X main.version=v0.1.0" (див. .github/workflows/build.yml)
var version = "dev"

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "СабДей " + version,
		Width:            1360,
		Height:           860,
		MinWidth:         960,
		MinHeight:        600,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 14, G: 14, B: 16, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "subday-twitch-tool-3f1c",
		},
		Windows: &windows.Options{
			Theme: windows.Dark,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
