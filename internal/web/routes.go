package web

import (
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/conn"

	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// addRoutes registers every route with the role it needs (see
// docs/architecture.md, "Routes"). Paths are relative to base_path; NewServer
// mounts the mux under it.
func addRoutes(mux *http.ServeMux, d *deps) {
	// probes (no auth, not logged)
	mux.Handle("GET /health/liveness", handleLiveness())
	mux.Handle("GET /health/readiness", handleReadiness(d.ready))

	// auth
	mux.Handle("GET /login", handleLogin(d))
	mux.Handle("POST /login", d.auth.LoginHandler(d.loginFailed))  // basic form
	mux.Handle("GET /login/sso", d.auth.SSOHandler(d.loginFailed)) // oidc "Sign in with SSO" link: starts the flow
	mux.Handle("GET /auth/callback", d.auth.CallbackHandler(d.loginFailed))
	mux.Handle("POST /logout", d.auth.LogoutHandler())

	// preferences (no-JS fallback of the theme and refresh controls; no auth)
	mux.Handle("POST /prefs", handlePrefs(d))

	// pages (viewer)
	mux.Handle("GET /{$}", view(d, handleOverview(d)))
	mux.Handle("GET /c/{conn}", view(d, handleQueues(d)))                         // ?sort=size&dir=desc&range=14d&queue=
	mux.Handle("GET /c/{conn}/queues/{queue}", view(d, handleQueue(d)))           // ?state=pending&page=2&size=20
	mux.Handle("GET /c/{conn}/queues/{queue}/find", view(d, handleFindTask(d)))   // ?id=; the Find task lookup, a page without JS
	mux.Handle("GET /c/{conn}/queues/{queue}/tasks/{id}", view(d, handleTask(d))) // ?payload=raw
	mux.Handle("GET /c/{conn}/servers", view(d, handleServers(d)))                // ?sort=busy&dir=desc
	mux.Handle("GET /c/{conn}/servers/{id}", view(d, handleServer(d)))            // ?sort=running&dir=desc
	mux.Handle("GET /c/{conn}/schedulers", view(d, handleSchedulers(d)))          // ?sort=next&dir=asc
	mux.Handle("GET /c/{conn}/schedulers/{id}", view(d, handleSchedulerEntry(d))) // ?page=2
	mux.Handle("GET /c/{conn}/info", view(d, handleInfo(d)))
	mux.Handle("POST /c/{conn}/recheck", view(d, handleRecheck(d))) // Overview "Retry now"

	// actions (operator)
	mux.Handle("POST /c/{conn}/queues/pause", op(d, handleQueueAction(d, pages.VerbPause))) // form: queue=a&queue=b&next=
	mux.Handle("POST /c/{conn}/queues/resume", op(d, handleQueueAction(d, pages.VerbResume)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/run", op(d, handleTaskAction(d, conn.TaskRun))) // form: id=a&id=b or all=1&state=, next=, from=task
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/archive", op(d, handleTaskAction(d, conn.TaskArchive)))
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/cancel", op(d, handleTaskAction(d, conn.TaskCancel))) // ids only: no "all"

	// admin
	mux.Handle("POST /c/{conn}/queues/{queue}/tasks/delete", admin(d, handleTaskAction(d, conn.TaskDelete))) // + confirm=1
	mux.Handle("POST /c/{conn}/queues/delete", admin(d, handleQueueAction(d, pages.VerbDelete)))             // + confirm=1

	// assets
	mux.Handle("GET /static/", http.StripPrefix("/static", d.assets))

	// everything else
	mux.Handle("/", handleNotFound(d))
}
