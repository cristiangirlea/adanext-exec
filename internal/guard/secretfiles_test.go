package guard

import "testing"

// The table is harness's (internal/tools/secretfiles_test.go).

func TestSecretFileKnowsTheSecretNamesAndOnlyThem(t *testing.T) {
	for path, want := range map[string]bool{
		".env":                     true,
		".env.local":               true,
		".env.staging":             true,
		"web/.env.production":      true,
		".envrc":                   true,
		".env.example":             true, // refused too: a template costs one turn
		`site\.ENV`:                true, // Windows separators and case
		"worker/.dev.vars":         true,
		"certs/server.pem":         true,
		"keys/signing.key":         true,
		".git-credentials":         true,
		".private/notes.md":        true,
		"a/.private/b/c.txt":       true,
		"home/.ssh/id_ed25519":     true,
		".aws/credentials":         true,
		".kube/config":             true,
		".wrangler/config/default": true,
		".env.":                    true, // Windows drops trailing dots and spaces
		"certs/x.pem ":             true,
		".env::$DATA":              true, // and reads a stream name as the file
		`C:\Users\me\.ssh\config`:  true,
		"release/upload.keystore":  true,
		"certs/client.p12":         true,
		".netrc":                   true,
		"_netrc":                   true,
		".pypirc":                  true,
		"../outside/.env":          true,
		".env/..":                  true, // . and .. are dropped before matching; refusing this costs a turn
		// Logins secret only with the directories above them.
		"home/me/.claude/.credentials.json":                        true,
		"C:/Users/me/.codex/auth.json":                             true,
		"home/me/.local/share/opencode/auth.json":                  true,
		`C:\Users\me\.local\share\opencode\auth.json.::$DATA`:      true,
		"home/me/.local/share/opencode/config.json":                false,
		"AppData/Roaming/GitHub CLI/hosts.yml":                     true,
		"home/me/.config/gh/hosts.yml":                             true,
		"home/me/.docker/config.json":                              true,
		"home/me/.cargo/credentials.toml":                          true,
		"home/me/.config/.wrangler/config/default.toml":            true,
		"AppData/Roaming/xdg.config/.wrangler/config/default.toml": true,
		".npmrc":                                 true,
		"worker/.wrangler/state/v3/d1/db.sqlite": false, // a worker's local state
		"worker/.wrangler/tmp/bundle.js":         false,
		"src/config.json":                        false,
		"home/me/.cargo/config.toml":             false,
		"docs/auth.json":                         false,
		"site/.claude/settings.json":             false,
		"src/env.go":                             false,
		"src/key.go":                             false,
		"docs/keys.md":                           false,
		"internal/private/x.go":                  false, // private without the dot is code
		"testdata/environment.json":              false,
		"README.md":                              false,
	} {
		if got := secretFile(path); got != want {
			t.Errorf("secretFile(%q) = %v, want %v", path, got, want)
		}
	}
}
