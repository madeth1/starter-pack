package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const defaultTemplatesRepo = "https://github.com/madeth1/starter-pack-templates"

// Var is one value the template needs from the user. Choices turns the prompt
// into a select instead of a free-text input.
type Var struct {
	Key     string   `toml:"key"`
	Prompt  string   `toml:"prompt"`
	Default string   `toml:"default"`
	Choices []string `toml:"choices"`
}

// Template is one directory of a templates repo, described by its
// template.toml. PreSteps run before the files/ overlay is copied (that is how
// we delegate to `npm create ...`), Steps run after it, in the project dir.
type Template struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Category    string   `toml:"category"`
	Requires    []string `toml:"requires"`
	Vars        []Var    `toml:"vars"`
	PreSteps    []string `toml:"pre_steps"`
	Steps       []string `toml:"steps"`
	Next        string   `toml:"next"` // printed on success, e.g. "uv run litestar run"

	ID     string `toml:"-"`
	Dir    string `toml:"-"`
	Root   string `toml:"-"` // the catalog checkout this template came from
	Source string `toml:"-"` // "" for the shared catalog, else the source's label
}

// Source is an extra templates repo the user has added. Templates only ever
// come from git, so this is always a clonable URL - including a path to a local
// git repo, which git clones like any other remote.
type Source struct {
	URL string `toml:"url"`
}

type sourcesFile struct {
	Source []Source `toml:"source"`
}

// catalog is one resolved templates directory, in precedence order.
type catalog struct {
	Dir   string
	Label string
}

func configDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "project-starter"), nil
}

func sourcesPath() (string, error) {
	cfg, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "sources.toml"), nil
}

func loadSources() ([]Source, error) {
	path, err := sourcesPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	var f sourcesFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Source, nil
}

