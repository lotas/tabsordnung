package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lotas/tabsordnung/internal/analyzer"
	"github.com/lotas/tabsordnung/internal/export"
	"github.com/lotas/tabsordnung/internal/firefox"
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

	// Resolve summarize config
	resolvedModel := os.Getenv("TABSORDNUNG_MODEL")
	if resolvedModel == "" {
		resolvedModel = "llama3.2"
	}
	ollamaHost := os.Getenv("OLLAMA_HOST")
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}
	summaryDir := os.Getenv("TABSORDNUNG_SUMMARY_DIR")
	if summaryDir == "" {
		home, _ := os.UserHomeDir()
		summaryDir = filepath.Join(home, ".local", "share", "tabsordnung", "summaries")
	}

	signalDir := os.Getenv("TABSORDNUNG_SIGNAL_DIR")
	if signalDir == "" {
		home, _ := os.UserHomeDir()
		signalDir = filepath.Join(home, ".local", "share", "tabsordnung", "signals")
	}

	model := tui.NewModel(profiles, *staleDays, *liveMode, srv, summaryDir, resolvedModel, ollamaHost, signalDir)
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

  tabsordnung snapshot [--profile X] [--label "text"]  Auto-snapshot (only if changed)
  tabsordnung snapshot list                            List saved snapshots
  tabsordnung snapshot diff [rev] [rev2] [--profile X] Compare snapshots or current tabs
  tabsordnung snapshot delete <rev> [--profile X] [--yes]  Delete a snapshot
  tabsordnung snapshot restore <rev> [--profile X] [--port N]  Restore tabs via live mode

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
	// If no args or first arg is a flag, it's the auto-create flow.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		runSnapshotCreate(args)
		return
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "create":
		runSnapshotCreate(subArgs)
	case "list":
		runSnapshotList()
	case "diff":
		runSnapshotDiff(subArgs)
	case "delete":
		runSnapshotDelete(subArgs)
	case "restore":
		runSnapshotRestore(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown snapshot command %q. Use list, diff, delete, or restore.\n", subcmd)
		os.Exit(1)
	}
}

func runSnapshotCreate(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	label := fs.String("label", "", "Optional label for the snapshot")
	fs.Parse(args)

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

	rev, created, diff, err := snapshot.Create(db, session, *label)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating snapshot: %v\n", err)
		os.Exit(1)
	}

	if !created {
		fmt.Printf("No changes since snapshot #%d\n", rev)
		return
	}

	groups := 0
	for _, g := range session.Groups {
		if g.ID != "" {
			groups++
		}
	}
	fmt.Printf("Snapshot #%d created: %d tabs in %d groups\n", rev, len(session.AllTabs), groups)

	if diff != nil && (len(diff.Added) > 0 || len(diff.Removed) > 0) {
		fmt.Println()
		if len(diff.Added) > 0 {
			fmt.Printf("+ Added (%d):\n", len(diff.Added))
			for _, e := range diff.Added {
				if e.Group != "" {
					fmt.Printf("  + %s [%s]\n", e.URL, e.Group)
				} else {
					fmt.Printf("  + %s\n", e.URL)
				}
			}
		}
		if len(diff.Removed) > 0 {
			if len(diff.Added) > 0 {
				fmt.Println()
			}
			fmt.Printf("- Removed (%d):\n", len(diff.Removed))
			for _, e := range diff.Removed {
				if e.Group != "" {
					fmt.Printf("  - %s [%s]\n", e.URL, e.Group)
				} else {
					fmt.Printf("  - %s\n", e.URL)
				}
			}
		}
	}
}

func runSnapshotList() {
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

	fmt.Printf("%-5s %5s  %-12s %-20s  %s\n", "REV", "TABS", "PROFILE", "LABEL", "CREATED")
	for _, s := range snaps {
		fmt.Printf("%5d %5d  %-12s %-20s  %s\n",
			s.Rev,
			s.TabCount,
			s.Profile,
			s.Name,
			s.CreatedAt.Format("2006-01-02 15:04"),
		)
	}
}

func runSnapshotDiff(args []string) {
	fs := flag.NewFlagSet("snapshot diff", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	fs.Parse(reorderArgs(args))

	profile := resolveProfileName(*profileName)

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	switch fs.NArg() {
	case 0:
		// Diff latest vs current.
		session, err := resolveSession(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		result, err := snapshot.DiffAgainstCurrent(db, session.Profile.Name, 0, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	case 1:
		// Diff specific rev vs current.
		rev, err := strconv.Atoi(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
			os.Exit(1)
		}
		session, err := resolveSession(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		result, err := snapshot.DiffAgainstCurrent(db, session.Profile.Name, rev, session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	case 2:
		// Diff two revisions.
		rev1, err := strconv.Atoi(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
			os.Exit(1)
		}
		rev2, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(1))
			os.Exit(1)
		}
		// For rev-vs-rev diff we need a profile name.
		resolvedProfile := profile
		if resolvedProfile == "" {
			session, err := resolveSession("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			resolvedProfile = session.Profile.Name
		}
		result, err := snapshot.DiffRevisions(db, resolvedProfile, rev1, rev2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(snapshot.FormatDiff(result))

	default:
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot diff [rev] [rev2] [--profile name]")
		os.Exit(1)
	}
}

func runSnapshotDelete(args []string) {
	fs := flag.NewFlagSet("snapshot delete", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	yes := fs.Bool("yes", false, "Skip confirmation prompt")
	fs.Parse(reorderArgs(args))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot delete <rev> [--profile name] [--yes]")
		os.Exit(1)
	}

	rev, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
		os.Exit(1)
	}

	// Resolve profile.
	profile := resolveProfileName(*profileName)
	if profile == "" {
		session, err := resolveSession("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		profile = session.Profile.Name
	}

	if !*yes {
		fmt.Printf("Delete snapshot #%d? [y/N] ", rev)
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

	if err := storage.DeleteSnapshot(db, profile, rev); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting snapshot: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Snapshot #%d deleted.\n", rev)
}

func runSnapshotRestore(args []string) {
	fs := flag.NewFlagSet("snapshot restore", flag.ExitOnError)
	profileName := fs.String("profile", "", "Firefox profile name")
	port := fs.Int("port", 19191, "WebSocket port for live mode")
	fs.Parse(reorderArgs(args))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tabsordnung snapshot restore <rev> [--profile name] [--port N]")
		os.Exit(1)
	}

	rev, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid revision number: %s\n", fs.Arg(0))
		os.Exit(1)
	}

	// Resolve profile.
	profile := resolveProfileName(*profileName)
	if profile == "" {
		session, err := resolveSession("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		profile = session.Profile.Name
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := snapshot.Restore(db, profile, rev, *port); err != nil {
		fmt.Fprintf(os.Stderr, "Error restoring snapshot: %v\n", err)
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
