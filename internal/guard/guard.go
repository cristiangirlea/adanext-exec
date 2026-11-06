// Package guard refuses command lines that publish, deploy, push, escalate, destroy a root or a
// disk, run a download, or read a secret file. Matching is textual: it stops the plausible
// accident, not a determined adversary. From harness (internal/tools, guard.go and
// secretfiles.go), without the rule that guards hrn's own run environment.
package guard

import (
	"regexp"
	"strings"
)

// A scriptblock built from a download, by either type name and with $(...) too.
const scriptblockOfDownload = `\[(?i:(?:(?:System\.)?Management\.Automation\.)?scriptblock)\]\s*::\s*(?i:Create)\s*\(\s*(?:\$?\(\s*)*(?:` +
	`(?i:curl|wget|irm|iwr|Invoke-RestMethod|Invoke-WebRequest)\s+` +
	`|(?i:New-Object)\s+(?i:(?:System\.)?Net\.WebClient)\s*\)\s*\.\s*(?i:DownloadString)\s*\()`

// Textual rules stop accidents, not hostile commands.
var rules = []struct {
	name  string
	match func(command string) bool
}{
	// pushurl/pushInsteadOf can bypass guarded push rewrites.
	{"git-push", matches(`(?i)\bgit\b[^|&;]*\bpush(?:url|insteadof)?\b`)},
	// Privilege prompts hang without a terminal.
	{"sudo", matches(`\b(?i:sudo)\b`)},
	{"rm-root", rmHitsRoot},
	// Catch indirect pipes and PowerShell evaluation too.
	{"pipe-to-shell", matches(
		`\b(?i:curl|wget|irm|iwr|Invoke-RestMethod|Invoke-WebRequest)\b.*\|\s*(?:[&.]\s*)?(?:(?i:sudo)\s+)?(?:"[^"|]*[\\/]|'[^'|]*[\\/]|[^\s"'|]*[\\/])?["']?(?i:sh|bash|zsh|powershell|pwsh|iex|Invoke-Expression)\b` +
			`|(?:^|[^\w])(?:&|\.)\s*\(\s*` + scriptblockOfDownload +
			`|` + scriptblockOfDownload + `.*\)\s*\.\s*(?i:Invoke(?:ReturnAsIs)?)\s*\(` +
			`|(?:^|[^\w-])(?i:iex|Invoke-Expression)\s*(?:-(?i:c\w*)\s+)?\$?\(\s*(?:\(\s*)*(?:` +
			`(?i:curl|wget|irm|iwr|Invoke-RestMethod|Invoke-WebRequest)\s+` +
			`|(?i:New-Object)\s+(?i:(?:System\.)?Net\.WebClient)\s*\)\s*\.\s*(?i:DownloadString)\s*\()`,
	)},
	// Accept unlikely Unix case collisions.
	{"disk", matches(`\b(?i:mkfs)|\b(?i:format)\s+[A-Za-z]:|\b(?i:dd)\b[^|&;]*\bof=\s*(?:/dev/|\\\\\.\\)`)},
	// Block GET: fields imply POST; logins guard pr create.
	{"gh-publish", matches(
		`\b(?i:gh)\b[^|&;]*\b(?:pr\s+merge|release\s+(?:create|upload|edit|delete)|repo\s+(?:delete|archive|rename|edit\b[^|&;]*--visibility))\b` +
			`|\b(?i:gh(?:\.exe)?)["']?\s+api\b[^|&;]*(?:-X|--method)[\s="'\\$]*(?i:PUT|PATCH|DELETE)\b` +
			`|\b(?i:gh(?:\.exe)?)["']?\s+api\b[^|&;]*(?:/(?:releases|git/(?:refs|tags)|rulesets|keys|hooks|collaborators|dispatches|merges|environments|transfer|deployments|statuses|branches/[^/\s"'?]+/rename|merge-upstream|forks|pages|actions/(?:secrets|variables|runs/[^/\s"'?]+/pending_deployments|runners/(?:registration-token|generate-jitconfig|remove-token))|user/repos|orgs/[^/\s"'?]+/repos|gists))(?:/|[\s"'?]|$)` +
			`|\b(?i:gh(?:\.exe)?)["']?\s+api(?:\s+-[-\w]+(?:[\s=]+(?:[^\s"'-]|"[^"]*"|'[^']*')(?:[^\s"']|"[^"]*"|'[^']*')*)?)*\s+["']?(?:user/repos|orgs/[^/\s"'?]+/repos|gists)(?:/|[\s"'?]|$)` +
			`|\b(?i:gh(?:\.exe)?)["']?\s+api\b[^|&;]*(?:\bgraphql\b[^|&;]*(?i:mutation)\b|(?i:mutation)\b[^|&;]*\bgraphql\b)`,
	)},
	// Redirects cannot hide system credentials.
	{"gh-token", matches(`(?i)\bgh\b[^|&;]*\bauth\s+(?:token|git-credential)\b|\bgh\b[^|&;]*\bauth\s+status\b[^|&;]*(?:--show-token\b|\s-[a-z]*t\b)`)},
	// Shells must not bypass secret-file refusals.
	{"secret-file", namesSecretFile},
	// Project scripts can deploy too.
	{"deploy", matches(`\bwrangler\b[^|&;]*\b(?:deploy|publish)\b|\b(?:npm|pnpm|yarn|bun|cargo)\s+publish\b|\b(?:npm|pnpm|yarn|bun)\b[^|&;]*\b(?:run(?:-script)?\s+)?deploy\b|\bdeploy/push\.sh\b|\bkubectl\b[^|&;]*\b(?:apply|delete|replace|patch|rollout)\b|\b(?:terraform|tofu)\b[^|&;]*\b(?:apply|destroy)\b|\bhelm\b[^|&;]*\b(?:install|upgrade|uninstall|delete)\b|\bdocker\s+push\b|\bvercel\b[^|&;]*--prod\b|\b(?:fly|flyctl|netlify)\s+deploy\b`)},
}