func saveSources(srcs []Source) error {
	path, err := sourcesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Extra template repositories, lowest priority first.\n")
	b.WriteString("# Private repos work with your normal git credentials - an SSH remote\n")
	b.WriteString("# (git@github.com:you/repo.git), or `gh auth setup-git` for HTTPS.\n")
	for _, s := range srcs {
		fmt.Fprintf(&b, "\n[[source]]\nurl = %q\n", s.URL)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

var slugUnsafe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// slug names a cache directory after a repo URL. The hash suffix keeps two URLs
// that flatten to the same text (a/b-c and a-b/c) in separate directories.
func slug(url string) string {
	sum := sha256.Sum256([]byte(url))
	name := strings.Trim(slugUnsafe.ReplaceAllString(strings.TrimSuffix(url, ".git"), "-"), "-")
	if len(name) > 60 {
		name = name[len(name)-60:]
	}
	return name + "-" + hex.EncodeToString(sum[:4])
}

// label is the short name shown beside a template from a non-default source.
func label(url string) string {
	base := filepath.Base(strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git"))
	if i := strings.LastIndex(base, ":"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// syncRepo clones or updates a git source into the cache and returns its path.
//
// Authentication is git's own: an SSH remote uses the user's key, HTTPS uses
// their credential helper. We add none of our own. allowPrompt is false under
// --yes so a private repo with no configured credentials fails fast instead of
// blocking on a username prompt.
func syncRepo(url string, allowPrompt bool) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is required to fetch templates but was not found in PATH")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "project-starter", slug(url))

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Env = os.Environ()
		if !allowPrompt {
			cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
		return cmd.CombinedOutput()
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// Cold cache: without a successful clone there is nothing to fall back on.
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		// Fetch the manifests, not the templates: a catalog can be large and
		// you only ever use one template per run. Blobs are fetched lazily and
		// the working tree is narrowed to the *.toml files the picker needs;
		// the chosen template's files/ is checked out later by materialize.
		out, err := run("clone", "--depth", "1", "--filter=blob:none", "--sparse", url, dir)
		if err != nil {
			// Older git, or a server with no partial-clone support.
			if out, err = run("clone", "--depth", "1", url, dir); err != nil {
				os.RemoveAll(dir) // don't leave a half-clone that looks warm next run
				return "", fmt.Errorf("cloning %s: %w\n%s\nIf this repo is private, check `ssh -T git@github.com` or run `gh auth setup-git`", url, err, strings.TrimSpace(string(out)))
			}
			return dir, nil
		}
		if out, err := run("-C", dir, "sparse-checkout", "set", "--no-cone", "/tools.toml", "/*/template.toml"); err != nil {
			// Not fatal: without it we simply have the whole checkout.
			fmt.Fprintf(os.Stderr, "warning: sparse checkout unavailable for %s (%v)\n%s\n", url, err, strings.TrimSpace(string(out)))
		}
		return dir, nil
	}
	// Warm cache: a failed update is survivable, so carry on with what we have.
	if out, err := run("-C", dir, "pull", "--ff-only", "--quiet"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update %s (%v), using cached copy\n%s\n", url, err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// resolveCatalogs returns every templates directory in precedence order: the
// shared catalog, then each configured source, then the user's private
// directory. Later entries win on a clashing template id.
//
// Failures are returned rather than raised: one unreachable private repo must
// not stop you scaffolding from the sources that did resolve.
func resolveCatalogs(allowPrompt bool) (cats []catalog, warns []error) {
	def := os.Getenv("PROJECT_STARTER_TEMPLATES")
	if def == "" {
		def = defaultTemplatesRepo
	}
	sources := []Source{{URL: def}}
	extra, err := loadSources()
	if err != nil {
		warns = append(warns, err)
	}
	sources = append(sources, extra...)

	for i, src := range sources {
		if src.URL == "" {
			continue
		}
		dir, err := syncRepo(src.URL, allowPrompt)
		if err != nil {
			warns = append(warns, err)
			continue
		}
		lbl := "" // the first source is the default catalog; it needs no label
		if i > 0 {
			lbl = label(src.URL)
		}
		cats = append(cats, catalog{Dir: dir, Label: lbl})
	}
	return cats, warns
}

// loadTemplates merges several catalogs. Later ones win on a clashing id, so a
// personal template can deliberately shadow a shared one. A directory that does
// not exist is simply skipped.
func loadTemplates(cats []catalog) ([]Template, error) {
	byID := map[string]Template{}
	for _, c := range cats {
		ts, err := loadDir(c)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			byID[t.ID] = t
		}
	}
	if len(byID) == 0 {
		return nil, fmt.Errorf("no templates found")
	}
	out := make([]Template, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// loadDir reads every subdirectory of a catalog that holds a template.toml.
func loadDir(c catalog) ([]Template, error) {
	entries, err := os.ReadDir(c.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		manifest := filepath.Join(c.Dir, e.Name(), "template.toml")
		if _, err := os.Stat(manifest); err != nil {
			continue
		}
		var t Template
		md, err := toml.DecodeFile(manifest, &t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", manifest, err)
		}
		// Catches the TOML footgun where a top-level key written after a
		// [[vars]] header silently becomes a field of that table instead.
		if un := md.Undecoded(); len(un) > 0 {
			var keys []string
			for _, k := range un {
				keys = append(keys, k.String())
			}
			return nil, fmt.Errorf("%s: unrecognised key(s): %s\n(top-level keys must appear before the first [[vars]] table)", manifest, strings.Join(keys, ", "))
		}
		t.ID = e.Name()
		t.Dir = filepath.Join(c.Dir, e.Name())
		t.Root = c.Dir
		t.Source = c.Label
		if t.Name == "" {
			t.Name = t.ID
		}
		if t.Category == "" {
			t.Category = "Other"
		}
		out = append(out, t)
	}
	return out, nil
}

func findTemplate(ts []Template, id string) (Template, error) {
	for _, t := range ts {
		if t.ID == id {
			return t, nil
		}
	}
	var names []string
	for _, t := range ts {
		names = append(names, t.ID)
	}
	return Template{}, fmt.Errorf("unknown template %q (have: %s)", id, strings.Join(names, ", "))
}

// categories returns the distinct categories present, in display order. "Other"
// sorts last so an uncategorised template never leads the menu.
func categories(ts []Template) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t.Category] {
			seen[t.Category] = true
			out = append(out, t.Category)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i] == "Other") != (out[j] == "Other") {
			return out[j] == "Other"
		}
		return out[i] < out[j]
	})
	return out
}

func inCategory(ts []Template, cat string) []Template {
	var out []Template
	for _, t := range ts {
		if t.Category == cat {
			out = append(out, t)
		}
	}
	return out
}

// materialize checks out the chosen template's files, which a sparse catalog
// has deliberately left out. A no-op for a catalog that is not sparse.
//
// It re-runs `sparse-checkout set` with the union of the patterns rather than
// `add`: `add` rejects --no-cone on older git (macOS), and on newer git accepts
// it as a literal pattern, which is worse. `set --no-cone` behaves the same
// everywhere.
func materialize(t Template) error {
	if t.Root == "" {
		return nil
	}
	sparse, err := exec.Command("git", "-C", t.Root, "config", "--get", "core.sparseCheckout").Output()
	if err != nil || strings.TrimSpace(string(sparse)) != "true" {
		return nil
	}

	want := "/" + t.ID + "/"
	out, err := exec.Command("git", "-C", t.Root, "sparse-checkout", "list").Output()
	if err != nil {
		return fmt.Errorf("reading sparse patterns for %s: %w", t.ID, err)
	}
	patterns := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// Skip junk a previous version may have written into the cache, and
		// anything git would read as a flag.
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		if line == want {
			return nil // already checked out
		}
		patterns = append(patterns, line)
	}
	patterns = append(patterns, want)

	args := append([]string{"-C", t.Root, "sparse-checkout", "set", "--no-cone"}, patterns...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("fetching template %s: %w\n%s", t.ID, err, strings.TrimSpace(string(out)))
	}
	return nil
}
