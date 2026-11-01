// The page a link opens. It polls /:id/status, shows the file in the
// state it's in (live from the sender, available on the relay, offline,
// expired), plays media in the browser's own player, and reports how
// playback went when the page is hidden or closed.
"use strict";
(() => {
	const id = location.pathname.split("/").filter(Boolean)[0] || "";
	const $ = (s) => document.querySelector(s);
	const nameEl = $("#name"), metaEl = $("#meta"), badgeEl = $("#badge"), stateEl = $("#state"), stageEl = $("#stage");
	const downloadEl = $("#download"), copyEl = $("#copy");

	const STREAMABLE = ["video/mp4", "video/webm", "video/quicktime", "audio/mpeg", "audio/mp4", "audio/wav"];
	// link lifetimes the web sender offers: the time-left bar measures
	// against the shortest one that covers what's left, since the relay
	// only knows when a link ends (a redial restarts its clock)
	const LIFETIMES = [3600, 21600, 86400];

	let stage = "";        // what the stage shows: "player", "replay", "waiting", "watching", "none"
	let countdownTimer = null;
	let lastState = "";
	let pollFast = false;
	let watching = false;  // "tell me when it's back" was pressed
	let lifetime = 0;

	function sizeText(n) {
		if (n < 1e6) return (n / 1e3).toFixed(0) + " KB";
		if (n < 1e9) return (n / 1e6).toFixed(n < 1e7 ? 1 : 0) + " MB";
		return (n / 1e9).toFixed(2) + " GB";
	}

	function typeText(mime) {
		if (!mime) return "";
		const [kind, sub] = mime.split(";")[0].split("/");
		if (["video", "audio", "image"].includes(kind)) return `${sub.toUpperCase()} ${kind}`;
		if (mime === "application/pdf") return "PDF";
		if (mime === "application/octet-stream") return "File";
		return mime.split(";")[0];
	}

	function leftText(s) {
		const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60);
		if (h) return `${h}h ${m}m`;
		if (m) return `${m}m ${Math.floor(s % 60)}s`;
		return `${Math.floor(s)}s`;
	}

	function showExpiry(expires) {
		if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
		$("#life").hidden = !expires;
		if (!expires) return;
		const tick = () => {
			const left = expires - Date.now() / 1000;
			if (left <= 0) { clearInterval(countdownTimer); countdownTimer = null; poll(); return; }
			if (!lifetime) lifetime = LIFETIMES.find((t) => t >= left) || left;
			const pct = Math.round(100 * Math.min(1, left / lifetime));
			$("#left").textContent = leftText(left);
			$("#life-pct").textContent = pct + "%";
			const bar = $("#life .progress");
			bar.firstElementChild.style.transform = `scaleX(${pct / 100})`;
			bar.setAttribute("aria-valuenow", pct);
		};
		tick();
		countdownTimer = setInterval(tick, 1000);
	}

	function badge(st) {
		const b = document.createElement("span");
		if (st.state === "live") {
			b.className = "badge badge-live";
			b.append(Object.assign(document.createElement("span"), { className: "dot" }), "Live");
		} else if (st.state === "available") {
			b.className = "badge badge-secondary";
			b.append(ui.icon("check"), "Ready");
		} else {
			b.className = "badge badge-warning";
			b.append(ui.icon("wifi-off"), "Sender offline");
		}
		return b;
	}

	function canDownload(st, on) {
		downloadEl.setAttribute("aria-disabled", String(!on));
		if (on) {
			downloadEl.href = `/${id}/file`;
			downloadEl.download = st.name || id;
		} else {
			downloadEl.removeAttribute("href");
		}
	}

	function player(mime) {
		const v = document.createElement(mime.startsWith("audio/") ? "audio" : "video");
		v.controls = true;
		v.playsInline = true;
		v.preload = "metadata";
		watch(v);
		v.src = `/${id}/file`;
		return v;
	}

	// empty fills the stage with a message: an icon, a title, a line
	// and maybe a button.
	function empty(iconName, title, text, button) {
		const box = Object.assign(document.createElement("div"), { className: "empty" });
		const head = Object.assign(document.createElement("div"), { className: "empty-header" });
		const media = Object.assign(document.createElement("div"), { className: "empty-media" });
		media.append(ui.icon(iconName));
		head.append(media,
			Object.assign(document.createElement("div"), { className: "empty-title", textContent: title }),
			Object.assign(document.createElement("div"), { className: "empty-description", textContent: text }));
		box.append(head);
		if (button) box.append(button);
		return box;
	}

	function button(iconName, text, cls = "btn") {
		const b = Object.assign(document.createElement("button"), { type: "button", className: cls });
		b.append(ui.icon(iconName), text);
		return b;
	}

	// setStage swaps what the stage shows, but only when that changes:
	// a playing video survives every poll.
	function setStage(kind, make) {
		if (stage === kind) return;
		stage = kind;
		stageEl.replaceChildren(make());
	}

	function render(st) {
		if (st.state === "expired" || st.state === "gone") return gone(st.state);
		if (st.name) {
			nameEl.textContent = st.name;
			document.title = `${st.name} · Ephemera`;
			metaEl.textContent = [sizeText(st.size), typeText(st.mime)].filter(Boolean).join(" · ");
		}
		badgeEl.replaceChildren(badge(st));
		showExpiry(st.expires);
		const playable = STREAMABLE.includes(st.mime);

		if (st.state === "live" || st.state === "available") {
			if (st.state === "available") {
				stateEl.textContent = "The whole file is here, even if the sender goes offline.";
			} else {
				const pct = st.upload && st.size ? Math.floor(100 * (st.uploaded || 0) / st.size) : -1;
				stateEl.textContent = "Streaming live from the sender's device" + (pct >= 0 ? ` · ${pct}% uploaded` : "") + ".";
			}
			canDownload(st, true);
			if (watching) {
				watching = false;
				ui.toast("The sender is back", { description: "The file streams again." });
			}
			if (playable && stage !== "none") {
				setStage("player", () => {
					const v = player(st.mime);
					// the browser can't play it after all: offer the download
					v.onerror = () => { stage = ""; setStage("none", noPreview); };
					return v;
				});
			} else {
				setStage("none", noPreview);
			}
			return;
		}

		// offline
		canDownload(st, false);
		if (st.replay) {
			stateEl.textContent = "The sender is offline, but the parts already watched replay from memory.";
			if (stage === "player") return; // already replaying
			setStage("replay", () => {
				const b = button("play", "Replay what's cached");
				b.onclick = () => {
					if (playable) { stage = ""; setStage("player", () => player(st.mime)); return; }
					const a = Object.assign(document.createElement("a"), { href: `/${id}/file`, download: (st.name || id) + " (replay)" });
					a.click();
				};
				return empty("rotate-ccw", "Replay what was watched", "Only the parts someone already played are cached.", b);
			});
		} else if (st.prefix > 0) {
			stateEl.textContent = `The first ${sizeText(st.prefix)} is here. Start watching; the rest streams when they're back.`;
			if (playable) setStage("player", () => player(st.mime));
			else waiting();
		} else {
			stateEl.textContent = "This link streams from the sender's device, and they're offline. It comes back when they do.";
			waiting();
		}
	}

	function noPreview() {
		return empty("download", "No preview for this type", "Download it to open on your device.");
	}

	function waiting() {
		setStage(watching ? "watching" : "waiting", () => {
			const b = button("bell", watching ? "Watching for it…" : "Tell me when it's back", watching ? "btn btn-outline" : "btn");
			b.disabled = watching;
			b.onclick = () => {
				watching = true;
				pollFast = true;
				b.replaceChildren(ui.icon("bell"), "Watching for it…");
				b.className = "btn btn-outline";
				b.disabled = true;
				stage = "watching";
			};
			return empty("wifi-off", "Waiting for the sender", "Leave this tab open and it picks up as soon as they reconnect.", b);
		});
	}

	// gone swaps the page for the expired or not-found message.
	function gone(state) {
		const expired = state === "expired";
		if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
		pbVideo?.pause();
		$("#link").hidden = true;
		$("#gone").hidden = false;
		$("#gone-icon").replaceChildren(ui.icon(expired ? "hourglass" : "file-question"));
		$("#gone-title").textContent = expired ? "This link has expired" : "Link not found";
		$("#gone-text").textContent = expired
			? "Ephemera links leave nothing behind. Ask the sender for a fresh one."
			: "This link doesn't exist. Check it was copied whole.";
		document.title = expired ? "Link expired · Ephemera" : "Link not found · Ephemera";
	}

	copyEl.onclick = () => ui.copy(location.href, copyEl);

	// Playback report: how watching went on this page, sent to the
	// relay when the page is hidden or closed. Numbers only: how long
	// the first frame took, how soon playing started after pressing
	// play, how often and how long playback waited for data, how long
	// seeks took. The relay adds what it measured of the sender; it
	// never sees who you are.
	const pb = {
		view: (crypto.randomUUID ? crypto.randomUUID() : String(Math.random()).slice(2)).replace(/-/g, "").slice(0, 32),
		state: "", ttff_ms: 0, start_ms: 0, stalls: 0, stall_ms: 0, seeks: 0, seek_wait_ms: 0, watched_s: 0, duration_s: 0,
	};
	let pbVideo = null, loadAt = 0, playAt = 0, waitAt = 0, seekAt = 0, pbSent = "";
	const now = () => performance.now();
	function endWait() {
		if (waitAt) { pb.stall_ms += Math.round(now() - waitAt); waitAt = 0; }
	}
	// watch is called before the video's src is set, so loadAt is
	// when it started loading
	function watch(v) {
		pbVideo = v;
		if (!loadAt) { loadAt = now(); pb.state = lastState; }
		v.addEventListener("loadeddata", () => {
			if (!pb.ttff_ms) pb.ttff_ms = Math.max(1, Math.round(now() - loadAt));
		});
		v.addEventListener("play", () => {
			if (!playAt) playAt = now();
		});
		v.addEventListener("playing", () => {
			if (playAt && !pb.start_ms) pb.start_ms = Math.max(1, Math.round(now() - playAt));
			endWait();
		});
		v.addEventListener("waiting", () => {
			// a wait for data mid-play; waits right after a seek are
			// counted as seek time instead
			if (pb.start_ms && !v.seeking && !waitAt) { pb.stalls++; waitAt = now(); }
		});
		v.addEventListener("pause", endWait);
		v.addEventListener("seeking", () => {
			endWait();
			if (pb.start_ms) { pb.seeks++; seekAt = now(); }
		});
		v.addEventListener("seeked", () => {
			if (seekAt) { pb.seek_wait_ms += Math.round(now() - seekAt); seekAt = 0; }
		});
	}
	function sendPlayback() {
		if (!pb.ttff_ms || !pbVideo) return; // never got to a first frame
		endWait();
		let watched = 0;
		for (let i = 0; i < pbVideo.played.length; i++) watched += pbVideo.played.end(i) - pbVideo.played.start(i);
		pb.watched_s = Math.round(watched * 10) / 10;
		pb.duration_s = isFinite(pbVideo.duration) ? Math.round(pbVideo.duration * 10) / 10 : 0;
		const json = JSON.stringify(pb);
		if (json === pbSent) return; // hidden and closed: one report
		pbSent = json;
		const body = new Blob([json], { type: "application/json" });
		if (!navigator.sendBeacon || !navigator.sendBeacon(`/${id}/playback`, body)) {
			fetch(`/${id}/playback`, { method: "POST", body, keepalive: true }).catch(() => {});
		}
	}
	addEventListener("pagehide", sendPlayback);
	document.addEventListener("visibilitychange", () => {
		if (document.visibilityState === "hidden") sendPlayback();
	});

	// reporting: offered once the link exists (live or offline)
	const reportBtn = $("#report-btn"), reportDialog = $("#report-dialog"), reportForm = $("#report-form");
	const reportMsg = $("#report-msg"), reportSend = $("#report-send");
	reportForm.reason.onchange = () => { reportSend.disabled = !reportForm.reason.value; };
	reportDialog.addEventListener("close", () => {
		reportForm.reset();
		reportSend.disabled = true;
		reportMsg.hidden = true;
	});
	reportForm.onsubmit = async (e) => {
		e.preventDefault();
		const data = new FormData(reportForm);
		const fail = (text) => { reportMsg.textContent = text; reportMsg.hidden = false; reportSend.disabled = false; };
		reportSend.disabled = true;
		try {
			const resp = await fetch(`/${id}/report`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ reason: data.get("reason"), note: data.get("note") }),
			});
			if (resp.ok) {
				reportDialog.close();
				ui.toast("Report sent", { description: "The relay operator will review this link." });
				return;
			}
			const body = await resp.json().catch(() => ({}));
			fail(resp.status === 429 ? "Too many reports from here. Try again later." : (body.error || "The report didn't go through."));
		} catch {
			fail("The report didn't go through. Check your connection.");
		}
	};

	// poll reports whether to keep polling: not once the link is gone
	async function poll() {
		try {
			const resp = await fetch(`/${id}/status`);
			if (resp.status === 410) { render({ state: "expired" }); return false; }
			if (resp.status === 404) { render({ state: "gone" }); return false; }
			const wasOffline = lastState === "offline";
			const st = await resp.json();
			lastState = st.state;
			render(st);
			reportBtn.hidden = false;
			if (wasOffline && st.state !== "offline") pollFast = false; // back: normal cadence
			return true;
		} catch {
			return true; // network blip: keep polling
		}
	}

	(async () => {
		while (await poll()) {
			await new Promise((r) => setTimeout(r, pollFast ? 1000 : 5000));
		}
	})();
})();
