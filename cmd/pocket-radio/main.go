// Command pocket-radio is the terminal companion to the PocketRadio menubar app.
//
// Usage:
//
//	pocket-radio           full TUI (M4 — placeholder for now)
//	pocket-radio up_next   mini mode: play the top of Up Next
package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"pocket-radio-console/internal/config"
	"pocket-radio-console/internal/library"
	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/pocketcasts"
	"pocket-radio-console/internal/ui/mini"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		// Bare invocation = full TUI. Lands in M4; placeholder for M1.
		fmt.Fprintln(os.Stderr, "Full TUI is not built yet (milestone M4).")
		fmt.Fprintln(os.Stderr, "Try: pocket-radio up_next")
		os.Exit(0)
	}

	switch args[0] {
	case "up_next":
		if err := runMiniUpNext(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown argument %q (M1 supports: up_next)\n", args[0])
		os.Exit(2)
	}
}

func runMiniUpNext() error {
	// Player is the mpv boundary — fail early with an install hint if missing.
	p, err := player.New()
	if err != nil {
		return err
	}

	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	store, err := config.NewStore(dir)
	if err != nil {
		return err
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	// No cached token and no creds → prompt once and persist.
	if st, _ := store.LoadState(); st.Token == "" && (cfg.Email == "" || cfg.Password == "") {
		cfg, err = config.PromptCredentials(os.Stdin)
		if err != nil {
			return err
		}
		if err := store.SaveConfig(cfg); err != nil {
			return err
		}
	}

	api := pocketcasts.NewClient()
	auth, err := library.NewStoreAuth(store, api, cfg)
	if err != nil {
		return err
	}

	engine := library.New(api, p, auth)
	defer engine.Close()

	if err := engine.PlayTarget(context.Background(), library.UpNextTop{}); err != nil {
		return err
	}

	prog := tea.NewProgram(mini.New(engine))
	_, err = prog.Run()
	return err
}
