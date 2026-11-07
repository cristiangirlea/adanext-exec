// Package claudecode reads the permission rules Claude Code keeps in its settings files and
// matches their Bash and PowerShell rules against a command's original text. The hook rewrites a
// command before Claude Code applies those rules, so it applies their deny and ask itself.
package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Decision is what a user's rule says of a command: Deny, Ask, or nothing.
type Decision string

const (
	Deny Decision = "deny"
	Ask  Decision = "ask"
)

// Sources are the places Claude Code's settings files are read from.
type Sources struct {
	// Managed is the directory of managed-settings.json and managed-settings.d.
	Managed string
	// User is the user's Claude Code directory, ~/.claude or $CLAUDE_CONFIG_DIR.
	User string
	// Project is the project's root, which holds .claude/settings.json and settings.local.json.
	Project string
}

// DefaultSources are the places Claude Code reads on this platform, for a project root.
func DefaultSources(project string) (Sources, error) {
	s := Sources{Project: project}
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		s.Managed = filepath.Join(pf, "ClaudeCode")
	case "darwin":
		s.Managed = "/Library/Application Support/ClaudeCode"
	default:
		s.Managed = "/etc/claude-code"
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		s.User = dir
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return Sources{}, fmt.Errorf("find the user's home: %v", err)
		}
		s.User = filepath.Join(home, ".claude")
	}
	return s, nil
}

// Rules are the deny and ask rules for Bash and PowerShell from every settings file read.
type Rules struct {
	deny, ask []rule
	// Files are the settings files the rules came from, for the reason a hook gives.
	Files []string
}

// settingsFile is the part of a settings file read here.
type settingsFile struct {
	Permissions struct {
		Deny []string `json:"deny"`
		Ask  []string `json:"ask"`
	} `json:"permissions"`
	ManagedOnly bool `json:"allowManagedPermissionRulesOnly"`
}

// maxSettingsSize bounds what is read from one settings file.
const maxSettingsSize = 1 << 20

// Load reads the settings files Claude Code reads: managed-settings.json and the files in
// managed-settings.d, the user's settings.json, and the project's settings.json and
// settings.local.json. A file that is not there is skipped; one that is there but cannot be
// read or is not valid is an error, since its rules cannot be applied. When a managed file sets
// allowManagedPermissionRulesOnly, only the managed rules count, as in Claude Code.
func Load(s Sources) (*Rules, error) {
	r := &Rules{}
	managed := []string{}
	if s.Managed != "" {
		managed = append(managed, filepath.Join(s.Managed, "managed-settings.json"))
		entries, err := os.ReadDir(filepath.Join(s.Managed, "managed-settings.d"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %v", filepath.Join(s.Managed, "managed-settings.d"), err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				managed = append(managed, filepath.Join(s.Managed, "managed-settings.d", e.Name()))
			}
		}
	}
	managedOnly := false
	for _, path := range managed {
		f, err := readSettings(path)
		if err != nil {
			return nil, err
		}
		if f != nil {
			managedOnly = managedOnly || f.ManagedOnly
			r.add(path, f)
		}
	}
	if managedOnly {
		return r, nil
	}
	var others []string
	if s.User != "" {
		others = append(others, filepath.Join(s.User, "settings.json"))
	}
	if s.Project != "" {
		others = append(others, filepath.Join(s.Project, ".claude", "settings.json"), filepath.Join(s.Project, ".claude", "settings.local.json"))
	}
	for _, path := range others {
		f, err := readSettings(path)
		if err != nil {
			return nil, err
		}
		if f != nil {
			r.add(path, f)
		}
	}
	return r, nil
}

func (r *Rules) add(path string, f *settingsFile) {
	n := len(r.deny) + len(r.ask)
	for _, text := range f.Permissions.Deny {
		if ru, ok := parseRule(text); ok {
			r.deny = append(r.deny, ru)
		}
	}
	for _, text := range f.Permissions.Ask {
		if ru, ok := parseRule(text); ok {
			r.ask = append(r.ask, ru)
		}
	}
	if len(r.deny)+len(r.ask) > n && !slices.Contains(r.Files, path) {
		r.Files = append(r.Files, path)
	}
}

// readSettings reads one settings file; a file that is not there is nil.
func readSettings(path string) (*settingsFile, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %v", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsSize+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("read %s: %v", path, err)
	case len(data) > maxSettingsSize:
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxSettingsSize)
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s is not a settings file Claude Code reads: %v", path, err)
	}
	return &f, nil
}

// Check is the strictest of the user's rules for a tool's command: Deny, else Ask, else "", with
// the rule that decided.
func (r *Rules) Check(tool, command string) (Decision, string) {
	for _, set := range []struct {
		d     Decision
		rules []rule
	}{{Deny, r.deny}, {Ask, r.ask}} {
		for _, ru := range set.rules {
			if ru.tool == tool && ru.matches(command) {
				return set.d, ru.text
			}
		}
	}
	return "", ""
}
