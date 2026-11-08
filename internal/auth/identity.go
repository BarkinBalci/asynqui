package auth

import "context"

// Identity is an authenticated user. Name is the username: it is shown in
// the account menu and matched against authorization bindings.
//
//   - basic: Subject and Name are the configured username.
//   - proxy: Subject and Name are the user header.
//   - oidc: Subject is the "sub" claim, Name the configured username claim
//     (default "email") and Email the "email" claim.
//   - none: the zero Identity.
type Identity struct {
	Subject string
	Name    string
	Email   string
	Groups  []string
}

type identityKey struct{}

// WithIdentity returns a copy of ctx carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity stored by the authentication middleware.
// ok is false when the request is not authenticated.
func IdentityFrom(ctx context.Context) (id Identity, ok bool) {
	id, ok = ctx.Value(identityKey{}).(Identity)
	return id, ok
}
