// Package config loads, interpolates and validates the asynqui YAML
// configuration: connections, authentication, authorization and UI defaults.
//
// It imports no other internal package. Validation failures (duplicate
// connection names, unknown roles, invalid globs) are returned as errors so the
// binary refuses to start.
package config
