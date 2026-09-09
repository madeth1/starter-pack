package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	huh "charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
)

// Set by GoReleaser at build time via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// testInput/testOutput, when set, drive the forms headlessly from tests. The
// TUI is otherwise the one part no test can reach.
var (
	testInput  io.Reader
	testOutput io.Writer
)

func runForm(f *huh.Form) error {
	if testInput != nil {
		f = f.WithInput(testInput).WithOutput(testOutput)
	}
	return f.Run()
}

type varFlag map[string]string

func (v varFlag) String() string { return "" }

func (v varFlag) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	v[k] = val
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `starter - scaffold a project from a template

  starter                                    pick everything interactively
  starter <template> [dir]                   scaffold directly
  starter list                               show available templates
  starter update                             refresh the shared catalog
  starter new <id>                           scaffold a new template here
  starter source [list|add|remove] <url>     extra template repos (private ok)

  --var key=value   set a template variable (repeatable)
  --yes             never prompt; fail if something is unanswered
  --force           scaffold into a non-empty directory
  --version         print the version and exit

$PROJECT_STARTER_TEMPLATES overrides the templates repo, and accepts a local
path so you can develop templates without pushing.
`)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	vars := varFlag{}
	fs := flag.NewFlagSet("starter", flag.ExitOnError)
	yes := fs.Bool("yes", false, "never prompt; fail if something is unanswered")
	force := fs.Bool("force", false, "scaffold into a non-empty directory")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Var(vars, "var", "template variable, key=value (repeatable)")
	fs.Usage = usage

	// flag stops at the first positional, but `starter litestar myapp --yes` is
	// the documented form, so keep parsing past each one.
	var args []string
	rest := os.Args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		args = append(args, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if *showVersion {
		fmt.Printf("starter %s (%s, built %s)\n", version, commit, date)
		return nil
	}
	if len(args) > 0 && args[0] == "new" {
		return newTemplate(args[1:])
	}

	cats, warns := resolveCatalogs(!*yes)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	dirs := make([]string, 0, len(cats))
	for _, c := range cats {
		dirs = append(dirs, c.Dir)
	}

	if len(args) > 0 && args[0] == "source" {
		return manageSources(args[1:])
	}
	if len(args) > 0 && args[0] == "update" {
		for _, c := range cats {
			fmt.Println("  synced", c.Dir)
		}
		return nil
	}
	templates, err := loadTemplates(cats)
	if err != nil {
		if len(warns) > 0 {
			return warns[0] // the real cause, not the empty-catalog symptom
		}
		return err
	}
	if len(args) > 0 && args[0] == "list" {
		for _, cat := range categories(templates) {
			fmt.Printf("\n%s\n", cat)
			for _, t := range inCategory(templates, cat) {
				mark := ""
				if t.Source != "" {
					mark = "  (" + t.Source + ")"
				}
				fmt.Printf("  %-12s %s%s\n", t.ID, t.Description, mark)
			}
		}
		fmt.Println()
		return nil
	}

	tmpl, err := pickTemplate(templates, args, *yes)
	if err != nil {
		return err
	}

	target := ""
	if len(args) > 1 {
		target = args[1]
	}
	if target != "" {
		if _, ok := vars["project_name"]; !ok {
			vars["project_name"] = filepath.Base(filepath.Clean(target))
		}
	}
	if err := collectVars(tmpl, vars, *yes); err != nil {
		return err
	}
	if err := validName(vars["project_name"]); err != nil {
		return fmt.Errorf("project_name %q: %w", vars["project_name"], err)
	}
	if target == "" {
		target = vars["project_name"]
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	vars["project_dir"] = abs
	// Convenience for templates whose language forbids hyphens in identifiers.
	vars["package_name"] = strings.ReplaceAll(vars["project_name"], "-", "_")

	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 && !*force {
		return fmt.Errorf("%s is not empty (use --force)", abs)
	}

	if err := ensureTools(dirs, tmpl, *yes); err != nil {
		return err
	}

	// pre_steps run in the parent, because a delegated scaffolder creates the
	// project directory itself.
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	for _, s := range tmpl.PreSteps {
		line, err := expand(s, vars)
		if err != nil {
			return err
		}
		if err := runAttached(parent, line); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	if files := filepath.Join(tmpl.Dir, "files"); dirExists(files) {
		if err := render(files, abs, vars); err != nil {
			return err
		}
	}

	for _, s := range tmpl.Steps {
		line, err := expand(s, vars)
		if err != nil {
			return err
		}
		if err := runStep(abs, line, *yes); err != nil {
			return err
		}
	}

	fmt.Printf("\n  %s ready.\n\n    cd %s\n", tmpl.Name, target)
	if tmpl.Next != "" {
		next, err := expand(tmpl.Next, vars)
		if err != nil {
			return err
		}
		fmt.Printf("    %s\n", next)
	}
	fmt.Println()
	return nil
}

func pickTemplate(templates []Template, args []string, yes bool) (Template, error) {
	if len(args) > 0 {
		return findTemplate(templates, args[0])
	}
	if yes {
		return Template{}, errors.New("no template given: name one on the command line, or drop --yes to pick interactively")
	}
	var id string
	cats := categories(templates)

	// One category is not a choice, so don't make the user page past it.
	if len(cats) < 2 {
		if err := runForm(huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Template").Options(options(templates)...).Value(&id),
		))); err != nil {
			return Template{}, err
		}
		return findTemplate(templates, id)
	}

	cat := cats[0]
	err := runForm(huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("What are you building?").
				Options(huh.NewOptions(cats...)...).
				Value(&cat),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Template").
				OptionsFunc(func() []huh.Option[string] {
					return options(inCategory(templates, cat))
				}, &cat).
				Value(&id),
		),
	))
	if err != nil {
		return Template{}, err
	}
	return findTemplate(templates, id)
}

