// Command gl-repos shows the status of every git repository below a
// directory and lets you fetch, pull and push them interactively.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/ai"
	"gl-repos/internal/config"
	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
	"gl-repos/internal/safe"
	"gl-repos/internal/scan"
	"gl-repos/internal/tui"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

// listTimeout bounds each repository's status read in -list mode, so a
// hung git (e.g. a dead network file system) cannot stall the table.
const listTimeout = 30 * time.Second

func main() {
	var (
		cfgPath     = flag.String("config", config.Path(), "config file (TOML)")
		depth       = flag.Int("depth", 1, "how many directory levels below dir to search (0 = dir only)")
		jobs        = flag.Int("jobs", runtime.NumCPU(), "maximum concurrent git processes")
		fetch       = flag.Bool("fetch", false, "fetch all repositories on startup")
		sortBy      = flag.String("sort", "name", "initial sort: name, attention or recent")
		tool        = flag.String("tool", "lazygit", "git UI opened with 'o' in the selected repo")
		model       = flag.String("model", "", "Ollama model for the AI assistant (default from config: "+config.Default().AI.Model+")")
		profile     = flag.String("profile", "", "profile active at start for clone/new/publish (default: first in config)")
		list        = flag.Bool("list", false, "print a status table and exit (non-interactive)")
		initCfg     = flag.Bool("init", false, "write a commented config file to the path in use (-config, GL_REPOS_CONFIG or the default) and exit")
		doctorRun   = flag.Bool("doctor", false, "check the setup (config, git, tools, glab/gh logins and scopes, ssh, Ollama) without changing anything, and exit")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.IntVar(depth, "d", 1, "shorthand for -depth")
	flag.IntVar(jobs, "j", runtime.NumCPU(), "shorthand for -jobs")
	flag.Usage = func() { usage(flag.CommandLine.Output(), *cfgPath) }
	flag.Parse()

	if *showVersion {
		fmt.Println("gl-repos", version)
		return
	}
	warnLegacyEnv(os.Stderr)
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}

	// A config the user named explicitly must exist: a typo in -config
	// or GL_REPOS_CONFIG would otherwise silently run with the defaults.
	_, source := config.EnvPath()
	fetchSet := false // -fetch on the command line (-list says it is ignored)
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config":
			source = "-config"
		case "fetch":
			fetchSet = true
		}
	})
	// The settings and cache of gitgaze (the old name) move once;
	// -doctor changes nothing and only reports them.
	if !*doctorRun {
		migrate(os.Stderr, source != "")
	}
	// Explicit flags win over the config file.
	applyFlags := func(cfg *config.Config) {
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "depth", "d":
				cfg.Depth = *depth
			case "jobs", "j":
				cfg.Jobs = *jobs
			case "fetch":
				cfg.FetchOnStart = *fetch
			case "sort":
				cfg.Sort = *sortBy
			case "tool":
				cfg.Tool = *tool
			case "model":
				cfg.AI.Model = *model
			case "profile":
				cfg.DefaultProfile = *profile
			}
		})
	}
	if *initCfg {
		if err := writeConfig(*cfgPath, configExample); err != nil {
			fatal(err)
		}
		fmt.Print(initDone(*cfgPath))
		return
	}
	if *doctorRun {
		os.Exit(runDoctor(*cfgPath, source, applyFlags))
	}
	if err := checkExplicitConfig(*cfgPath, source); err != nil {
		fatal(err)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(fmt.Errorf("config: %w", err))
	}
	applyFlags(&cfg)
	if err := cfg.Validate(); err != nil { // the flags may have broken it
		fatal(fmt.Errorf("config: %w", err))
	}
	if cfg.Jobs <= 0 { // 0 in the config or -j 0
		cfg.Jobs = runtime.NumCPU()
	}
	profiles, start, err := buildProfiles(cfg, exec.LookPath)
	if err != nil {
		fatal(fmt.Errorf("config: %w", err))
	}

	root := "."
	if flag.NArg() == 1 {
		root = flag.Arg(0)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fatal(err)
	}
	repos, err := scan.Find(root, cfg.Depth, cfg.Skip...)
	if err != nil {
		fatal(err)
	}
	if *list {
		if len(repos) == 0 {
			fatal(fmt.Errorf("no git repositories found in %s (depth %d)", root, cfg.Depth))
		}
		printList(os.Stdout, os.Stderr, root, repos, cfg.Jobs, fetchSet)
		return
	}
	// The TUI also starts in an empty folder so repositories can be cloned.

	m := tui.New(tui.Options{
		Root:         root,
		Depth:        cfg.Depth,
		Repos:        repos,
		FetchOnStart: cfg.FetchOnStart,
		Jobs:         cfg.Jobs,
		Sort:         cfg.Sort,
		Tool:         cfg.Tool,
		Agents:       cfg.Agents,
		AI: ai.Client{
			URL:          cfg.AI.URL,
			Model:        cfg.AI.Model,
			MaxCtx:       cfg.AI.MaxCtx,
			Timeout:      seconds(cfg.AI.TimeoutSeconds),
			LoadTimeout:  seconds(cfg.AI.LoadTimeoutSeconds),
			StallTimeout: seconds(cfg.AI.StallTimeoutSeconds),
			KeepAlive:    cfg.AI.KeepAlive,
			MemoryGuard:  cfg.AI.MemoryGuard,
		},
		MaxDiff:  cfg.AI.MaxDiffBytes,
		Profiles: profiles,
		Profile:  start,
	})
	tui.DetectBackground()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fatal(err)
	}
}

