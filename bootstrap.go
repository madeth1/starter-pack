package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// managers lists the package managers we can drive, most-preferred first.
// key is the field looked up in tools.toml; install takes the package list.
var managers = []struct{ bin, key, install string }{
	{"brew", "brew", "brew install %s"},
	{"apt-get", "apt", "sudo apt-get install -y %s"},
	{"dnf", "dnf", "sudo dnf install -y %s"},
	{"pacman", "pacman", "sudo pacman -S --noconfirm %s"},
	{"apk", "apk", "sudo apk add %s"},
	{"winget", "winget", "winget install --silent %s"},
}

// toolSpec is deliberately an untyped map so a new package manager or field can
// be added to tools.toml in the templates repo without rebuilding the binary.
// Recognised keys: "check", "script", and one per entry in managers.
type toolSpec map[string]string

// loadTools merges the tools.toml of each directory, so a personal template can
// declare a tool the shared catalog has never heard of. Later directories win.
func loadTools(dirs ...string) (map[string]toolSpec, error) {
	merged := map[string]toolSpec{}
	for _, dir := range dirs {
		path := filepath.Join(dir, "tools.toml")
		if _, err := os.Stat(path); err != nil {
			continue // a catalog need not declare any tools
		}
		var tools map[string]toolSpec
		if _, err := toml.DecodeFile(path, &tools); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for name, spec := range tools {
			merged[name] = spec
		}
	}
	return merged, nil
}

// detectManager returns the tools.toml key and install command template for the
// first package manager present on this machine.
func detectManager() (key, install string, ok bool) {
	for _, m := range managers {
		if _, err := exec.LookPath(m.bin); err == nil {
			return m.key, m.install, true
		}
	}
	return "", "", false
}

// installPlan returns, for each required tool that is not already present, the
// command that would install it. An unsatisfiable requirement is an error
// rather than a guess.
func installPlan(tools map[string]toolSpec, required []string) ([]string, error) {
	mgrKey, mgrInstall, haveMgr := detectManager()
	var plan []string
	for _, name := range required {
		spec, known := tools[name]
		if !known {
			return nil, fmt.Errorf("template requires %q but tools.toml does not describe it", name)
		}
		if isInstalled(name, spec) {
			continue
		}
		cmd, err := installCmd(name, spec, mgrKey, mgrInstall, haveMgr)
		if err != nil {
			return nil, err
		}
		plan = append(plan, cmd)
	}
	return plan, nil
}

func installCmd(name string, spec toolSpec, mgrKey, mgrInstall string, haveMgr bool) (string, error) {
	if haveMgr {
		if pkg := spec[mgrKey]; pkg != "" {
			return fmt.Sprintf(mgrInstall, pkg), nil
		}
	}
	if s := spec["script"]; s != "" {
		return s, nil
	}
	return "", fmt.Errorf("don't know how to install %q on this system (no %q entry and no script in tools.toml) - install it manually and re-run", name, mgrKey)
}

// isInstalled runs the tool's check command. Running it, rather than looking up
// a binary name, lets tools.toml express a version gate later without a code
// change.
func isInstalled(name string, spec toolSpec) bool {
	check := spec["check"]
	if check == "" {
		check = name + " --version"
	}
	cmd := exec.Command("sh", "-c", check)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// runAttached gives the command the real terminal, because pre_steps are
// third-party scaffolders that may prompt.
func runAttached(dir, line string) error {
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", line, err)
	}
	return nil
}

// runCaptured hides output so it can sit under a spinner, and surfaces it only
// when the command fails.
func runCaptured(dir, line string) error {
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", line, err, strings.TrimSpace(string(out)))
	}
	return nil
}
