// Package web is the HTTP layer: NewServer wires dependencies into an
// http.Handler, routes.go lists every URL, and handle_*.go files hold the
// handlers. Handlers fetch through package conn, build view models and render
// templ components from package views.
package web
