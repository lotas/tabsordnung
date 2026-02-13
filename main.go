package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nickel-chromium/tabsordnung/internal/export"
	"github.com/nickel-chromium/tabsordnung/internal/firefox"
	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/tui"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func main() {
	profileName := flag.String("profile", "", "Firefox profile name (skip picker)")
	staleDays := flag.Int("stale-days", 7, "Days before a tab is considered stale")
	liveMode := flag.Bool("live", false, "Start in live mode (connect to extension)")
	port := flag.Int("port", 19191, "WebSocket port for live mode")
	exportMode := flag.Bool("export", false, "Export tabs and exit")
	exportFormat := flag.String("format", "markdown", "Export format: markdown or json")
	outFile := flag.String("out", "", "Output file path (default: stdout)")
	listProfiles := flag.Bool("list-profiles", false, "List Firefox profiles and exit")
	flag.Parse()

	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering Firefox profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No Firefox profiles found.")
		os.Exit(1)
	}

	if *listProfiles {
		for _, p := range profiles {
			suffix := ""
			if p.IsDefault {
				suffix = " [default]"
			}
			fmt.Printf("%s (%s)%s\n", p.Name, p.Path, suffix)
		}
		return
	}

	// If --profile flag is set, filter to just that profile
	if *profileName != "" {
		var filtered []types.Profile
		for _, p := range profiles {
			if p.Name == *profileName {
				filtered = append(filtered, p)
				break
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "Profile %q not found. Available profiles:\n", *profileName)
			for _, p := range profiles {
				fmt.Fprintf(os.Stderr, "  - %s\n", p.Name)
			}
			os.Exit(1)
		}
		profiles = filtered
	}

	if *exportMode {
		var data *types.SessionData

		if *liveMode {
			data, err = exportLive(*port)
		} else {
			profile := profiles[0]
			data, err = firefox.ReadSessionFile(profile.Path)
			if err == nil {
				data.Profile = profile
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		var output string
		switch *exportFormat {
		case "json":
			output, err = export.JSON(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating JSON: %v\n", err)
				os.Exit(1)
			}
		case "markdown", "md":
			output = export.Markdown(data)
		default:
			fmt.Fprintf(os.Stderr, "Unknown format %q. Use 'markdown' or 'json'.\n", *exportFormat)
			os.Exit(1)
		}
		if *outFile != "" {
			if err := os.WriteFile(*outFile, []byte(output), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Print(output)
		}
		return
	}

	// Always create the server — it's cheap (just a struct + channel).
	// ListenAndServe is only called when the user actually enters live mode.
	srv := server.New(*port)

	model := tui.NewModel(profiles, *staleDays, *liveMode, srv)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func exportLive(port int) (*types.SessionData, error) {
	srv := server.New(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.ListenAndServe(ctx)

	fmt.Fprintf(os.Stderr, "Waiting for Firefox extension on port %d...\n", port)

	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-srv.Messages():
			if msg.Type == "snapshot" {
				return server.ParseSnapshot(msg)
			}
		case <-timeout:
			return nil, fmt.Errorf("timed out waiting for extension (10s)")
		}
	}
}
