package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The overlay must render *.tmpl, leave {{ }} in ordinary files alone (Vue and
// Astro sources depend on that), and substitute __key__ inside paths.
func TestRender(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	vue := "<template><p>{{ msg }}</p></template>\n"

	write(t, filepath.Join(src, "README.md.tmpl"), "# {{.project_name}}\n")
	write(t, filepath.Join(src, "App.vue"), vue)
	write(t, filepath.Join(src, "src/__project_name__/main.py"), "x = 1\n")

	vars := map[string]string{"project_name": "demo"}
	if err := render(src, dst, vars); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(dst, "README.md")); got != "# demo\n" {
		t.Errorf("rendered README = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md.tmpl")); err == nil {
		t.Error(".tmpl suffix was not stripped")
	}
	if got := read(t, filepath.Join(dst, "App.vue")); got != vue {
		t.Errorf("App.vue was mangled: %q", got)
	}
	if got := read(t, filepath.Join(dst, "src/demo/main.py")); got != "x = 1\n" {
		t.Errorf("path substitution failed: %q", got)
	}
}

// A typo in a template must fail loudly rather than writing "<no value>".
func TestRenderUnknownVar(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "x.txt.tmpl"), "{{.nope}}")
	if err := render(src, dst, map[string]string{"project_name": "demo"}); err == nil {
		t.Fatal("expected an error for an undeclared variable")
	}
}

func TestInstallCmd(t *testing.T) {
	spec := toolSpec{"apt": "nodejs npm", "brew": "node"}
	scriptOnly := toolSpec{"script": "curl -LsSf https://astral.sh/uv/install.sh | sh"}

	tests := []struct {
		name, mgrKey, mgrInstall string
		spec                     toolSpec
		haveMgr                  bool
		want                     string
		wantErr                  bool
	}{
		{"node", "apt", "sudo apt-get install -y %s", spec, true, "sudo apt-get install -y nodejs npm", false},
		{"node", "brew", "brew install %s", spec, true, "brew install node", false},
		// manager present but no package listed -> fall back to the script
		{"uv", "apt", "sudo apt-get install -y %s", scriptOnly, true, scriptOnly["script"], false},
		// no manager at all -> still fine when a script exists
		{"uv", "", "", scriptOnly, false, scriptOnly["script"], false},
		// nothing usable -> error, never a guess
		{"node", "pacman", "sudo pacman -S %s", spec, true, "", true},
	}
	for _, tt := range tests {
		got, err := installCmd(tt.name, tt.spec, tt.mgrKey, tt.mgrInstall, tt.haveMgr)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s/%s: expected error, got %q", tt.name, tt.mgrKey, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s/%s: %v", tt.name, tt.mgrKey, err)
		} else if got != tt.want {
			t.Errorf("%s/%s = %q, want %q", tt.name, tt.mgrKey, got, tt.want)
		}
	}
}

func TestVarFlag(t *testing.T) {
	v := varFlag{}
	if err := v.Set("db=postgres"); err != nil || v["db"] != "postgres" {
		t.Errorf("Set(db=postgres) = %v, %v", v, err)
	}
	if err := v.Set("nope"); err == nil {
		t.Error("expected an error for a value without =")
	}
	// values may legitimately contain '='
	_ = v.Set("url=a=b")
	if v["url"] != "a=b" {
		t.Errorf("url = %q", v["url"])
	}
}