// usage prints -h: the config path comes from -config or the
// environment and is printed like every path in messages (sanitized,
// URL credentials hidden), as are the flag defaults, which name it too.
func usage(w io.Writer, cfgPath string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: gl-repos [flags] [dir]\n\n")
	fmt.Fprintf(&b, "Shows every git repository below dir (default: current directory).\n")
	fmt.Fprintf(&b, "Settings are read from %s; flags override them.\n", cfgPath)
	fmt.Fprintf(&b, "First run: gl-repos -init, edit the file, then gl-repos -doctor.\n\nFlags:\n")
	out := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(&b)
	flag.PrintDefaults()
	flag.CommandLine.SetOutput(out)
	for line := range strings.Lines(b.String()) {
		fmt.Fprintln(w, forge.RedactLine(strings.TrimSuffix(line, "\n")))
	}
}

// checkExplicitConfig reports a config file named by source (-config,
// GL_REPOS_CONFIG or the deprecated GITGAZE_CONFIG) that cannot be
// read; "" means the default location, which may be missing.
func checkExplicitConfig(path, source string) error {
	if source == "" {
		return nil
	}
	_, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("config: %s names %s, which does not exist", source, path)
	case err != nil:
		return fmt.Errorf("config: %s names %s: %w", source, path, err)
	}
	return nil
}

// buildProfiles turns the configured profiles into forges and finds the
// one active at start (cfg.Validate has checked that it exists). Without
// [[profile]] or default_profile, the first built-in profile whose CLI is
// installed starts (lookPath only: nothing slow before the TUI).
func buildProfiles(cfg config.Config, lookPath func(string) (string, error)) ([]tui.Profile, int, error) {
	var out []tui.Profile
	start, found := 0, false
	for _, p := range cfg.EffectiveProfiles() {
		f, err := profileForge(cfg, p)
		if err != nil {
			return nil, 0, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		if p.Name == cfg.DefaultProfile {
			start, found = len(out), true
		}
		out = append(out, tui.Profile{Name: p.Name, Color: p.Color, Forge: f, DefaultGroup: p.DefaultGroup,
			ExcludeKey: cfg.Key(p, "exclude")})
	}
	if !found && cfg.Implicit() {
		for i, p := range out {
			if _, err := lookPath(p.Forge.CLI()); err == nil {
				start = i
				break
			}
		}
	}
	return out, start, nil
}

// profileForge builds the forge of profile p, naming p's host setting in
// its messages.
func profileForge(cfg config.Config, p config.Profile) (forge.Forge, error) {
	f, err := forge.New(p.Forge, p.Host, p.Exclude)
	if g, ok := f.(forge.GitLab); ok {
		g.HostKey = cfg.Key(p, "host")
		f = g
	}
	return f, err
}

// printList reads every repository's status (jobs at a time) and prints
// the same columns as the TUI as a tab-separated table to out, warnings
// to errOut. It never fetches: fetch says -fetch was given, which is
// noted on errOut. Directory names are text from anyone: sanitized like
// the TUI. Branch and upstream names are ref names from anyone who
// pushed and may hold a credential shape: redacted like the TUI
// (scrubStatus).
func printList(out, errOut io.Writer, root string, repos []string, jobs int, fetch bool) {
	if fetch {
		fmt.Fprintln(errOut, "gl-repos: -list never fetches; -fetch ignored (ahead/behind are as of the last fetch)")
	}
	statuses := make([]gitx.Status, len(repos))
	todo := make(chan int)
	var wg sync.WaitGroup
	for range min(max(jobs, 1), len(repos)) { // a pool of jobs workers, not a goroutine per repository
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range todo {
				ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
				statuses[i] = gitx.ReadStatus(ctx, repos[i])
				cancel()
			}
		}()
	}
	for i := range repos {
		todo <- i
	}
	close(todo)
	wg.Wait()

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tBRANCH\tSYNC\tVS BASE\tSTATE\tUPSTREAM")
	for _, s := range statuses {
		rel, err := filepath.Rel(root, s.Path)
		if err != nil {
			rel = s.Path
		}
		base := s.BaseText()
		if base == "" {
			base = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", safe.Line(rel), forge.RedactLine(s.BranchText()), s.SyncText(), base, s.StateText(),
			forge.RedactLine(s.UpstreamText()))
	}
	w.Flush()
	for _, s := range statuses {
		if len(s.Risky) > 0 {
			rel, err := filepath.Rel(root, s.Path)
			if err != nil {
				rel = s.Path
			}
			fmt.Fprintf(errOut, "gl-repos: %s: its own git config runs programs (%s); gl-repos did not run them\n",
				safe.Line(rel), forge.RedactLine(strings.Join(s.Risky, ", ")))
		}
	}
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// fatal prints err (sanitized, URL credentials hidden: it may quote a
// config value or git's output) and exits with status 1.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gl-repos:", forge.RedactLine(err.Error()))
	os.Exit(1)
}
