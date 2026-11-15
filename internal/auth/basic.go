package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"

	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// maxPasswordBytes is bcrypt's input limit; longer passwords would be
// silently truncated, so they are rejected.
const maxPasswordBytes = 72

type basicProvider struct {
	users map[string]config.BasicUser
	// dummy is compared against when the username is unknown or the
	// password is too long, so a miss costs the same bcrypt work as a hit
	// and does not reveal which usernames exist.
	dummy []byte
}

// newBasic indexes the users that config.Load validated: at least one,
// distinct usernames, bcrypt hashes of one cost.
func newBasic(b config.Basic) (*basicProvider, error) {
	if len(b.Users) == 0 {
		return nil, errors.New("basic: no users configured")
	}
	users := make(map[string]config.BasicUser, len(b.Users))
	for _, u := range b.Users {
		u.Groups = slices.Clone(u.Groups)
		users[u.Username] = u
	}
	// The dummy hash shares the users' cost.
	cost, err := bcrypt.Cost([]byte(b.Users[0].PasswordHash.Reveal()))
	if err != nil {
		return nil, errors.New("basic: invalid bcrypt password hash") // never echo the hash
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
		hash = []byte(u.PasswordHash.Reveal())
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if err != nil || !usable {
		return Identity{}, "", false
	}
	return identity(u), hashVersion(u), true
}

// exists reports whether username is configured, so failures are logged
// only for real accounts (an unknown "username" may be a mistyped password).
func (b *basicProvider) exists(username string) bool {
	_, ok := b.users[username]
	return ok
}

// lookup re-checks a session against config: the user must still exist with
// the same password hash. Groups always come from config.
func (b *basicProvider) lookup(username, v string) (Identity, bool) {
	u, ok := b.users[username]
	if !ok || hashVersion(u) != v {
		return Identity{}, false
	}
	return identity(u), true
}

func identity(u config.BasicUser) Identity {
	return Identity{Subject: u.Username, Name: u.Username, Groups: slices.Clone(u.Groups)}
}

// hashVersion is a short fingerprint of the password hash. It reveals nothing
// useful: the hash is salted and the cookie is encrypted.
func hashVersion(u config.BasicUser) string {
	sum := sha256.Sum256([]byte(u.PasswordHash.Reveal()))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}