func TestLoadTemplates(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "litestar/template.toml"), "name = \"Litestar\"\nrequires = [\"uv\"]\nsteps = [\"uv sync\"]\n")
	write(t, filepath.Join(dir, "notatemplate/readme.md"), "ignore me")

	ts, err := loadTemplates([]catalog{{Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].ID != "litestar" || ts[0].Name != "Litestar" {
		t.Fatalf("got %+v", ts)
	}
	if _, err := findTemplate(ts, "nuxt"); err == nil || !strings.Contains(err.Error(), "litestar") {
		t.Errorf("findTemplate should list what is available, got %v", err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCategories(t *testing.T) {
	ts := []Template{
		{ID: "vue", Category: "Frontend"},
		{ID: "misc", Category: "Other"},
		{ID: "litestar", Category: "Backend"},
		{ID: "astro", Category: "Frontend"},
	}
	got := categories(ts)
	want := []string{"Backend", "Frontend", "Other"} // "Other" always last
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("categories = %v, want %v", got, want)
	}
	if in := inCategory(ts, "Frontend"); len(in) != 2 || in[0].ID != "vue" || in[1].ID != "astro" {
		t.Errorf("inCategory(Frontend) = %v", in)
	}
	if in := inCategory(ts, "nope"); len(in) != 0 {
		t.Errorf("inCategory(nope) = %v", in)
	}
}

// An untagged template must still show up, not vanish from the picker.
func TestCategoryDefaultsToOther(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bare/template.toml"), "name = \"Bare\"\n")
	ts, err := loadTemplates([]catalog{{Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if ts[0].Category != "Other" {
		t.Errorf("category = %q, want Other", ts[0].Category)
	}
}

// A later catalog wins on a clashing id, so a private template can deliberately
// shadow one from the shared repo - and the untouched ones still come through.
func TestLoadTemplatesPrecedence(t *testing.T) {
	shared, private := t.TempDir(), t.TempDir()
	write(t, filepath.Join(shared, "litestar/template.toml"), "name = \"Shared Litestar\"\n")
	write(t, filepath.Join(shared, "astro/template.toml"), "name = \"Astro\"\n")
	write(t, filepath.Join(private, "litestar/template.toml"), "name = \"My Litestar\"\n")
	write(t, filepath.Join(private, "internal/template.toml"), "name = \"Internal\"\n")

	ts, err := loadTemplates([]catalog{{Dir: shared}, {Dir: private, Label: "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Template{}
	for _, x := range ts {
		got[x.ID] = x
	}
	if len(ts) != 3 {
		t.Fatalf("want 3 templates, got %d: %v", len(ts), got)
	}
	if got["litestar"].Name != "My Litestar" || got["litestar"].Source != "acme" {
		t.Errorf("private template did not win: %+v", got["litestar"])
	}
	if got["astro"].Name != "Astro" || got["astro"].Source != "" {
		t.Errorf("shared template was disturbed: %+v", got["astro"])
	}
	if got["internal"].Source != "acme" {
		t.Errorf("internal source = %q", got["internal"].Source)
	}
}

// A source that is missing entirely must be skipped, not fatal.
func TestLoadTemplatesSkipsMissingDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x/template.toml"), "name = \"X\"\n")
	ts, err := loadTemplates([]catalog{{Dir: dir}, {Dir: filepath.Join(dir, "nope")}})
	if err != nil || len(ts) != 1 {
		t.Fatalf("got %d templates, %v", len(ts), err)
	}
	if _, err := loadTemplates([]catalog{{Dir: filepath.Join(dir, "nope")}}); err == nil {
		t.Error("no templates anywhere should be an error")
	}
}

func TestSlugAndLabel(t *testing.T) {
	// URLs that flatten to the same text must not share a cache directory.
	a, b := slug("https://github.com/a/b-c"), slug("https://github.com/a-b/c")
	if a == b {
		t.Errorf("slug collision: %s", a)
	}
	// Same URL, same directory, every run.
	if slug("git@github.com:acme/t.git") != slug("git@github.com:acme/t.git") {
		t.Error("slug is not stable")
	}
	for url, want := range map[string]string{
		"git@github.com:acme/internal-templates.git": "internal-templates",
		"https://github.com/madeth/project-starter":  "project-starter",
		"https://github.com/madeth/templates.git":    "templates",
		"/home/me/my-templates":                      "my-templates",
	} {
		if got := label(url); got != want {
			t.Errorf("label(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestSourcesRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got, err := loadSources(); err != nil || len(got) != 0 {
		t.Fatalf("empty config should yield no sources: %v %v", got, err)
	}
	want := []Source{{URL: "git@github.com:acme/private.git"}, {URL: "https://github.com/x/y"}}
	if err := saveSources(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].URL != want[0].URL || got[1].URL != want[1].URL {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestLoadToolsMerges(t *testing.T) {
	shared, private := t.TempDir(), t.TempDir()
	write(t, filepath.Join(shared, "tools.toml"), "[node]\ncheck = \"node --version\"\napt = \"nodejs\"\n")
	write(t, filepath.Join(private, "tools.toml"), "[node]\ncheck = \"node --version\"\napt = \"nodejs-custom\"\n\n[terraform]\ncheck = \"terraform version\"\nbrew = \"terraform\"\n")

	tools, err := loadTools(shared, private)
	if err != nil {
		t.Fatal(err)
	}
	if tools["node"]["apt"] != "nodejs-custom" {
		t.Errorf("later source should win: %v", tools["node"])
	}
	if tools["terraform"]["brew"] != "terraform" {
		t.Error("a private source must be able to declare a new tool")
	}
}

// A .tmpl that renders to nothing must not create an empty file: that is how a
// template makes a file conditional on the answers.
func TestRenderSkipsEmptyResult(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "compose.yaml.tmpl"), "{{if eq .db \"postgres\"}}services: pg{{end}}\n")
	write(t, filepath.Join(src, "keep.txt.tmpl"), "{{.db}}\n")

	if err := render(src, dst, map[string]string{"db": "sqlite"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "compose.yaml")); !os.IsNotExist(err) {
		t.Error("empty render should not have created compose.yaml")
	}
	if got := read(t, filepath.Join(dst, "keep.txt")); got != "sqlite\n" {
		t.Errorf("keep.txt = %q", got)
	}

	dst2 := t.TempDir()
	if err := render(src, dst2, map[string]string{"db": "postgres"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dst2, "compose.yaml")); got != "services: pg\n" {
		t.Errorf("compose.yaml = %q", got)
	}
}

// materialize must work on every git that supports sparse checkout, and must
// never write a flag into the pattern list. `sparse-checkout add --no-cone`
// did both wrong: rejected outright by macOS git, silently stored as a literal
// pattern by newer git.
func TestMaterialize(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// A catalog with two templates.
	origin := t.TempDir()
	for _, id := range []string{"alpha", "beta"} {
		write(t, filepath.Join(origin, id, "template.toml"), "name = \""+id+"\"\n")
		write(t, filepath.Join(origin, id, "files", id+".txt"), id)
	}
	write(t, filepath.Join(origin, "tools.toml"), "[git]\ncheck = \"git --version\"\n")
	git(origin, "init", "-q")
	git(origin, "add", "-A")
	git(origin, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")

	// Clone it the way syncRepo does: manifests only.
	clone := filepath.Join(t.TempDir(), "repo")
	out, err := exec.Command("git", "clone", "-q", "--sparse", origin, clone).CombinedOutput()
	if err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	git(clone, "sparse-checkout", "set", "--no-cone", "/tools.toml", "/*/template.toml")

	exists := func(p string) bool { _, err := os.Stat(filepath.Join(clone, p)); return err == nil }
	if exists("alpha/files/alpha.txt") {
		t.Fatal("manifest-only checkout should not include files/")
	}

	if err := materialize(Template{ID: "alpha", Root: clone}); err != nil {
		t.Fatal(err)
	}
	if !exists("alpha/files/alpha.txt") {
		t.Error("alpha files were not checked out")
	}
	if exists("beta/files/beta.txt") {
		t.Error("beta was fetched despite not being asked for")
	}

	// A second template must not evict the first.
	if err := materialize(Template{ID: "beta", Root: clone}); err != nil {
		t.Fatal(err)
	}
	if !exists("alpha/files/alpha.txt") || !exists("beta/files/beta.txt") {
		t.Error("materializing beta disturbed alpha")
	}

	// The pattern list must contain patterns, never flags.
	list, err := exec.Command("git", "-C", clone, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			t.Errorf("a flag leaked into the sparse pattern list: %q", line)
		}
	}

	// Re-materializing is a no-op, not an error.
	if err := materialize(Template{ID: "alpha", Root: clone}); err != nil {
		t.Errorf("second materialize of alpha: %v", err)
	}
	// A non-sparse checkout is simply left alone.
	if err := materialize(Template{ID: "alpha", Root: origin}); err != nil {
		t.Errorf("non-sparse repo should be a no-op: %v", err)
	}
}
