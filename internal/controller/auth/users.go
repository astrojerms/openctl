package auth

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// usersFileName is the controller-directory file that defines named users and
// their roles. Absent file = single-user (root token only).
const usersFileName = "users.yaml"

// User is a named principal authenticated by its own bearer token, distinct
// from the install-time root token. Loaded from users.yaml.
type User struct {
	// UserID is the login name (unique; "root" is reserved).
	UserID string
	// Role is the user's permission level.
	Role Role
	// Token is the raw bearer token this user presents.
	Token string
}

// userFileEntry is the on-disk YAML shape of one user.
type userFileEntry struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
	// TokenFile holds the user's bearer token (mode 0600). Relative paths
	// resolve against the controller directory. Created (with a fresh random
	// token) if it does not exist, mirroring the root token bootstrap.
	TokenFile string `yaml:"tokenFile"`
}

type usersFile struct {
	Users []userFileEntry `yaml:"users"`
}

// LoadUsers reads <dir>/users.yaml and returns the configured named users,
// loading (or minting) each user's token file. A missing users.yaml is not an
// error — it returns nil (single-user mode). It validates that names are
// non-empty, unique, not "root", and that roles are recognized.
func LoadUsers(dir string) ([]User, error) {
	path := filepath.Join(dir, usersFileName)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the controller dir + a fixed name
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var parsed usersFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := make(map[string]bool, len(parsed.Users))
	out := make([]User, 0, len(parsed.Users))
	for i, e := range parsed.Users {
		if e.Name == "" {
			return nil, fmt.Errorf("%s: user %d has no name", path, i)
		}
		if e.Name == "root" {
			return nil, fmt.Errorf("%s: user name %q is reserved", path, e.Name)
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("%s: duplicate user name %q", path, e.Name)
		}
		seen[e.Name] = true

		role := Role(e.Role)
		if role.rank() == 0 {
			return nil, fmt.Errorf("%s: user %q has invalid role %q (want viewer, editor, or admin)", path, e.Name, e.Role)
		}
		if e.TokenFile == "" {
			return nil, fmt.Errorf("%s: user %q has no tokenFile", path, e.Name)
		}
		tokenFile := e.TokenFile
		if !filepath.IsAbs(tokenFile) {
			tokenFile = filepath.Join(dir, tokenFile)
		}
		token, err := LoadOrCreateToken(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("%s: user %q token: %w", path, e.Name, err)
		}

		out = append(out, User{UserID: e.Name, Role: role, Token: token})
	}
	return out, nil
}