// collectVars fills vars with everything the template declares, prompting for
// whatever --var did not already supply.
func collectVars(tmpl Template, vars varFlag, yes bool) error {
	need := tmpl.Vars
	if !declares(need, "project_name") {
		need = append([]Var{{Key: "project_name", Prompt: "Project name", Default: "myapp"}}, need...)
	}

	if yes {
		var missing []string
		for _, v := range need {
			if _, ok := vars[v.Key]; ok {
				continue
			}
			switch {
			case v.Default != "":
				vars[v.Key] = v.Default
			case len(v.Choices) > 0:
				vars[v.Key] = v.Choices[0]
			default:
				missing = append(missing, v.Key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("--yes given but these have no default: --var %s", strings.Join(missing, "= --var ")+"=")
		}
		return nil
	}

	answers := map[string]*string{}
	var fields []huh.Field
	for _, v := range need {
		if _, ok := vars[v.Key]; ok {
			continue
		}
		val := v.Default
		answers[v.Key] = &val
		title := v.Prompt
		if title == "" {
			title = v.Key
		}
		if len(v.Choices) > 0 {
			if val == "" {
				*answers[v.Key] = v.Choices[0]
			}
			fields = append(fields, huh.NewSelect[string]().
				Title(title).
				Options(huh.NewOptions(v.Choices...)...).
				Value(answers[v.Key]))
			continue
		}
		in := huh.NewInput().Title(title).Value(answers[v.Key])
		if v.Key == "project_name" {
			in = in.Validate(validName)
		}
		fields = append(fields, in)
	}
	if len(fields) == 0 {
		return nil
	}
	if err := runForm(huh.NewForm(huh.NewGroup(fields...))); err != nil {
		return err
	}
	for k, p := range answers {
		vars[k] = strings.TrimSpace(*p)
	}
	return nil
}

// ensureTools installs whatever the template requires and this machine lacks.
// These commands are sudo-level and hard to undo, so they are shown and
// confirmed first, and run attached to the terminal so sudo can prompt.
func ensureTools(dirs []string, tmpl Template, yes bool) error {
	tools, err := loadTools(dirs...)
	if err != nil {
		return err
	}
	plan, err := installPlan(tools, tmpl.Requires)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}
	fmt.Println("\nSome required tools are missing. This will run:")
	for _, c := range plan {
		fmt.Println("    " + c)
	}
	fmt.Println()
	if !yes {
		ok := false
		if err := runForm(huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Install them?").Value(&ok),
		))); err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}
	for _, c := range plan {
		if err := runAttached("", c); err != nil {
			return err
		}
	}
	return nil
}

// runStep streams under --yes so CI logs stay useful, and hides output behind a
// spinner otherwise.
func runStep(dir, line string, yes bool) error {
	if yes {
		fmt.Println("  " + line)
		return runAttached(dir, line)
	}
	var runErr error
	if err := spinner.New().Title(line).Action(func() { runErr = runCaptured(dir, line) }).Run(); err != nil {
		return err
	}
	return runErr
}

