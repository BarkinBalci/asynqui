// Package config loads, interpolates and validates the asynqui YAML
// configuration (docs/requirements.md §8). Secrets have type Secret, which
// never appears in fmt output, slog records or marshaled output.
package config
