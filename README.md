# starter

A TUI for starting projects. Pick a template, answer a couple of prompts, get a
project with its dependencies installed and its first commit made.

    starter                                    # pick everything interactively
    starter litestar myapp                     # skip the picker
    starter litestar myapp --var db=postgres --yes
    starter list
    starter update
    starter source add git@github.com:you/your-templates.git
    starter new my-template

## Install

    go install github.com/madeth1/starter-pack@latest

Or grab a binary or `.deb` from the [releases page][r]. On Windows:

    scoop bucket add madeth1 https://github.com/madeth1/scoop-bucket
    scoop install starter

[r]: https://github.com/madeth1/starter-pack/releases

## What it does

1. Syncs the template catalog (a separate git repo, cached under `~/.cache/project-starter`).
2. Prompts for the template and whatever variables it declares.
3. Installs any required tool this machine lacks - shown and confirmed first,
   since those commands use `sudo`.
4. Runs the template's `pre_steps`, copies its `files/` overlay, runs its `steps`.

## Templates

Templates always come from git, so adding one never touches this binary.

### Your own templates

Put them in a repo of your own - private is fine - and add it as a source:

    starter source add git@github.com:you/your-templates.git
    starter source list
    starter source remove your-templates

Sources are recorded in `~/.config/project-starter/sources.toml` and cloned to
`~/.cache/project-starter`. Authentication is git's own: an SSH remote uses your
key, HTTPS uses your credential helper (`gh auth setup-git`). The tool stores no
tokens. Under `--yes` git prompts are disabled, so CI fails fast instead of
hanging on a password prompt.

Sources layer in the order listed, over the shared catalog. A template whose id
matches an earlier one replaces it, so you can override a shared template with
your own house-style version. Templates from a non-default source are tagged
with the repo name in the picker and in `starter list`.

A source that cannot be reached is a warning, not an error - the templates that
did resolve still work.

To start a new template, clone your repo, then:

    starter new my-template     # writes my-template/{template.toml,files/}

See the templates repo README for the manifest format. Override the default
shared catalog with `$PROJECT_STARTER_TEMPLATES` (any git URL, including a path
to a local repo).

## Layout

| file | |
|---|---|
| `main.go` | flags, the form, orchestration |
| `catalog.go` | catalog sync and manifest parsing |
| `render.go` | the `files/` overlay |
| `bootstrap.go` | tool detection and installation |
