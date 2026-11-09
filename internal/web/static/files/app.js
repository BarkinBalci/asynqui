// asynqui client script: htmx configuration, theme and auto-refresh
// controls, visibility-gated polling, local times, copy buttons and the
// sidebar filter. Loaded with defer after htmx, so htmx exists and the
// config below is applied before htmx initialises on DOMContentLoaded.
// Everything here is progressive enhancement: pages read fine without it.
(function () {
	"use strict";

	var root = document.documentElement;
	var cookiePath = root.getAttribute("data-base") || "/";

	// htmx must never eval, run swapped-in scripts or inject an inline
	// <style> (the CSP forbids all three).
	if (window.htmx) {
		htmx.config.allowEval = false;
		htmx.config.allowScriptTags = false;
		htmx.config.includeIndicatorStyles = false;
	}

	function setCookie(name, value) {
		var c = name + "=" + encodeURIComponent(value) + "; Path=" + cookiePath +
			"; Max-Age=31536000; SameSite=Lax";
		if (location.protocol === "https:") {
			c += "; Secure";
		}
		document.cookie = c;
	}

	// ---- preference controls (theme, auto refresh) ----
	// Each is a <form data-pref method="post"> of submit buttons, so it
	// works without JS; with JS the choice applies without a reload.
	function press(form, button) {
		var buttons = form.querySelectorAll("button[name]");
		for (var i = 0; i < buttons.length; i++) {
			var on = buttons[i] === button;
			buttons[i].classList.toggle("on", on);
			buttons[i].setAttribute("aria-pressed", on ? "true" : "false");
		}
	}

	document.addEventListener("click", function (e) {
		var b = e.target.closest("form[data-pref] button[name]");
		if (!b) {
			return;
		}
		e.preventDefault();
		setCookie("asynqui_" + b.name, b.value);
		press(b.form, b);
		if (b.name === "theme") {
			root.setAttribute("data-theme", b.value);
		} else if (b.name === "refresh") {
			root.setAttribute("data-refresh", b.value);
			schedule();
		}
	});

	// ---- polling ----
	// Regions marked data-poll carry hx-trigger="asynqui-refresh". One timer
	// fires that event at the chosen interval; ticks are skipped while the
	// tab is hidden and one refresh fires when it becomes visible again.
	// With "off" no timer runs, so no polling requests are sent at all.
	var pollTimer = null;

	function intervalMs() {
		var n = parseInt(root.getAttribute("data-refresh"), 10); // "5s" → 5, "off" → NaN
		return n > 0 ? n * 1000 : 0;
	}

	function refresh() {
		if (document.hidden || !window.htmx) {
			return;
		}
		var regions = document.querySelectorAll("[data-poll]");
		for (var i = 0; i < regions.length; i++) {
			htmx.trigger(regions[i], "asynqui-refresh");
		}
	}

	function schedule() {
		if (pollTimer) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
		var ms = intervalMs();
		if (ms > 0) {
			pollTimer = setInterval(refresh, ms);
		}
	}

	document.addEventListener("visibilitychange", function () {
		if (document.hidden) {
			return;
		}
		renderTimes(document);
		if (intervalMs() > 0) {
			refresh();
			schedule(); // restart so the next tick is a full interval away
		}
	});

	// ---- times ----
	// <time datetime="RFC 3339"> holds the absolute UTC time as text. It is
	// rewritten to relative text ("3m ago", "in 2m 40s") with the absolute
	// local time in title, or to the absolute local time when
	// data-format="absolute".
	var absFormat = new Intl.DateTimeFormat(undefined, {
		month: "short", day: "numeric",
		hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23"
	});

	function pad2(n) {
		return n < 10 ? "0" + n : String(n);
	}

	// twoUnits mirrors format.Duration: "38s", "2m 40s", "3h 05m", "2d 4h".
	function twoUnits(secs) {
		var d = Math.floor(secs / 86400), h = Math.floor(secs % 86400 / 3600);
		var m = Math.floor(secs % 3600 / 60), s = secs % 60;
		if (d > 0) { return d + "d " + h + "h"; }
		if (h > 0) { return h + "h " + pad2(m) + "m"; }
		if (m > 0) { return m + "m " + pad2(s) + "s"; }
		return s + "s";
	}

	function oneUnit(secs) {
		if (secs < 60) { return secs + "s"; }
		if (secs < 3600) { return Math.floor(secs / 60) + "m"; }
		if (secs < 86400) { return Math.floor(secs / 3600) + "h"; }
		return Math.floor(secs / 86400) + "d";
	}

	function relative(date) {
		var secs = Math.round((Date.now() - date.getTime()) / 1000);
		if (secs >= -1) { // tolerate a little clock skew
			return oneUnit(Math.max(secs, 0)) + " ago";
		}
		return "in " + twoUnits(-secs);
	}

	function renderTimes(scope) {
		var els = scope.querySelectorAll("time[datetime]");
		for (var i = 0; i < els.length; i++) {
			var el = els[i];
			var d = new Date(el.getAttribute("datetime"));
			if (isNaN(d.getTime())) {
				continue;
			}
			var abs = absFormat.format(d);
			if (el.getAttribute("data-format") === "absolute") {
				el.textContent = abs;
			} else {
				el.textContent = relative(d);
				el.title = abs;
			}
		}
	}

	setInterval(function () {
		if (!document.hidden) {
			renderTimes(document);
		}
	}, 1000);

	// ---- copy buttons ----
	// <button data-copy="text"> copies its value; the full ID, never the
	// shortened text shown.
	document.addEventListener("click", function (e) {
		var b = e.target.closest("[data-copy]");
		if (!b || !navigator.clipboard) {
			return;
		}
		e.preventDefault();
		navigator.clipboard.writeText(b.getAttribute("data-copy")).then(function () {
			b.classList.add("copied");
			setTimeout(function () { b.classList.remove("copied"); }, 1500);
		});
	});

	// ---- client-side filters ----
	// <input data-filter="#list-id"> hides the list's [data-filter-text]
	// items that do not contain the typed text.
	function applyFilter(input) {
		var list = document.querySelector(input.getAttribute("data-filter"));
		if (!list) {
			return;
		}
		var q = input.value.trim().toLowerCase();
		var items = list.querySelectorAll("[data-filter-text]");
		for (var i = 0; i < items.length; i++) {
			var text = items[i].getAttribute("data-filter-text").toLowerCase();
			items[i].hidden = q !== "" && text.indexOf(q) < 0;
		}
	}

	document.addEventListener("input", function (e) {
		if (e.target.matches("input[data-filter]")) {
			applyFilter(e.target);
		}
	});

	// ---- menus ----
	// <details data-menu> closes on an outside click or Escape.
	function closeMenus(except) {
		var open = document.querySelectorAll("details[data-menu][open]");
		for (var i = 0; i < open.length; i++) {
			if (open[i] !== except) {
				open[i].removeAttribute("open");
			}
		}
	}

	document.addEventListener("click", function (e) {
		closeMenus(e.target.closest("details[data-menu]"));
	});
	document.addEventListener("keydown", function (e) {
		if (e.key === "Escape") {
			closeMenus(null);
		}
	});

	// ---- after htmx swaps ----
	document.addEventListener("htmx:afterSettle", function () {
		renderTimes(document);
		var filters = document.querySelectorAll("input[data-filter]");
		for (var i = 0; i < filters.length; i++) {
			applyFilter(filters[i]);
		}
	});

	renderTimes(document);
	schedule();
})();
