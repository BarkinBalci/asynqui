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
// whose DBs 0 to 2 they flush: ASYNQUI_E2E_REDIS=host:port, or
// testcontainers when it is unset. Every test runs against one realistic
// fixture (fixture_test.go).
//
// Each page records a video into test-results/, and a failed test also
// saves its trace there. ASYNQUI_E2E_PACE=1 slows the run down and draws
// the cursor, ASYNQUI_E2E_HEADED=1 shows the browser, and
// ASYNQUI_E2E_GIF=<subtest> writes that subtest as the README's
// docs/images/demo.gif. See docs/contributing.md.
package e2e