// validName keeps project_name usable as a directory, a Python package and an
// npm package name all at once.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func validName(s string) error {
	if !nameRe.MatchString(s) {
		return errors.New("lowercase letters, digits, - and _; must start with a letter")
	}
	return nil
}

func options(ts []Template) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(ts))
	for _, t := range ts {
		label := t.Name
		if t.Source != "" {
			label += "  (" + t.Source + ")"
		}
		opts = append(opts, huh.NewOption(label, t.ID))
	}
	return opts
}

func declares(vs []Var, key string) bool {
	for _, v := range vs {
		if v.Key == key {
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// skeleton is a working template, not a stub: `starter new x` then `starter x`
// should scaffold something rather than error.
const skeleton = `# Top-level keys must come before the first [[vars]] table: in TOML, anything
# after a table header belongs to that table.
name        = "%s"
description = "TODO: one line, shown in the picker"
category    = "Other"
requires    = ["git"]
next        = "cat README.md"

steps = ["git init -q"]

# Prompts. Add "choices" to turn one into a select.
# [[vars]]
# key    = "author"
# prompt = "Author"
# default = "me"

# Run before the files/ overlay, in the parent directory - use this to call an
# official scaffolder such as: npm create astro@latest {{.project_name}}
# pre_steps = []
`

// newTemplate scaffolds a template skeleton in the current directory. The
// intended flow is: clone your own templates repo, cd into it, run this, commit.
func newTemplate(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: starter new <id>   (run this inside a clone of your templates repo)")
	}
	id := args[0]
	if err := validName(id); err != nil {
		return fmt.Errorf("template id %q: %w", id, err)
	}
	if _, err := os.Stat(id); err == nil {
		return fmt.Errorf("%s already exists", id)
	}
	if err := os.MkdirAll(filepath.Join(id, "files"), 0o755); err != nil {
		return err
	}
	manifest := filepath.Join(id, "template.toml")
	if err := os.WriteFile(manifest, []byte(fmt.Sprintf(skeleton, id)), 0o644); err != nil {
		return err
	}
	// git will not track an empty directory, and a template that renders
	// nothing is a confusing first run - so seed one real file.
	seed := filepath.Join(id, "files", "README.md.tmpl")
	if err := os.WriteFile(seed, []byte("# {{.project_name}}\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf(`
  Created %s/

    %s     what it is and what it runs
    %s/     copied into the new project

  Anything ending .tmpl is rendered ({{.project_name}}); everything else is
  copied as-is. Commit and push, then: starter %s ./somewhere

`, id, manifest, filepath.Join(id, "files"), id)
	return nil
}

// manageSources implements `starter source [list|add <url>|remove <url>]`.
// Sources are extra git repos of templates - typically a private one of your
// own - layered over the shared catalog in the order listed.
func manageSources(args []string) error {
	srcs, err := loadSources()
	if err != nil {
		return err
	}
	verb := "list"
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "list":
		path, _ := sourcesPath()
		if len(srcs) == 0 {
			fmt.Printf("  no extra sources (%s)\n", path)
			return nil
		}
		for _, s := range srcs {
			fmt.Printf("  %-12s %s\n", label(s.URL), s.URL)
		}
		return nil

	case "add":
		if len(args) < 2 {
			return errors.New("usage: starter source add <git-url>")
		}
		url := args[1]
		for _, s := range srcs {
			if s.URL == url {
				return fmt.Errorf("%s is already a source", url)
			}
		}
		// Clone before recording it, so a typo or a missing key is reported now
		// rather than on every later run.
		if _, err := syncRepo(url, true); err != nil {
			return err
		}
		if err := saveSources(append(srcs, Source{URL: url})); err != nil {
			return err
		}
		fmt.Printf("  added %s\n", url)
		return nil

	case "remove":
		if len(args) < 2 {
			return errors.New("usage: starter source remove <git-url>")
		}
		out := make([]Source, 0, len(srcs))
		for _, s := range srcs {
			if s.URL != args[1] && label(s.URL) != args[1] {
				out = append(out, s)
			}
		}
		if len(out) == len(srcs) {
			return fmt.Errorf("no source matching %q", args[1])
		}
		if err := saveSources(out); err != nil {
			return err
		}
		fmt.Printf("  removed %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown: starter source %s (want list, add, remove)", verb)
}
