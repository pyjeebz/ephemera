// What every page of the relay's web app shares: the light/dark theme,
// the header and footer, Lucide icons, the mayfly mark, toasts, dialogs
// and copy buttons. Loaded in <head> so the saved theme applies before
// the first paint; the rest waits for the document.
"use strict";
(() => {
	const root = document.documentElement;
	const THEME_KEY = "ephemera.theme";
	const store = {
		get(k) { try { return localStorage.getItem(k) || ""; } catch { return ""; } },
		set(k, v) { try { v ? localStorage.setItem(k, v) : localStorage.removeItem(k); } catch {} },
	};
	const saved = store.get(THEME_KEY);
	if (saved === "light" || saved === "dark") root.dataset.theme = saved;
	const isDark = () => root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;

	// Lucide icons (ISC licence), the inner markup of each 24×24 glyph.
	const ICONS = {
		"arrow-up": '<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>',
		"arrow-up-right": '<path d="M7 7h10v10"/><path d="M7 17 17 7"/>',
		"bell": '<path d="M10.268 21a2 2 0 0 0 3.464 0"/><path d="M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326"/>',
		"check": '<path d="M20 6 9 17l-5-5"/>',
		"circle-check": '<circle cx="12" cy="12" r="10"/><path d="m16 9-5.5 5.5L8 12"/>',
		"circle-slash": '<circle cx="12" cy="12" r="10"/><line x1="9" x2="15" y1="15" y2="9"/>',
		"code-xml": '<path d="m18 16 4-4-4-4"/><path d="m6 8-4 4 4 4"/><path d="m14.5 4-5 16"/>',
		"copy": '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
		"download": '<path d="M12 15V3"/><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5 5 5-5"/>',
		"file": '<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/>',
		"file-archive": '<path d="M13.659 22H18a2 2 0 0 0 2-2V8a2.4 2.4 0 0 0-.706-1.706l-3.588-3.588A2.4 2.4 0 0 0 14 2H6a2 2 0 0 0-2 2v11.5"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/><path d="M8 12v-1"/><path d="M8 18v-2"/><path d="M8 7V6"/><circle cx="8" cy="20" r="2"/>',
		"file-audio": '<path d="M4 6.835V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.706.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2h-.343"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/><path d="M2 19a2 2 0 0 1 4 0v1a2 2 0 0 1-4 0v-4a6 6 0 0 1 12 0v4a2 2 0 0 1-4 0v-1a2 2 0 0 1 4 0"/>',
		"file-question": '<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M12 17h.01"/><path d="M9.1 9a3 3 0 0 1 5.82 1c0 2-3 3-3 3"/>',
		"file-text": '<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/><path d="M10 9H8"/><path d="M16 13H8"/><path d="M16 17H8"/>',
		"file-video": '<path d="M4 12V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.706.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/><path d="m10 17.843 3.033-1.755a.64.64 0 0 1 .967.56v4.704a.65.65 0 0 1-.967.56L10 20.157"/><rect width="7" height="6" x="3" y="16" rx="1"/>',
		"flag": '<path d="M4 22V4a1 1 0 0 1 .4-.8A6 6 0 0 1 8 2c3 0 5 2 7.333 2q2 0 3.067-.8A1 1 0 0 1 20 4v10a1 1 0 0 1-.4.8A6 6 0 0 1 16 16c-3 0-5-2-8-2a6 6 0 0 0-4 1.528"/>',
		"hourglass": '<path d="M5 22h14"/><path d="M5 2h14"/><path d="M17 22v-4.172a2 2 0 0 0-.586-1.414L12 12l-4.414 4.414A2 2 0 0 0 7 17.828V22"/><path d="M7 2v4.172a2 2 0 0 0 .586 1.414L12 12l4.414-4.414A2 2 0 0 0 17 6.172V2"/>',
		"key-round": '<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/>',
		"laptop": '<path d="M18 5a2 2 0 0 1 2 2v8.526a2 2 0 0 0 .212.897l1.068 2.127a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45l1.068-2.127A2 2 0 0 0 4 15.526V7a2 2 0 0 1 2-2z"/><path d="M20.054 15.987H3.946"/>',
		"loader": '<path d="M21 12a9 9 0 1 1-6.219-8.56"/>',
		"monitor-play": '<path d="M15.033 9.44a.647.647 0 0 1 0 1.12l-4.065 2.352a.645.645 0 0 1-.968-.56V7.648a.645.645 0 0 1 .967-.56z"/><path d="M12 17v4"/><path d="M8 21h8"/><rect x="2" y="3" width="20" height="14" rx="2"/>',
		"moon": '<path d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"/>',
		"octagon-x": '<path d="m15 9-6 6"/><path d="M2.586 16.726A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2h6.624a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586z"/><path d="m9 9 6 6"/>',
		"play": '<path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z"/>',
		"rotate-ccw": '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/>',
		"server": '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
		"sun": '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>',
		"terminal": '<path d="M12 19h8"/><path d="m4 17 6-6-6-6"/>',
		"timer": '<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>',
		"triangle-alert": '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
		"wifi-off": '<path d="M12 20h.01"/><path d="M8.5 16.429a5 5 0 0 1 7 0"/><path d="M5 12.859a10 10 0 0 1 5.17-2.69"/><path d="M19 12.859a10 10 0 0 0-2.007-1.523"/><path d="M2 8.82a15 15 0 0 1 4.177-2.643"/><path d="M22 8.82a15 15 0 0 0-11.288-3.764"/><path d="m2 2 20 20"/>',
		"x": '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
	};

	const SVG = "http://www.w3.org/2000/svg";
	function icon(name, cls) {
		const s = document.createElementNS(SVG, "svg");
		s.setAttribute("viewBox", "0 0 24 24");
		s.setAttribute("aria-hidden", "true");
		s.setAttribute("class", "icon" + (cls ? " " + cls : ""));
		s.innerHTML = ICONS[name] || "";
		return s;
	}

	// The mark: a mayfly (Ephemeroptera), which lives about a day, as
	// long as the longest link. Side profile at rest: back-swept forewing
	// on a narrow root, up-curved abdomen, two tail filaments, forelegs
	// held forward. data-flying animates it while bytes move.
	const STROKES = {
		regular: [1, 1.6, 2.4],
		medium: [1.4, 2, 2.8], // the 20-28px lockup: legs stay, lines thicken
		heavy: [1.8, 2.4, 3.2], // 24px and below: no legs, so the silhouette holds
	};
	function mayfly(weight = "regular", flying = false, title = "") {
		const [fine, abdomen, thorax] = STROKES[weight] || STROKES.regular;
		const s = document.createElementNS(SVG, "svg");
		s.setAttribute("viewBox", "0 0 32 32");
		s.setAttribute("overflow", "visible");
		s.setAttribute("class", "mayfly");
		s.dataset.flying = String(flying);
		if (title) { s.setAttribute("role", "img"); s.setAttribute("aria-label", title); } else s.setAttribute("aria-hidden", "true");
		const legs = weight === "heavy" ? "" :
			`<path class="mayfly-leg mayfly-leg-a" d="M10.6 22.6 L9.6 25.4" stroke-width="${fine}"/>` +
			`<path class="mayfly-leg mayfly-leg-b" d="M12.8 22.6 L13.6 25.4" stroke-width="${fine}"/>`;
		s.innerHTML =
			`<g class="mayfly-trail" fill="currentColor"><circle cx="25.6" cy="6.6" r="0.7"/><circle cx="30.8" cy="12.5" r="0.7"/><circle cx="28.4" cy="9.4" r="0.55"/></g>` +
			`<g class="mayfly-body">` +
			`<g class="mayfly-tails" fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="${fine}"><path d="M21 16.8 C22.8 13.8 24.2 10.8 25.2 7.2"/><path d="M21 16.8 C23.8 14.6 27 13.2 30.4 12.6"/></g>` +
			`<g fill="none" stroke="currentColor" stroke-linecap="round">` +
			`<path d="M12.6 21.9 C16 21.6 18.8 20 21 16.8" stroke-width="${abdomen}"/>` +
			`<path d="M8.8 21.6 C10.5 21.9 11.8 22 12.8 21.9" stroke-width="${thorax}"/>` +
			`<path d="M8.2 20.4 L5 16.6" stroke-width="${fine}"/>${legs}</g>` +
			`<g fill="currentColor"><circle cx="7.2" cy="21.4" r="1.8"/>` +
			`<path class="mayfly-hindwing" d="M12.8 21 C14.4 18.6 16.6 17.2 18.6 17.4 C17.8 19.6 15.8 21.2 13.2 21.8 Z"/>` +
			`<path class="mayfly-wing" d="M10.2 20.8 C10.6 14.6 13 9.4 17.4 6 C18.8 11.2 17.4 16.6 12.6 21.4 Z"/></g>` +
			`</g>`;
		return s;
	}

	const SOURCE = "https://github.com/pyjeebz/ephemera";

	const HEADER = `<div class="container">
	<a class="btn btn-ghost btn-sm home tip-start" href="/" aria-label="Ephemera home" data-tip=""><span class="wordmark"><i data-mark="medium"></i>ephemera</span></a>
	<nav>
		<a class="btn btn-ghost btn-sm muted wide-only" href="/#how">How it works</a>
		<button class="btn btn-ghost btn-sm muted" type="button" data-dialog="cli-dialog"><i data-icon="terminal"></i>CLI</button>
		<a class="btn btn-ghost btn-icon-sm muted" href="${SOURCE}" target="_blank" rel="noreferrer" aria-label="Source code" data-tip="Source code · MIT"><i data-icon="code-xml"></i></a>
		<button class="btn btn-ghost btn-icon-sm muted" type="button" data-theme-toggle></button>
	</nav>
</div>`;

	const FOOTER = `<div class="container">
	<div class="about"><span class="wordmark"><i data-mark="medium"></i>ephemera</span>
		<span><span class="mono">${location.host}</span> is a relay. It passes bytes along and drops them at expiry.</span></div>
	<div class="links"><a href="/brand">Brand</a><a href="${SOURCE}" target="_blank" rel="noreferrer">Source code · MIT</a></div>
</div>`;

	// The terminal sample: real commands and what they print.
	const CLI = `<div class="terminal">
	<div class="terminal-bar"><span class="xs muted">Terminal</span>
		<button class="btn btn-ghost btn-icon-xs" type="button" aria-label="Copy command" data-copy="ephemera send -ttl 21600 final-cut.mp4" data-toast="Command copied"><i data-icon="copy"></i></button></div>
	<pre><span class="prompt">$ </span><span class="cmd">ephemera login</span>
sender token: ••••••••
<span class="prompt">$ </span><span class="cmd">ephemera send -ttl 21600 final-cut.mp4</span>
<span class="link">https://${location.host}/k3Jx9qTzR2vWbE</span>
uploaded 18%: 412 MB of 2310 MB at 11 MB/s</pre>
</div>`;

	const CLI_DIALOG = `<dialog class="dialog wide" id="cli-dialog" aria-labelledby="cli-title">
	<div class="dialog-header">
		<h2 class="dialog-title" id="cli-title">Send from the terminal</h2>
		<p class="dialog-description">The <span class="mono fg">ephemera</span> command does what this page does. The link prints on stdout, so scripts and agents can pipe it.</p>
	</div>
	<div data-cli></div>
	<button class="btn btn-ghost btn-icon-sm dialog-close" type="button" data-close aria-label="Close"><i data-icon="x"></i></button>
</dialog>`;

	// toast shows a short note at the top of the page for a few seconds.
	function toast(title, { description = "", kind = "success" } = {}) {
		let box = document.querySelector(".toasts");
		if (!box) {
			box = Object.assign(document.createElement("div"), { className: "toasts" });
			box.setAttribute("role", "status");
			box.setAttribute("aria-live", "polite");
			document.body.append(box);
		}
		const t = Object.assign(document.createElement("div"), { className: "toast" });
		const text = document.createElement("div");
		text.append(Object.assign(document.createElement("div"), { className: "toast-title", textContent: title }));
		if (description) text.append(Object.assign(document.createElement("div"), { className: "toast-description", textContent: description }));
		if (kind) t.append(icon(kind === "error" ? "octagon-x" : "circle-check"));
		t.append(text);
		box.append(t);
		setTimeout(() => { t.classList.add("out"); setTimeout(() => t.remove(), 200); }, 3000);
	}

	// copy puts text on the clipboard, flips the button to "Copied" for a
	// moment and says so in a toast.
	async function copy(text, btn, note = "Link copied") {
		try { await navigator.clipboard.writeText(text); } catch {
			toast("Couldn't copy", { description: text, kind: "error" });
			return;
		}
		toast(note);
		if (!btn) return;
		const svg = btn.querySelector("svg.icon");
		const label = btn.querySelector("[data-label]");
		svg?.replaceWith(icon("check"));
		if (label) label.textContent = "Copied";
		clearTimeout(btn.copyTimer);
		btn.copyTimer = setTimeout(() => {
			btn.querySelector("svg.icon")?.replaceWith(icon("copy"));
			if (label) label.textContent = label.dataset.label;
		}, 1600);
	}

	// hydrate turns placeholders into icons, marks and the CLI block.
	function hydrate(scope) {
		for (const el of scope.querySelectorAll("[data-cli]")) { el.outerHTML = CLI; }
		for (const el of scope.querySelectorAll("i[data-icon]")) el.replaceWith(icon(el.dataset.icon, el.className));
		for (const el of scope.querySelectorAll("i[data-mark]")) {
			const m = mayfly(el.dataset.mark);
			if (el.className) m.classList.add(...el.className.split(" "));
			if (el.id) m.id = el.id;
			el.replaceWith(m);
		}
	}

	function themeButton(btn) {
		const dark = isDark();
		btn.replaceChildren(icon(dark ? "sun" : "moon"));
		btn.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
	}

	// fly sets the header mark in flight while files are moving.
	function fly(n) {
		const home = document.querySelector(".site-header .home");
		if (!home) return;
		home.querySelector(".mayfly").dataset.flying = String(n > 0);
		home.dataset.tip = n > 0 ? (n === 1 ? "1 file in flight" : `${n} files in flight`) : "";
	}

	function start() {
		const header = document.querySelector("header.site-header");
		if (header) header.innerHTML = HEADER;
		const footer = document.querySelector("footer.site-footer");
		if (footer) footer.innerHTML = FOOTER;
		if (header) document.body.insertAdjacentHTML("beforeend", CLI_DIALOG);
		hydrate(document);

		for (const btn of document.querySelectorAll("[data-theme-toggle]")) {
			themeButton(btn);
			btn.onclick = () => {
				root.dataset.theme = isDark() ? "light" : "dark";
				store.set(THEME_KEY, root.dataset.theme);
				document.querySelectorAll("[data-theme-toggle]").forEach(themeButton);
			};
		}
		matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
			document.querySelectorAll("[data-theme-toggle]").forEach(themeButton);
		});

		// dialogs: [data-dialog=id] opens one; [data-close] or a click on
		// the backdrop closes it
		document.addEventListener("click", (e) => {
			const opener = e.target.closest("[data-dialog]");
			if (opener) { document.getElementById(opener.dataset.dialog)?.showModal(); return; }
			const closer = e.target.closest("[data-close]");
			if (closer) { closer.closest("dialog")?.close(); return; }
			if (e.target instanceof HTMLDialogElement) {
				// the dialog's own padding is the dialog too: only a click
				// outside its box is on the backdrop
				const r = e.target.getBoundingClientRect();
				if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) e.target.close();
				return;
			}
			const copier = e.target.closest("[data-copy]");
			if (copier) copy(copier.dataset.copy, copier, copier.dataset.toast);
		});
	}

	window.ui = { icon, mayfly, toast, copy, fly, store };
	if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
	else start();
})();
