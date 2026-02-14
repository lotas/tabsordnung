package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lotas/tabsordnung/internal/analyzer"
	"github.com/lotas/tabsordnung/internal/export"
	"github.com/lotas/tabsordnung/internal/firefox"
	"path/filepath"

	"github.com/lotas/tabsordnung/internal/server"
	"github.com/lotas/tabsordnung/internal/snapshot"
	"github.com/lotas/tabsordnung/internal/storage"
	"github.com/lotas/tabsordnung/internal/summarize"
	"github.com/lotas/tabsordnung/internal/triage"
	"github.com/lotas/tabsordnung/internal/tui"
	"github.com/lotas/tabsordnung/internal/types"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "snapshot":
			runSnapshot(os.Args[2:])
			return
		case "triage":
			runTriage(os.Args[2:])
			return
		case "export":
			runExport(os.Args[2:])
			return
		case "summarize":
			runSummarize(os.Args[2:])
			return
		case "profiles":
			runProfiles()
			return
		case "help", "--help", "-h":
			printHelp()
			return
		}
	}

	fs := flag.NewFlagSet("tabsordnung", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name (skip picker)")
	staleDays := fs.Int("stale-days", 7, "Days before a tab is considered stale")
	liveMode := fs.Bool("live", false, "Start in live mode (connect to extension)")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(os.Args[1:])

	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering Firefox profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No Firefox profiles found.")
		os.Exit(1)
	}

	// If --profile flag or TABSORDNUNG_PROFILE env var is set, filter to just that profile
	resolved := resolveProfileName(*profileName)
	if resolved != "" {
		var filtered []types.Profile
		for _, p := range profiles {
			if p.Name == resolved {
				filtered = append(filtered, p)
				break
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "Profile %q not found. Available profiles:\n", resolved)
			for _, p := range profiles {
				fmt.Fprintf(os.Stderr, "  - %s\n", p.Name)
			}
			os.Exit(1)
		}
		profiles = filtered
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

func printHelp() {
	fmt.Print(`tabsordnung — Firefox tab analyzer

Usage:
  tabsordnung                                          Start the TUI (default)
    --profile <name>       Firefox profile name (skips picker)
    --stale-days <n>       Days before a tab is considered stale (default: 7)
    --live                 Start in live mode (connect to extension)
    --port <n>             WebSocket port for live mode (default: 19191)

  tabsordnung export                                   Export tabs to stdout or file
    --profile <name>       Firefox profile name
    --json                 Export as JSON instead of markdown
    --out <file>           Output file path (default: stdout)
    --live                 Export from live extension instead of session file
    --port <n>             WebSocket port for live mode (default: 19191)

  tabsordnung profiles                                 List Firefox profiles

  tabsordnung snapshot create <name> [--profile X]     Save current tabs
  tabsordnung snapshot list                            List saved snapshots
  tabsordnung snapshot diff <name> [--profile X]       Compare snapshot to current tabs
  tabsordnung snapshot delete <name> [--yes]           Delete a snapshot
  tabsordnung snapshot restore <name> [--port N]       Restore tabs via live mode

  tabsordnung triage                                   Classify GitHub tabs into groups
    --profile <name>       Firefox profile name
    --apply                Apply moves without confirmation
    --port <n>             WebSocket port for live mode (default: 19191)

  tabsordnung summarize                                  Summarize tabs via Ollama
    --profile <name>       Firefox profile name
    --model <name>         Ollama model (env: TABSORDNUNG_MODEL, default: llama3.2)
    --out-dir <path>       Output directory (default: ~/.local/share/tabsordnung/summaries/)
    --group <name>         Tab group to summarize (default: "Summarize This")

Environment:
  TABSORDNUNG_PROFILE    Default Firefox profile (overridden by --profile flag)
  TABSORDNUNG_MODEL      Default Ollama model (overridden by --model flag)
  OLLAMA_HOST            Ollama server URL (default: http://localhost:11434)
`)
}

func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	jsonFlag := fs.Bool("json", false, "Export as JSON instead of markdown")
	outFile := fs.String("out", "", "Output file path (default: stdout)")
	liveMode := fs.Bool("live", false, "Export from live extension instead of session file")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(args)

	var data *types.SessionData
	var err error

	if *liveMode {
		data, err = exportLive(*port)
	} else {
		data, err = resolveSession(resolveProfileName(*profileName))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var output string
	if *jsonFlag {
		output, err = export.JSON(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating JSON: %v\n", err)
			os.Exit(1)
		}
	} else {
		output = export.Markdown(data)
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, []byte(output), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Print(output)
	}
}

func runProfiles() {
	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering Firefox profiles: %v\n", err)
		os.Exit(1)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No Firefox profiles found.")
		os.Exit(1)
	}

	for _, p := range profiles {
		suffix := ""
		if p.IsDefault {
			suffix = " [default]"
		}
		fmt.Printf("%s (%s)%s\n", p.Name, p.Path, suffix)
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

// resolveSession discovers profiles and reads session data for the given
// profile name. If profileName is empty, it uses the default profile
// (IsDefault=true), falling back to the first profile found.
func resolveSession(profileName string) (*types.SessionData, error) {
	profiles, err := firefox.DiscoverProfiles()
	if err != nil {
		return nil, fmt.Errorf("discover profiles: %w", err)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no Firefox profiles found")
	}

	var profile types.Profile
	if profileName != "" {
		found := false
		for _, p := range profiles {
			if p.Name == profileName {
				profile = p
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("profile %q not found", profileName)
		}
	} else {
		// Use default profile, fall back to first.
		profile = profiles[0]
		for _, p := range profiles {
			if p.IsDefault {
				profile = p
				break
			}
		}
	}

	session, err := firefox.ReadSessionFile(profile.Path)
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	session.Profile = profile
	return session, nil
}

func openDB() (*sql.DB, error) {
	dbPath, err := storage.DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return storage.OpenDB(dbPath)
}

// reorderArgs moves flag arguments before positional arguments so that
// flag.Parse handles them correctly (it stops at the first non-flag arg).
func reorderArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

// resolveProfileName returns the profile name from the flag if set,
// otherwise falls back to the TABSORDNUNG_PROFILE environment variable.
func resolveProfileName(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("TABSORDNUNG_PROFILE")
}

func runSnapshot(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot <create|list|diff|delete|restore> ...")
		os.Exit(1)
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "create":
		fs := flag.NewFlagSet("snapshot create", flag.ExitOnError)
		profileName := fs.String("profile", "", "Firefox profile name")
		fs.Parse(reorderArgs(subArgs))

		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot create <name> [--profile name]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		session, err := resolveSession(resolveProfileName(*profileName))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		db, err := openDB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := snapshot.Create(db, name, session); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating snapshot: %v\n", err)
			os.Exit(1)
		}

	case "list":
		db, err := openDB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		snaps, err := storage.ListSnapshots(db)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing snapshots: %v\n", err)
			os.Exit(1)
		}

		if len(snaps) == 0 {
			fmt.Println("No snapshots found.")
			return
		}

		fmt.Printf("%-40s %5s %8s  %s\n", "NAME", "TABS", "PROFILE", "CREATED")
		for _, s := range snaps {
			fmt.Printf("%-40s %5d %8s  %s\n",
				s.Name,
				s.TabCount,
				s.Profile,
				s.CreatedAt.Format("2006-01-02 15:04"),
			)
		}

	case "diff":
		fs := flag.NewFlagSet("snapshot diff", flag.ExitOnError)
		profileName := fs.String("profile", "", "Firefox profile name")
		fs.Parse(reorderArgs(subArgs))

		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot diff <name> [--profile name]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		session, err := resolveSession(resolveProfileName(*profileName))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		db, err := openDB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		result, err := snapshot.Diff(db, name, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error computing diff: %v\n", err)
			os.Exit(1)
		}

		fmt.Print(snapshot.FormatDiff(result))

	case "delete":
		fs := flag.NewFlagSet("snapshot delete", flag.ExitOnError)
		yes := fs.Bool("yes", false, "Skip confirmation prompt")
		fs.Parse(reorderArgs(subArgs))

		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot delete <name> [--yes]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		if !*yes {
			fmt.Printf("Delete snapshot %q? [y/N] ", name)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				fmt.Println("Aborted.")
				return
			}
		}

		db, err := openDB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := storage.DeleteSnapshot(db, name); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting snapshot: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Snapshot %q deleted.\n", name)

	case "restore":
		fs := flag.NewFlagSet("snapshot restore", flag.ExitOnError)
		port := fs.Int("port", 19191, "WebSocket port for live mode")
		fs.Parse(reorderArgs(subArgs))

		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot restore <name> [--port N]")
			os.Exit(1)
		}
		name := fs.Arg(0)

		db, err := openDB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := snapshot.Restore(db, name, *port); err != nil {
			fmt.Fprintf(os.Stderr, "Error restoring snapshot: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown snapshot command %q. Use create, list, diff, delete, or restore.\n", subcmd)
		os.Exit(1)
	}
}

