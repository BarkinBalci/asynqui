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

// BasicOptions lists the users of the basic provider.
type BasicOptions struct {
	Users []BasicUser
}

// BasicUser is one configured account with a bcrypt password hash. bcrypt
// only reads the first 72 bytes of a password.
type BasicUser struct {
	Username     string
	PasswordHash string
	Groups       []string
}

type basicProvider struct {
	users map[string]BasicUser
	// dummy is compared against when the username is unknown, so a miss
	// costs the same bcrypt work as a hit and does not reveal which
	// usernames exist.
	dummy []byte
}

func newBasic(o BasicOptions) (*basicProvider, error) {
	if len(o.Users) == 0 {
		return nil, errors.New("basic: no users configured")
	}
	users := make(map[string]BasicUser, len(o.Users))
	maxCost := bcrypt.MinCost
	for i, u := range o.Users {
		if u.Username == "" {
			return nil, fmt.Errorf("basic: user %d: empty username", i)
		}
		if _, dup := users[u.Username]; dup {
			return nil, fmt.Errorf("basic: duplicate username %q", u.Username)
		}
		cost, err := bcrypt.Cost([]byte(u.PasswordHash))
		if err != nil {
			// The hash itself is a secret; never echo it.
			return nil, fmt.Errorf("basic: user %q: invalid bcrypt password hash", u.Username)
		}
		maxCost = max(maxCost, cost)
		u.Groups = slices.Clone(u.Groups)
		users[u.Username] = u
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), maxCost)
	if err != nil {
		return nil, fmt.Errorf("basic: dummy hash: %w", err)
	}
	return &basicProvider{users: users, dummy: dummy}, nil
}

// verify checks a username and password. It always performs exactly one
// bcrypt comparison, at the highest configured cost for unknown users.
// version identifies the password hash for the session.
func (b *basicProvider) verify(username, password string) (id Identity, version string, ok bool) {
	u, known := b.users[username]
	hash := b.dummy
	if known {
		hash = []byte(u.PasswordHash)
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if err != nil || !known {
		return Identity{}, "", false
	}
	return u.identity(), u.version(), true
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
