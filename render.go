package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// render copies srcDir onto dstDir.
//
// Substitution is opt-in because Vue, Nuxt and Astro sources contain {{ }} as
// real syntax: only *.tmpl files go through text/template (and lose the
// suffix), everything else is copied byte-for-byte. Path segments substitute a
// __key__ token instead, for the same reason.
func render(srcDir, dstDir string, vars map[string]string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil || rel == "." {
			return err
		}
		dst := filepath.Join(dstDir, substPath(rel, vars))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if strings.HasSuffix(dst, ".tmpl") {
			return renderFile(path, strings.TrimSuffix(dst, ".tmpl"), info.Mode(), vars)
		}
		return copyFile(path, dst, info.Mode())
	})
}

func substPath(rel string, vars map[string]string) string {
	for k, v := range vars {
		rel = strings.ReplaceAll(rel, "__"+k+"__", v)
	}
	return rel
}

func renderFile(src, dst string, mode fs.FileMode, vars map[string]string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, err := expand(string(b), vars)
	if err != nil {
		return err
	}
	// A template that renders to nothing produces no file. That is how a
	// template makes a whole file conditional - a compose.yaml that is only
	// needed when you picked postgres, say - without any extra manifest syntax.
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return os.WriteFile(dst, []byte(out), mode)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// expand renders one string with the collected vars. missingkey=error means a
// typo fails loudly instead of writing "<no value>" into somebody's config.
func expand(s string, vars map[string]string) (string, error) {
	t, err := template.New("").Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
