// Package e2e holds asynqui's browser smoke tests: the behavior that lives
// in app.js and htmx (polling, focus, selection across polls, dialogs,
// copy, theme) and that Go handler and component tests cannot run.
//
// The tests are behind the e2e build tag, so go test ./... stays fast and
// needs no browser:
//
//	go test -tags e2e ./e2e/...
//
// They need Chromium installed for playwright-go and a throwaway Redis,
// which they flush: ASYNQUI_E2E_REDIS=host:port, or testcontainers when it
// is unset. See docs/contributing.md.
package e2e
