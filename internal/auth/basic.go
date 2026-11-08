package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"

	"golang.org/x/crypto/bcrypt"
)

// maxPasswordBytes is bcrypt's input limit; longer passwords would be
// silently truncated, so they are rejected.
const maxPasswordBytes = 72

// BasicOptions lists the users of the basic provider.
type BasicOptions struct {
	Users []BasicUser
}

// BasicUser is one configured account with a bcrypt password hash. All
// users must share one bcrypt cost.
type BasicUser struct {
	Username     string
	PasswordHash string
	Groups       []string
}

type basicProvider struct {
	users map[string]BasicUser
	// dummy is compared against when the username is unknown or the
	// password is too long, so a miss costs the same bcrypt work as a hit
	// and does not reveal which usernames exist.
	dummy []byte
}

func newBasic(o BasicOptions) (*basicProvider, error) {
	if len(o.Users) == 0 {
		return nil, errors.New("basic: no users configured")
	}
	users := make(map[string]BasicUser, len(o.Users))
	folded := make(map[string]string, len(o.Users))
	cost := -1
	for i, u := range o.Users {
		if u.Username == "" {
			return nil, fmt.Errorf("basic: user %d: empty username", i)
		}
		if _, dup := users[u.Username]; dup {
			return nil, fmt.Errorf("basic: duplicate username %q", u.Username)
		}
		// Authorization matches usernames case-insensitively, so two users
		// differing only in case would share bindings.
		if other, dup := folded[foldUsername(u.Username)]; dup {
			return nil, fmt.Errorf("basic: usernames %q and %q differ only in case", other, u.Username)
		}
		c, err := bcrypt.Cost([]byte(u.PasswordHash))
		if err != nil {
			// The hash itself is a secret; never echo it.
			return nil, fmt.Errorf("basic: user %q: invalid bcrypt password hash", u.Username)
		}
		// One cost for everyone, so the timing of a failed login does not
		// tell which user (or that no user) was tried.
		if cost >= 0 && c != cost {
			return nil, fmt.Errorf("basic: user %q has bcrypt cost %d, others %d; rehash all passwords at one cost", u.Username, c, cost)
		}
		cost = c
		u.Groups = slices.Clone(u.Groups)
		users[u.Username] = u
		folded[foldUsername(u.Username)] = u.Username
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), cost)
	if err != nil {
		return nil, fmt.Errorf("basic: dummy hash: %w", err)
	}
	return &basicProvider{users: users, dummy: dummy}, nil
}

// verify checks a username and password. It always performs exactly one
// bcrypt comparison at the configured cost. version identifies the
// password hash for the session.
func (b *basicProvider) verify(username, password string) (id Identity, version string, ok bool) {
	u, known := b.users[username]
	usable := known && len(password) <= maxPasswordBytes
	hash := b.dummy
	if usable {
		hash = []byte(u.PasswordHash)
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if err != nil || !usable {
		return Identity{}, "", false
	}
	return u.identity(), u.version(), true
}

// exists reports whether username is configured, so failures are logged
// only for real accounts (an unknown "username" may be a mistyped password).
func (b *basicProvider) exists(username string) bool {
	_, ok := b.users[username]
	return ok
}

// lookup re-checks a session against config: the user must still exist with
// the same password hash. Groups always come from config.
func (b *basicProvider) lookup(username, version string) (Identity, bool) {
	u, ok := b.users[username]
	if !ok || u.version() != version {
		return Identity{}, false
	}
	return u.identity(), true
}

func (u BasicUser) identity() Identity {
	return Identity{Subject: u.Username, Name: u.Username, Groups: slices.Clone(u.Groups)}
}

// version is a short fingerprint of the password hash. It reveals nothing
// useful: the hash is salted and the cookie is encrypted.
func (u BasicUser) version() string {
	sum := sha256.Sum256([]byte(u.PasswordHash))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// foldUsername lowercases ASCII A–Z only. It must match
// authz.FoldUsername, which decides which usernames bindings treat alike.
func foldUsername(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