func runTriage(args []string) {
	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	apply := fs.Bool("apply", false, "Apply moves via live mode (skip confirmation)")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(args)

	session, err := resolveSession(resolveProfileName(*profileName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	token := analyzer.ResolveGitHubToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no GitHub token available. Run 'gh auth login' or set GITHUB_TOKEN.")
		os.Exit(1)
	}

	username, err := analyzer.ResolveGitHubUser(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving GitHub user: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Fetching GitHub status for %d tabs (as @%s)...\n", len(session.AllTabs), username)
	analyzer.AnalyzeGitHubTriage(session.AllTabs, username)

	result := triage.Classify(session.AllTabs)
	fmt.Print(triage.FormatDryRun(result))

	total := len(result.NeedsAttention) + len(result.OpenPRs) + len(result.OpenIssues) + len(result.ClosedMerged)
	if total == 0 {
		fmt.Println("No GitHub tabs to triage.")
		return
	}

	if !*apply {
		fmt.Print("Apply? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("No changes applied.")
			return
		}
	}

	if err := triage.Apply(result, *port); err != nil {
		fmt.Fprintf(os.Stderr, "Error applying triage: %v\n", err)
		os.Exit(1)
	}
}

func runSummarize(args []string) {
	fs := flag.NewFlagSet("summarize", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	model := fs.String("model", "", "Ollama model name (default: llama3.2)")
	outDir := fs.String("out-dir", "", "Output directory for summary files")
	groupName := fs.String("group", "Summarize This", "Tab group name to summarize")
	fs.Parse(args)

	session, err := resolveSession(resolveProfileName(*profileName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve model: flag > env > default.
	resolvedModel := *model
	if resolvedModel == "" {
		resolvedModel = os.Getenv("TABSORDNUNG_MODEL")
	}
	if resolvedModel == "" {
		resolvedModel = "llama3.2"
	}

	// Resolve Ollama host: env > default.
	ollamaHost := os.Getenv("OLLAMA_HOST")
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}

	// Resolve output directory: flag > default.
	resolvedOutDir := *outDir
	if resolvedOutDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		resolvedOutDir = filepath.Join(home, ".local", "share", "tabsordnung", "summaries")
	}

	cfg := summarize.Config{
		OutDir:     resolvedOutDir,
		Model:      resolvedModel,
		OllamaHost: ollamaHost,
		GroupName:  *groupName,
		Session:    session,
	}

	if err := summarize.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
