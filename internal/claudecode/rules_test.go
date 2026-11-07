package claudecode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// rulesOf is the rules from one settings text, as the project's settings.json.
func rulesOf(t *testing.T, settings string) *Rules {
	t.Helper()
	project := t.TempDir()
	write(t, filepath.Join(project, ".claude", "settings.json"), settings)
	r, err := Load(Sources{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Rules match as Claude Code's documentation says: a trailing " *" or ":*" also matches the bare
// command, "*" matches any text, deny and ask reach every command of a compound one, also in a
// substitution or a loop, past assignments and the wrappers Claude Code looks through.
func TestBashRulesMatchAsClaudeCodeDoes(t *testing.T) {
	for _, tc := range []struct {
		rule    string
		match   []string
		nomatch []string
	}{
		{"Bash(rm *)",
			[]string{"rm", "rm -rf x", "cd /tmp && rm -rf x", `echo "$(rm -rf x)"`, "echo `rm x`", "FOO=bar rm -rf tmp/",
				"timeout 10 rm x", "timeout -s KILL 5s rm x", "nice -n 5 rm x", "nohup rm x", "time -p rm x", "stdbuf -o L rm x",
				"command rm x", "builtin rm x", "xargs rm", "for f in a b; do rm $f; done", "if true; then rm x; fi",
				"ls | rm x", "a || rm x", "a & rm x", "a\nrm x", "(rm x)", "{ rm x; }", "! rm x", "'rm' x"},
			[]string{"rmdir x", "echo rm x", "lsof", "xargs -n1 rm x", "command -v rm", `git commit -m "rm x"`}},
		{"Bash(ls*)", []string{"ls", "lsof", "ls -la"}, []string{"cat ls"}},
		{"Bash(ls:*)", []string{"ls", "ls -la"}, []string{"lsof"}},
		{"Bash(npm run build)", []string{"npm run build", "x && npm run build"}, []string{"npm run build2", "npm run build --x"}},
		{"Bash(git * main)", []string{"git push origin main", "git checkout main"}, []string{"git push origin dev"}},
		{"Bash(git push *)", []string{"git push", "git push origin main", "git 'push' origin"}, []string{"git pull", "git -C . pull"}},
		{"Bash", []string{"anything at all", "ls"}, nil},
		{"Bash(*)", []string{"anything at all"}, nil},
	} {
		r := rulesOf(t, `{"permissions": {"deny": [`+quote(tc.rule)+`]}}`)
		for _, cmd := range tc.match {
			if d, rule := r.Check("Bash", cmd); d != Deny || rule != tc.rule {
				t.Errorf("%s on %q: %q %q, want deny", tc.rule, cmd, d, rule)
			}
		}
		for _, cmd := range tc.nomatch {
			if d, _ := r.Check("Bash", cmd); d != "" {
				t.Errorf("%s on %q: %q, want no match", tc.rule, cmd, d)
			}
		}
		if d, _ := r.Check("PowerShell", "rm x"); d != "" {
			t.Errorf("%s matches a PowerShell command", tc.rule)
		}
	}
}

// PowerShell rules ignore case, match a cmdlet's aliases, and reach every command of a pipeline
// or a chain.
func TestPowerShellRulesMatchAliasesAndIgnoreCase(t *testing.T) {
	r := rulesOf(t, `{"permissions": {"ask": ["PowerShell(Remove-Item *)"], "deny": ["PowerShell(Invoke-Expression)"]}}`)
	for _, cmd := range []string{"Remove-Item x", "remove-item -Recurse x", "rm x", "RI x", "del x", "Get-Date; ri x", "gci | Remove-Item", "a && rm x", "$(rm x)"} {
		if d, rule := r.Check("PowerShell", cmd); d != Ask || rule != "PowerShell(Remove-Item *)" {
			t.Errorf("%q: %q %q, want ask", cmd, d, rule)
		}
	}
	for _, cmd := range []string{"Get-ChildItem", "Remove-ItemProperty x", "echo rm"} {
		if d, _ := r.Check("PowerShell", cmd); d != "" {
			t.Errorf("%q: %q, want no match", cmd, d)
		}
	}
	if d, _ := r.Check("PowerShell", "iex"); d != Deny {
		t.Errorf("iex: %q, want deny by its cmdlet", d)
	}
	if d, _ := r.Check("Bash", "rm x"); d != "" {
		t.Errorf("a PowerShell rule matches Bash")
	}
}

// Deny wins over ask; allow rules and other tools' rules count for nothing here.
func TestDenyWinsAndOtherRulesAreLeftOut(t *testing.T) {
	r := rulesOf(t, `{"permissions": {"allow": ["Bash(rm *)"], "ask": ["Bash(rm *)", "Read(./.env)", "WebFetch"], "deny": ["Bash(rm -rf *)", "Bash(unclosed"]}}`)
	if d, rule := r.Check("Bash", "rm -rf x"); d != Deny || rule != "Bash(rm -rf *)" {
		t.Errorf("rm -rf: %q %q, want deny", d, rule)
	}
	if d, _ := r.Check("Bash", "rm x"); d != Ask {
		t.Errorf("rm: %q, want ask", d)
	}
	if d, _ := r.Check("Bash", "cat .env"); d != "" {
		t.Errorf("cat .env: %q, want nothing", d)
	}
}

// Every settings file Claude Code reads counts: managed, its drop-ins, the user's, the project's
// and its local one. A managed allowManagedPermissionRulesOnly leaves only the managed rules.
func TestLoadReadsEverySettingsFile(t *testing.T) {
	s := Sources{Managed: t.TempDir(), User: t.TempDir(), Project: t.TempDir()}
	files := map[string]string{
		filepath.Join(s.Managed, "managed-settings.json"):              `{"permissions": {"deny": ["Bash(m1)"]}}`,
		filepath.Join(s.Managed, "managed-settings.d", "10-x.json"):    `{"permissions": {"ask": ["Bash(m2)"]}}`,
		filepath.Join(s.Managed, "managed-settings.d", "notes.txt"):    `not read`,
		filepath.Join(s.User, "settings.json"):                         `{"permissions": {"deny": ["Bash(u)"]}, "model": "x"}`,
		filepath.Join(s.Project, ".claude", "settings.json"):           `{"permissions": {"ask": ["Bash(p)"]}}`,
		filepath.Join(s.Project, ".claude", "settings.local.json"):     `{"permissions": {"deny": ["Bash(l)"]}}`,
		filepath.Join(s.Project, ".claude", "settings.unrelated.json"): `{"permissions": {"deny": ["Bash(x)"]}}`,
	}
	for path, text := range files {
		write(t, path, text)
	}
	r, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]Decision{"m1": Deny, "m2": Ask, "u": Deny, "p": Ask, "l": Deny, "x": ""} {
		if d, _ := r.Check("Bash", cmd); d != want {
			t.Errorf("%s: %q, want %q", cmd, d, want)
		}
	}
	if len(r.Files) != 5 {
		t.Errorf("files %q, want the five with rules", r.Files)
	}

	write(t, filepath.Join(s.Managed, "managed-settings.d", "20-only.json"), `{"allowManagedPermissionRulesOnly": true}`)
	if r, err = Load(s); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]Decision{"m1": Deny, "m2": Ask, "u": "", "p": "", "l": ""} {
		if d, _ := r.Check("Bash", cmd); d != want {
			t.Errorf("managed only, %s: %q, want %q", cmd, d, want)
		}
	}
}

// Files that are not there are skipped; one that is there but cannot be read or is not valid is
// an error that names it, since its rules cannot be applied.
func TestLoadFailsOnASettingsFileItCannotRead(t *testing.T) {
	if _, err := Load(Sources{Managed: t.TempDir(), User: t.TempDir(), Project: t.TempDir()}); err != nil {
		t.Errorf("no files: %v", err)
	}
	for name, text := range map[string]string{
		"not JSON":       `{"permissions": `,
		"deny not list":  `{"permissions": {"deny": "Bash(rm *)"}}`,
		"rule not text":  `{"permissions": {"ask": [1]}}`,
		"too large":      `{"x": "` + strings.Repeat("a", maxSettingsSize) + `"}`,
		"managed not ok": `[]`,
	} {
		project := t.TempDir()
		path := filepath.Join(project, ".claude", "settings.json")
		write(t, path, text)
		if _, err := Load(Sources{Project: project}); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v, want an error naming %s", name, err, path)
		}
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".claude", "settings.local.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Sources{Project: project}); err == nil {
		t.Error("a settings file that is a directory: no error")
	}
}

// The user's directory is $CLAUDE_CONFIG_DIR when it is set, else ~/.claude; the managed one is
// the platform's.
func TestDefaultSources(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	s, err := DefaultSources("/p")
	if err != nil || s.User != os.Getenv("CLAUDE_CONFIG_DIR") || s.Project != "/p" {
		t.Errorf("%+v %v", s, err)
	}
	want := map[string]string{"windows": "ClaudeCode", "darwin": "/Library/Application Support/ClaudeCode", "linux": "/etc/claude-code"}[runtime.GOOS]
	if want != "" && !strings.HasSuffix(s.Managed, want) {
		t.Errorf("managed %q, want %q", s.Managed, want)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, _ := os.UserHomeDir()
	if s, _ := DefaultSources(""); s.User != filepath.Join(home, ".claude") {
		t.Errorf("user %q", s.User)
	}
}

func quote(s string) string { return `"` + s + `"` }
