// asynqui client script: htmx configuration, theme and auto-refresh
// controls, visibility-gated polling, local times, copy buttons, the
// client-side filters, row selection (with "select all N" for tasks),
// confirmation dialogs, the Find task dialog and toasts.
// Loaded with defer after htmx, so htmx exists and the config below is
// applied before htmx initialises on DOMContentLoaded.
// Everything here is progressive enhancement: pages read fine without it.
(function () {
	"use strict";

	var root = document.documentElement;
	var cookiePath = root.getAttribute("data-base") || "/";
	root.classList.add("js"); // hides the no-JS fallbacks ([data-nojs])

	// htmx must never eval, run swapped-in scripts or inject an inline
	// <style> (the CSP forbids all three).
	// historyCacheSize 0: no page snapshots (with data) in localStorage.
	if (window.htmx) {
		htmx.config.allowEval = false;
		htmx.config.allowScriptTags = false;
		htmx.config.includeIndicatorStyles = false;
		htmx.config.historyCacheSize = 0;
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
	// <button data-copy="text" hidden> copies its value: the full ID, never
	// the shortened text shown. <button data-copy-from="#id"> copies that
	// element's text (a code block), so large text is not repeated in an
	// attribute. Copy buttons are rendered hidden and shown only where the
	// Clipboard API exists (secure contexts), so no button does nothing.
	function revealCopy(scope) {
		if (!navigator.clipboard) {
			return;
		}
		var buttons = scope.querySelectorAll("[data-copy][hidden], [data-copy-from][hidden]");
		for (var i = 0; i < buttons.length; i++) {
			buttons[i].hidden = false;
		}
	}

	document.addEventListener("click", function (e) {
		var b = e.target.closest("[data-copy], [data-copy-from]");
		if (!b || !navigator.clipboard) {
			return;
		}
		e.preventDefault();
		var text = b.getAttribute("data-copy");
		if (b.hasAttribute("data-copy-from")) {
			var src = document.querySelector(b.getAttribute("data-copy-from"));
			text = src ? src.textContent : "";
		}
		navigator.clipboard.writeText(text).then(function () {
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
			updateSelections();
		}
	});

	// ---- row selection ----
	// Row checkboxes belong to a form by their form attribute and sit in a
	// <tr data-state>. A [data-toolbar=form-id] bar shows "N selected"
	// ([data-sel-label], count in [data-sel-count]) and enables its
	// buttons: data-enable="any" when a row is selected, "state" when a
	// selected row's data-state equals the button's data-state, "every"
	// when every selected row has the attribute data-{data-flag} (else
	// the button's title is its data-blocked-title). Buttons
	// rendered disabled for the viewer's role have no data-enable and stay
	// disabled. The header [data-select-all=form-id] checkbox selects the
	// visible rows.
	//
	// The checkboxes carry hx-preserve, so htmx keeps them (and their
	// checked state) when a poll or a sort replaces the table; this only
	// recomputes the toolbar afterwards.
	function rowBoxes(formId) {
		var out = [];
		var boxes = document.querySelectorAll("input[type=checkbox][name]");
		for (var i = 0; i < boxes.length; i++) {
			if (boxes[i].getAttribute("form") === formId) {
				out.push(boxes[i]);
			}
		}
		return out;
	}

	function visible(el) {
		var tr = el.closest("tr");
		return !tr || !tr.hidden;
	}

	function updateSelection(bar) {
		var formId = bar.getAttribute("data-toolbar");
		var boxes = rowBoxes(formId);
		var n = 0, states = {}, shown = 0, shownChecked = 0, rows = [];
		for (var i = 0; i < boxes.length; i++) {
			var b = boxes[i], tr = b.closest("tr");
			if (visible(b)) {
				shown++;
			}
			if (!b.checked) {
				continue;
			}
			n++;
			if (visible(b)) {
				shownChecked++;
			}
			if (tr && tr.getAttribute("data-state")) {
				states[tr.getAttribute("data-state")] = true;
			}
			rows.push(tr);
		}
		var everything = syncEverything(formId, shown, shownChecked);
		var label = bar.querySelector("[data-sel-label]");
		if (label) {
			label.hidden = n === 0;
			label.querySelector("[data-sel-count]").textContent = everything || String(n);
			var allWord = label.querySelector("[data-sel-all]");
			if (allWord) {
				allWord.hidden = !everything;
			}
		}
		var buttons = bar.querySelectorAll("button[data-enable]");
		for (var j = 0; j < buttons.length; j++) {
			var btn = buttons[j], rule = btn.getAttribute("data-enable");
			var blocked = rule === "every" && !everyFlag(rows, btn.getAttribute("data-flag"));
			btn.disabled = n === 0 || blocked || (rule === "state" && !states[btn.getAttribute("data-state")]);
			if (rule === "every") {
				if (blocked) {
					btn.title = btn.getAttribute("data-blocked-title") || "";
				} else {
					btn.removeAttribute("title");
				}
			}
		}
		var all = document.querySelectorAll("input[data-select-all]");
		for (var k = 0; k < all.length; k++) {
			if (all[k].getAttribute("data-select-all") === formId) {
				all[k].checked = shown > 0 && shownChecked === shown;
				all[k].indeterminate = shownChecked > 0 && shownChecked < shown;
			}
		}
	}

	// everyFlag reports whether every row has the attribute data-{flag}.
	function everyFlag(rows, flag) {
		for (var i = 0; i < rows.length; i++) {
			if (!rows[i] || !rows[i].hasAttribute("data-" + flag)) {
				return false;
			}
		}
		return true;
	}

	function updateSelections() {
		var bars = document.querySelectorAll("[data-toolbar]");
		for (var i = 0; i < bars.length; i++) {
			updateSelection(bars[i]);
		}
	}

	document.addEventListener("change", function (e) {
		var t = e.target;
		if (t.matches("input[data-select-all]")) {
			var boxes = rowBoxes(t.getAttribute("data-select-all"));
			for (var i = 0; i < boxes.length; i++) {
				if (visible(boxes[i])) {
					boxes[i].checked = t.checked;
				}
			}
		}
		if (t.matches("input[type=checkbox]")) {
			updateSelections();
		}
	});

	// ---- confirmation dialogs ----
	// A button with data-confirm="dialog-id" opens that <dialog> instead of
	// submitting (without JS it submits, and the server asks on a page).
	// [data-sel-names] in the dialog lists the selected rows' data-label
	// (the value with bidirectional controls made visible), or values.
	// [data-dialog-close] closes it; so does its confirm button once htmx
	// has sent the request.
	document.addEventListener("click", function (e) {
		var opener = e.target.closest("button[data-confirm]");
		if (opener) {
			var dlg = document.getElementById(opener.getAttribute("data-confirm"));
			if (!dlg || typeof dlg.showModal !== "function") {
				return; // no <dialog> support: post, and confirm on a page
			}
			e.preventDefault();
			var list = dlg.querySelector("[data-sel-names]");
			if (list) {
				list.textContent = "";
				var boxes = rowBoxes(opener.getAttribute("form"));
				for (var i = 0; i < boxes.length; i++) {
					if (boxes[i].checked) {
						var li = document.createElement("li");
						li.textContent = boxes[i].getAttribute("data-label") || boxes[i].value;
						list.appendChild(li);
					}
				}
			}
			dlg.showModal();
			return;
		}
		var closer = e.target.closest("dialog [data-dialog-close]");
		if (closer) {
			closer.closest("dialog").close();
			return;
		}
		// Only a confirmation dialog's own confirm button: the Find task
		// dialog stays open on submit so a miss can show inline.
		var confirm = e.target.closest("dialog button[type=submit][name=confirm]");
		if (confirm) {
			var d = confirm.closest("dialog");
			setTimeout(function () { d.close(); }, 0);
		}
	});

	// ---- task tables: "select all N" ----
	// The [data-select-banner=form-id] banner of a task table shows once
	// every row of the page is selected. Its [data-select-everything]
	// button sets the form's [data-everything] field to "1": the toolbar
	// then counts the banner's data-total, and a button with
	// data-confirm-all opens that dialog (the *All* actions) instead of
	// acting on the rows. Unchecking a row or [data-clear-everything]
	// leaves the mode. syncEverything runs on every selection update and
	// returns the "all" count while the mode is on.
	function byForm(selector, attr, formId) {
		var els = document.querySelectorAll(selector);
		for (var i = 0; i < els.length; i++) {
			if (els[i].getAttribute(attr) === formId) {
				return els[i];
			}
		}
		return null;
	}

	function syncEverything(formId, shown, shownChecked) {
		var input = byForm("input[data-everything]", "data-everything", formId);
		if (!input) {
			return "";
		}
		var banner = byForm("[data-select-banner]", "data-select-banner", formId);
		var pageAll = shown > 0 && shownChecked === shown;
		// While an "all" dialog is open the mode stays, whatever a poll
		// did to the page: its confirm button posts all=1. Closing the
		// dialog updates the selection again.
		if ((!banner || !pageAll) && !allDialogOpen(formId)) {
			input.value = "";
		}
		if (!banner) {
			return "";
		}
		var on = input.value === "1";
		banner.hidden = !pageAll;
		var parts = banner.querySelectorAll("[data-banner-page], [data-banner-all]");
		for (var i = 0; i < parts.length; i++) {
			parts[i].hidden = parts[i].hasAttribute("data-banner-page") === on;
		}
		return on ? banner.getAttribute("data-total") : "";
	}

	// allDialogOpen reports whether a data-confirm-all dialog of the form
	// is open.
	function allDialogOpen(formId) {
		var buttons = document.querySelectorAll("button[data-confirm-all]");
		for (var i = 0; i < buttons.length; i++) {
			if (buttons[i].getAttribute("form") !== formId) {
				continue;
			}
			var d = document.getElementById(buttons[i].getAttribute("data-confirm-all"));
			if (d && d.open) {
				return true;
			}
		}
		return false;
	}

	document.addEventListener("close", function () {
		updateSelections();
	}, true);

	// everythingTotal is the "all" count while the mode is on, else "".
	function everythingTotal(formId) {
		var input = byForm("input[data-everything]", "data-everything", formId);
		var banner = byForm("[data-select-banner]", "data-select-banner", formId);
		return input && banner && input.value === "1" ? banner.getAttribute("data-total") : "";
	}

	document.addEventListener("click", function (e) {
		var sel = e.target.closest("[data-select-everything]");
		var clear = e.target.closest("[data-clear-everything]");
		var formId, input, i;
		if (sel) {
			formId = sel.getAttribute("data-select-everything");
			input = byForm("input[data-everything]", "data-everything", formId);
			if (input) {
				input.value = "1";
				updateSelections();
				var back = byForm("[data-clear-everything]", "data-clear-everything", formId);
				if (back) {
					back.focus();
				}
			}
		} else if (clear) {
			formId = clear.getAttribute("data-clear-everything");
			var boxes = rowBoxes(formId);
			for (i = 0; i < boxes.length; i++) {
				boxes[i].checked = false;
			}
			updateSelections();
			var head = byForm("input[data-select-all]", "data-select-all", formId);
			if (head) {
				head.focus();
			}
		}
	});

	// In "all" mode a toolbar button opens its data-confirm-all dialog,
	// with the total filled in, before htmx or the delete dialog sees the
	// click (capture phase).
	document.addEventListener("click", function (e) {
		var btn = e.target.closest("button[data-confirm-all]");
		if (!btn || btn.disabled) {
			return;
		}
		var total = everythingTotal(btn.getAttribute("form"));
		var dlg = document.getElementById(btn.getAttribute("data-confirm-all"));
		if (!total || !dlg || typeof dlg.showModal !== "function") {
			return;
		}
		e.preventDefault();
		e.stopPropagation();
		var spans = dlg.querySelectorAll("[data-all-total]");
		for (var i = 0; i < spans.length; i++) {
			spans[i].textContent = total;
		}
		dlg.showModal();
	}, true);

	// ---- Find task dialog ----
	// <dialog data-find-opener="button-id"> opens from its
	// [data-dialog-open] link (a page without JS) or the "/" key outside
	// text fields, empty and with its field focused. Enter submits; Escape
	// closes it, and focus goes back to the button.
	function openFind(dlg) {
		if (!dlg || dlg.open || typeof dlg.showModal !== "function") {
			return false;
		}
		var input = dlg.querySelector("input[name=id]");
		if (input) {
			input.value = "";
			input.removeAttribute("aria-invalid");
			input.removeAttribute("aria-describedby");
		}
		var stale = dlg.querySelectorAll(".find-miss, .conn-error");
		for (var i = 0; i < stale.length; i++) {
			stale[i].remove();
		}
		dlg.showModal();
		if (input) {
			input.focus();
		}
		return true;
	}

	document.addEventListener("click", function (e) {
		var a = e.target.closest("[data-dialog-open]");
		if (a && openFind(document.getElementById(a.getAttribute("data-dialog-open")))) {
			e.preventDefault();
		}
	});

	document.addEventListener("keydown", function (e) {
		if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) {
			return;
		}
		var t = e.target;
		// Only text fields take "/": checkboxes, radios and buttons do not.
		var text = "textarea, select, [contenteditable], input:not([type=checkbox]):not([type=radio]):not([type=button]):not([type=submit]):not([type=reset])";
		if (t.closest && t.closest(text)) {
			return;
		}
		if (document.querySelector("dialog[open]")) {
			return;
		}
		if (openFind(document.querySelector("dialog[data-find-opener]"))) {
			e.preventDefault();
		}
	});

	// close does not bubble: listen in the capture phase.
	document.addEventListener("close", function (e) {
		var dlg = e.target;
		if (dlg.matches && dlg.matches("dialog[data-find-opener]")) {
			var b = document.getElementById(dlg.getAttribute("data-find-opener"));
			if (b) {
				b.focus();
			}
		}
	}, true);

	// ---- toasts ----
	// Toasts arrive in #toasts (out of band, or with the page). Each goes
	// away after a while or on its close button. A failed action request
	// with no answer to show gets the #toast-request-failed template.
	var toastMs = 8000;

	function armToasts() {
		var toasts = document.querySelectorAll("#toasts .toast:not([data-armed])");
		for (var i = 0; i < toasts.length; i++) {
			(function (t) {
				t.setAttribute("data-armed", "");
				setTimeout(function () { t.remove(); }, toastMs);
			})(toasts[i]);
		}
	}

	document.addEventListener("click", function (e) {
		var b = e.target.closest("[data-toast-close]");
		if (b) {
			b.closest(".toast").remove();
		}
	});

	function requestFailed(e) {
		var cfg = e.detail && e.detail.requestConfig;
		if (!cfg || cfg.verb === "get") {
			return; // a failed poll is retried by the next one
		}
		var tpl = document.getElementById("toast-request-failed");
		var region = document.getElementById("toasts");
		if (tpl && region) {
			region.appendChild(tpl.content.cloneNode(true));
			armToasts();
		}
	}
	document.addEventListener("htmx:responseError", requestFailed);
	document.addEventListener("htmx:sendError", requestFailed);

	// ---- menus ----
	// <details data-menu> closes on an outside click or Escape. Escape
	// returns focus to the menu's summary when focus was inside the menu.
	function closeMenus(except, restoreFocus) {
		var open = document.querySelectorAll("details[data-menu][open]");
		for (var i = 0; i < open.length; i++) {
			if (open[i] === except) {
				continue;
			}
			var hadFocus = open[i].contains(document.activeElement);
			open[i].removeAttribute("open");
			if (restoreFocus && hadFocus) {
				open[i].querySelector("summary").focus();
			}
		}
	}

	document.addEventListener("click", function (e) {
		closeMenus(e.target.closest("details[data-menu]"), false);
	});
	document.addEventListener("keydown", function (e) {
		if (e.key === "Escape") {
			closeMenus(null, true);
		}
	});

	// ---- after htmx swaps (including out-of-band ones) ----
	// Right after each swap, synchronously: re-apply the filters (so
	// filtered-out rows never flash back), recompute the toolbars, and give
	// focus back to the element that had it. An hx-preserve'd checkbox is
	// the same element after the swap, but htmx takes it out of the
	// document while swapping, which drops its focus; htmx itself only
	// refocuses elements it replaced.
	var focusBefore = null;
	document.addEventListener("htmx:beforeSwap", function () {
		focusBefore = document.activeElement;
	});

	function restoreFocus() {
		var f = focusBefore, now = document.activeElement;
		if (f && f !== document.body && f.isConnected && (!now || now === document.body)) {
			f.focus({ preventScroll: true });
		}
	}

	function applyFilters() {
		var filters = document.querySelectorAll("input[data-filter]");
		for (var i = 0; i < filters.length; i++) {
			applyFilter(filters[i]);
		}
	}

	function syncAfterSwap() {
		applyFilters();
		updateSelections();
		restoreFocus();
	}
	document.addEventListener("htmx:oobAfterSwap", syncAfterSwap);
	document.addEventListener("htmx:afterSwap", syncAfterSwap);

	// Then, once per batch of swaps: times, copy buttons, toasts.
	var afterSwapQueued = false;
	function afterSwap() {
		restoreFocus();
		if (afterSwapQueued) {
			return;
		}
		afterSwapQueued = true;
		setTimeout(function () {
			afterSwapQueued = false;
			renderTimes(document);
			revealCopy(document);
			armToasts();
		}, 0);
	}
	document.addEventListener("htmx:afterSettle", afterSwap);
	document.addEventListener("htmx:oobAfterSwap", afterSwap);

	renderTimes(document);
	revealCopy(document);
	updateSelections();
	armToasts();
	schedule();
})();
