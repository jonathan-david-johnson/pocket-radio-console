// Command pocket-radio is the terminal companion to the PocketRadio menubar app.
//
// Usage:
//
//	pocket-radio              full TUI (M4 — placeholder for now)
//	pocket-radio up_next      mini mode: play the top of Up Next
//	pocket-radio kcrw         mini mode: resolve + play a station
//	pocket-radio --list       print resolvable names and exit
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pocket-radio-console/internal/applog"
	"pocket-radio-console/internal/config"
	"pocket-radio-console/internal/library"
	"pocket-radio-console/internal/player"
	"pocket-radio-console/internal/pocketcasts"
	"pocket-radio-console/internal/radio"
	"pocket-radio-console/internal/resolver"
	"pocket-radio-console/internal/ui/full"
	"pocket-radio-console/internal/ui/mini"
)

func main() {
	var (
		list      bool
		full      bool
		debugFile string
		posArgs   []string
	)
	for _, a := range os.Args[1:] {
		switch {
		case a == "--list":
			list = true
		case a == "--full":
			full = true
		case a == "--debug":
			debugFile = "debug.log"
		case strings.HasPrefix(a, "--debug="):
			debugFile = strings.TrimPrefix(a, "--debug=")
		default:
			posArgs = append(posArgs, a)
		}
	}

	closer, err := applog.Init(debugFile, slog.LevelDebug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error opening debug log:", err)
		os.Exit(1)
	}
	defer closer.Close()

	if list {
		if err := runList(); err != nil {
			fail(err)
		}
		return
	}
	if full || len(posArgs) == 0 {
		if err := runFull(); err != nil {
			fail(err)
		}
		return
	}
	if err := runMini(posArgs[0]); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// deps bundles the wired boundary objects.
type deps struct {
	store *config.Store
	api   *pocketcasts.Client
	radio *radio.Client
	auth  *library.StoreAuth
}

// setup loads config, ensures a token (prompting/logging in if needed), and
// wires the API + radio clients + auth.
func setup(ctx context.Context) (*deps, error) {
	dir, err := config.DefaultDir()
	if err != nil {
		return nil, err
	}
	store, err := config.NewStore(dir)
	if err != nil {
		return nil, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, err
	}

	st, _ := store.LoadState()
	if st.Token == "" && (cfg.Email == "" || cfg.Password == "") {
		cfg, err = config.PromptCredentials(os.Stdin)
		if err != nil {
			return nil, err
		}
		if err := store.SaveConfig(cfg); err != nil {
			return nil, err
		}
	}

	api := pocketcasts.NewClient()
	auth, err := library.NewStoreAuth(store, api, cfg)
	if err != nil {
		return nil, err
	}
	// Ensure we have a token + user UUID up front (radio favorites need the UUID).
	if st.Token == "" {
		if _, err := auth.Relogin(ctx); err != nil {
			return nil, err
		}
	}
	return &deps{store: store, api: api, radio: radio.NewClient(), auth: auth}, nil
}

// runList prints reserved words + favorite station names.
func runList() error {
	ctx := context.Background()
	d, err := setup(ctx)
	if err != nil {
		return err
	}
	fmt.Println("Reserved words:")
	for _, w := range []string{"up_next", "new"} {
		fmt.Println("  " + w)
	}
	favs, err := d.radio.Favorites(ctx, d.auth.UserUUID())
	if err != nil {
		return err
	}
	fmt.Println("Favorites:")
	names := make([]string, 0, len(favs))
	for _, f := range favs {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println("  " + n)
	}
	return nil
}

// runFull opens the full alt-screen TUI without auto-starting playback.
func runFull() error {
	slog.Info("mode: full TUI")
	ctx := context.Background()
	d, err := setup(ctx)
	if err != nil {
		slog.Error("setup failed", "err", err)
		return err
	}
	return launchFull(ctx, d, nil)
}

// launchFull starts the full TUI, optionally reusing an existing engine.
func launchFull(ctx context.Context, d *deps, eng *library.Engine) error {
	favs, err := d.radio.Favorites(ctx, d.auth.UserUUID())
	if err != nil {
		slog.Warn("favorites fetch failed", "err", err)
	}
	slog.Info("favorites loaded", "count", len(favs))
	streams := favs
	if len(streams) > 3 {
		streams = streams[:3]
	}
	if eng == nil {
		p, err := player.New()
		if err != nil {
			return err
		}
		eng = library.New(d.api, p, d.auth, library.WithTracklister(d.radio))
		defer eng.Close()
	}
	model := full.New(eng, streams)
	_, runErr := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	).Run()
	return runErr
}

// runMini resolves arg and plays the result in mini mode.
// If the user presses 'f', the full TUI is launched with the same engine.
func runMini(arg string) error {
	slog.Info("mode: mini", "arg", arg)
	ctx := context.Background()
	d, err := setup(ctx)
	if err != nil {
		slog.Error("setup failed", "err", err)
		return err
	}

	target, err := resolveTarget(ctx, arg, d)
	if err != nil {
		return err
	}

	p, err := player.New()
	if err != nil {
		return err
	}
	engine := library.New(d.api, p, d.auth, library.WithTracklister(d.radio))
	defer engine.Close()

	if err := engine.PlayTarget(ctx, target); err != nil {
		return err
	}

	m, err := tea.NewProgram(mini.New(engine)).Run()
	if err != nil {
		return err
	}
	if mm, ok := m.(mini.Model); ok && mm.WantFull {
		return launchFull(ctx, d, engine)
	}
	return nil
}

// resolveTarget maps a mini-mode argument to a library.Target.
func resolveTarget(ctx context.Context, arg string, d *deps) (library.Target, error) {
	// Reserved words resolve without consulting favorites or the network.
	var favs []radio.Station
	if _, reserved := resolver.ReservedWords[arg]; !reserved {
		var err error
		favs, err = d.radio.Favorites(ctx, d.auth.UserUUID())
		if err != nil {
			return nil, err
		}
	}

	res, err := resolver.Resolve(ctx, arg, favs, d.radio.Search)
	if err != nil {
		return nil, err
	}
	switch res.Kind {
	case resolver.PlayUpNextTop:
		return library.UpNextTop{}, nil
	case resolver.PlayNewest:
		return library.NewestRelease{}, nil
	case resolver.PlayStation:
		return library.PlayStation{Station: res.Station}, nil
	default:
		return nil, fmt.Errorf("unhandled resolution")
	}
}
