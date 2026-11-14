// Package config loads, interpolates and validates the asynqui YAML
// configuration: server, connections, authentication, authorization and UI
// defaults. docs/requirements.md §8 is the reference, and
// config.example.yaml shows every key with its default.
//
// Load parses exactly one YAML document, interpolates ${NAME},
// ${NAME:-default} and $$ in scalar values (never keys), and decodes the
// result on top of the defaults, rejecting unknown fields. It then applies
// Overrides, normalizes and validates. A YAML or interpolation error is
// returned on its own; validation problems are joined, each as
// "location: problem", and never contain a configured value.
//
// Secrets (passwords, password hashes, client secrets and session keys) have
// type Secret, which never appears in fmt output, slog records or marshaled
// output. Config.String and Config.LogValue are safe to print and log.
//
// It imports no other internal package.
package config