func matches(pattern string) func(string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString
}

// Names lists the rules in the order Matches reports them.
func Names() []string {
	names := make([]string, len(rules))
	for i, r := range rules {
		names[i] = r.name
	}
	return names
}

// Matches returns every rule the command line meets, in rule order, accepting false positives.
func Matches(command string) []string {
	quoted := quotedSeparators(command)
	var hit []string
	// Both forms prevent quotes hiding real pipes.
	for _, r := range rules {
		if r.match(command) || r.match(quoted) {
			hit = append(hit, r.name)
		}
	}
	return hit
}

func quotedSeparators(command string) string {
	line := []byte(command)
	var quote byte
	escaped := false
	for i, c := range line {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 && c == quote {
			quote = 0
			continue
		}
		if quote == 0 && (c == '\'' || c == '"') {
			quote = c
			continue
		}
		if quote != 0 && (c == '|' || c == '&' || c == ';') {
			line[i] = ' '
		}
	}
	return string(line)
}

var rmCallRE = regexp.MustCompile(`\brm\b([^|&;]*)`)

// rm's root protection misses /* and $HOME.
func rmHitsRoot(command string) bool {
	for _, call := range rmCallRE.FindAllStringSubmatch(command, -1) {
		recursive, root := false, false
		for _, tok := range strings.Fields(call[1]) {
			switch {
			case strings.HasPrefix(tok, "--"):
				recursive = recursive || tok == "--recursive"
			case strings.HasPrefix(tok, "-"):
				recursive = recursive || strings.ContainsAny(tok, "rR")
			default:
				root = root || isRootTarget(tok)
			}
		}
		if recursive && root {
			return true
		}
	}
	return false
}

// Git Bash drive roots use /c.
var driveRoot = regexp.MustCompile(`^(?:[A-Za-z]:|/[A-Za-z])$`)

func isRootTarget(arg string) bool {
	// "$HOME"/ is one token despite partial quoting.
	arg = strings.NewReplacer(`"`, "", "'", "").Replace(arg)
	arg = strings.TrimSuffix(arg, "*")
	arg = strings.ReplaceAll(arg, `\`, "/")
	if arg != "/" {
		arg = strings.TrimSuffix(arg, "/")
	}
	switch arg {
	case "/", "~", "$HOME", "${HOME}", "%USERPROFILE%":
		return true
	}
	return driveRoot.MatchString(arg)
}
