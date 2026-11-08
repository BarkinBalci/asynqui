// Package config loads, interpolates and validates the asynqui YAML
// configuration: server, connections, authentication, authorization and UI
// defaults. docs/requirements.md §8 is the reference, and
// config.example.yaml shows every key with its default.
//
// Load runs these steps, in order:
//
//  1. Parse exactly one YAML document.
//  2. Walk the tree alongside the Config type:
//     - Interpolate environment variables into scalar values, never keys:
//     ${NAME}, ${NAME:-default} (default only when NAME is unset; nestable)
//     and $$ for a literal $. Any other $ is kept. An unset variable without
//     a default is an error naming the variable; one set to "" is used as is.
//     - Read an interpolated value as its field's type whatever its quoting:
//     strings verbatim, integers as decimal (DB=010 is 10), everything else
//     as if written unquoted. An empty value of a non-string field keeps the
//     default.
//     - Reject unknown fields, null list entries and values of the wrong
//     type, including an alias of an interpolated value used for a field of
//     another type.
//  3. Decode on top of the defaults. An absent or null key keeps its
//     default; an explicit zero is kept, so shutdown_delay: 0s disables the
//     delay and ttl: 0s is rejected.
//  4. Apply Overrides (flags and ASYNQUI_* variables), then normalize:
//     base_path "/" becomes "", openid is added to the OIDC scopes, and
//     authz.default gets its provider-dependent default.
//  5. Validate.
//
// Problems from steps 2 to 5 are reported together (errors.Join), ordered by
// line, each as "line N: location: problem", e.g.
// "line 12: connections[2].name: must match ...". A value that failed in
// step 2 is not validated again. Messages never contain a configured value.
//
// Secrets (passwords, password hashes, client secrets and session keys) have
// type Secret, which never appears in fmt output, slog records or marshaled
// output. Config.String and Config.LogValue are safe to print and log.
//
// It imports no other internal package.
package config
