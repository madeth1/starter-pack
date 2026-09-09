package main

import (
	"io"
	"os"
	"testing"
	"time"
)

const (
	keyDown  = "\x1b[B"
	keyEnter = "\r"
)

// drive runs fn with the forms wired to a pipe, feeding keys one at a time.
//
// io.Pipe gives back-pressure - a write blocks until bubbletea's input loop
// reads it - but reading is not processing, and huh advances between groups
// asynchronously. So a settle delay is still needed after each key; feeding
// them all at once makes the second page's Enter land on the first page.
//
// Only the resulting selection is asserted on, never the rendered output:
// bubbletea detects a non-TTY writer and skips drawing altogether, so the
// output buffer holds terminal capability queries and nothing else.
func drive(t *testing.T, keys []string, fn func()) {
	t.Helper()
	pr, pw := io.Pipe()
	testInput, testOutput = pr, io.Discard
	t.Cleanup(func() { testInput, testOutput = nil, nil })

	done := make(chan struct{})
	go func() { fn(); close(done) }()

	for _, k := range keys {
		time.Sleep(settle)
		if _, err := pw.Write([]byte(k)); err != nil {
			break
		}
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("form did not finish: it is waiting on more input than expected")
	}
}

// settle is how long a keystroke is given to be processed before the next one.
// Generous on purpose: this runs in the release pipeline, where a loaded runner
// failing the build over a lost keypress would be far worse than a slow test.
var settle = func() time.Duration {
	if os.Getenv("CI") != "" {
		return 750 * time.Millisecond
	}
	return 250 * time.Millisecond
}()

// Ordered so that the first entry overall is NOT the first entry of the Backend
// category. Any test that lands on litestar therefore proves the second page
// was filtered rather than showing every template.
var picker = []Template{
	{ID: "astro", Name: "Astro", Category: "Frontend"},
	{ID: "litestar", Name: "Litestar", Category: "Backend"},
	{ID: "vue", Name: "Vue", Category: "Frontend"},
}

func TestPickTemplateFiltersByCategory(t *testing.T) {
	var got Template
	var err error
	// Backend (first category), then the first template within it.
	drive(t, []string{keyEnter, keyEnter}, func() {
		got, err = pickTemplate(picker, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unfiltered, the first option would be astro.
	if got.ID != "litestar" {
		t.Errorf("picked %q, want litestar - the Backend page was not filtered", got.ID)
	}
}

func TestPickTemplateSecondCategory(t *testing.T) {
	var got Template
	var err error
	// Down to Frontend, enter; then down one template, enter.
	drive(t, []string{keyDown, keyEnter, keyDown, keyEnter}, func() {
		got, err = pickTemplate(picker, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Filtered, Frontend is [astro vue] so the second entry is vue.
	// Unfiltered it would be [astro litestar vue] and we would land on litestar.
	if got.ID != "vue" {
		t.Errorf("picked %q, want vue - options did not refilter on category change", got.ID)
	}
}

// A single category is not a choice, so that page must be skipped. One Enter
// has to be enough; if a category page were shown, drive would time out.
func TestPickTemplateSkipsSoleCategory(t *testing.T) {
	only := []Template{
		{ID: "litestar", Name: "Litestar", Category: "Backend"},
		{ID: "fastapi", Name: "FastAPI", Category: "Backend"},
	}
	var got Template
	var err error
	drive(t, []string{keyEnter}, func() {
		got, err = pickTemplate(only, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "litestar" {
		t.Errorf("picked %q, want litestar", got.ID)
	}
}

// Naming a template on the command line bypasses the TUI altogether.
func TestPickTemplateFromArgs(t *testing.T) {
	got, err := pickTemplate(picker, []string{"vue"}, true)
	if err != nil || got.ID != "vue" {
		t.Fatalf("got %q, %v", got.ID, err)
	}
	if _, err := pickTemplate(picker, nil, true); err == nil {
		t.Error("--yes with no template should error, not prompt")
	}
}

// Variables are asked one per step. huh renders a group as a page, so the
// number of groups is the number of screens - asserted structurally, because
// keystrokes cannot tell the layouts apart: Enter advances both between fields
// on a page and between pages.
func TestVarsAreOneStepEach(t *testing.T) {
	need := []Var{
		{Key: "project_name", Default: "myapp"},
		{Key: "db", Prompt: "Database", Choices: []string{"sqlite", "postgres"}},
		{Key: "redis", Prompt: "Redis", Choices: []string{"no", "yes"}},
	}
	groups, answers := varGroups(need, varFlag{})
	if len(groups) != 3 {
		t.Errorf("got %d groups for 3 variables; every variable needs its own page", len(groups))
	}
	if len(answers) != 3 {
		t.Errorf("got %d answer bindings, want 3", len(answers))
	}
	// A select with no default starts on its first choice, not empty.
	if *answers["db"] != "sqlite" {
		t.Errorf("db default = %q, want sqlite", *answers["db"])
	}

	// Anything already given with --var is not asked at all.
	groups, answers = varGroups(need, varFlag{"db": "postgres", "project_name": "x"})
	if len(groups) != 1 {
		t.Errorf("got %d groups, want 1 - only redis is unanswered", len(groups))
	}
	if _, asked := answers["db"]; asked {
		t.Error("db was already supplied and must not be asked")
	}
}

// End to end through the real form: the answers land in vars.
func TestCollectVarsSkipsAnswered(t *testing.T) {
	tmpl := Template{Vars: []Var{
		{Key: "db", Choices: []string{"sqlite", "postgres"}},
		{Key: "redis", Choices: []string{"no", "yes"}},
	}}
	vars := varFlag{"project_name": "demo", "db": "postgres"}

	// Only redis should be asked: one Enter must be enough to finish.
	var err error
	drive(t, []string{keyEnter}, func() {
		err = collectVars(tmpl, vars, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if vars["db"] != "postgres" || vars["redis"] != "no" {
		t.Errorf("got db=%q redis=%q", vars["db"], vars["redis"])
	}
}
