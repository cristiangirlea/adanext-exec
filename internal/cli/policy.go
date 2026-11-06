package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/cristiangirlea/adanext-exec/broker"
)

// configDir is where the policy and the profiles are looked for; tests replace it.
var configDir = os.UserConfigDir

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// selectPolicy finds the policy a command is judged by: the file given, the profile given, or
// the user's policy.json, and the built-in rules when there is none of those. A file that is
// named, by path or profile, must be usable.
func selectPolicy(file, profile string) (*broker.Policy, error) {
	switch {
	case file != "" && profile != "":
		return nil, errUsage("give --policy or --profile, not both")
	case file != "":
		return broker.LoadPolicy(file)
	}
	dir, err := configDir()
	if err != nil {
		return nil, fmt.Errorf("%w: find the user's configuration directory: %v", broker.ErrPolicy, err)
	}
	if profile != "" {
		if !profileName.MatchString(profile) {
			return nil, errUsage(fmt.Sprintf("profile %q is not lower-case letters, digits and dashes", profile))
		}
		return broker.LoadPolicy(filepath.Join(dir, "adanext-exec", "profiles", profile+".json"))
	}
	path := filepath.Join(dir, "adanext-exec", "policy.json")
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return broker.DefaultPolicy(), nil
	}
	return broker.LoadPolicy(path)
}

// usageError is an argument that is not valid.
type usageError string

func (e usageError) Error() string { return string(e) }

func errUsage(s string) error { return usageError(s) }
