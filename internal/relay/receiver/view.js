// The page a link opens. It polls /:id/status, shows the file in the
// state it's in (live from the sender, available on the relay, offline,
// expired), plays media in the browser's own player, and reports how
// playback went when the page is hidden or closed.
"use strict";
(() => {
	const id = location.pathname.split("/").filter(Boolean)[0] || "";
	const $ = (s) => document.querySelector(s);
	const nameEl = $("#name"), metaEl = $("#meta"), stateEl = $("#state"), expiryEl = $("#expiry"), heroEl = $("#hero");
	const actionsEl = $("#actions"), downloadEl = $("#download");

	const STREAMABLE = ["video/mp4", "video/webm", "video/quicktime", "audio/mpeg", "audio/mp4", "audio/wav"];

	let hero = ""; // "video" | "file" | ""
	let countdownTimer = null;
	let lastState = "";
	let pollFast = false;

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
		return mime.split(";")[0];
	}

	function showExpiry(expires) {
		if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
		if (!expires) { expiryEl.textContent = ""; return; }
		const tick = () => {
			const left = expires - Date.now() / 1000;
			if (left <= 0) { expiryEl.textContent = ""; poll(); return; }
			const h = Math.floor(left / 3600), m = Math.floor(left % 3600 / 60), s = Math.floor(left % 60);
			expiryEl.textContent = `The link expires in ${h ? `${h}h ` : ""}${m}m ${h ? "" : `${s}s`}`.trim() + ". After that, nothing is left.";
		};
		tick();
		countdownTimer = setInterval(tick, 1000);
	}

	function setActions(st, show) {
		actionsEl.hidden = !show;
		if (!show) return;
		downloadEl.href = `/${id}/file`;
		downloadEl.download = st.name || id;
	}

	function player() {
		const v = document.createElement("video");
		v.controls = true;
		v.playsInline = true;
		watch(v);
		v.src = `/${id}/file`;
		return v;
	}

	function render(st) {
		if (st.name) {
			nameEl.textContent = st.name;
			document.title = `${st.name} · Ephemera`;
			metaEl.textContent = [sizeText(st.size), typeText(st.mime)].filter(Boolean).join(" · ");
		}
		stateEl.className = "state";
		switch (st.state) {
		case "live":
		case "available": {
			if (st.state === "available") {
				stateEl.className = "state ok";
				stateEl.textContent = "Ready. The whole file is here, even if the sender goes offline.";
			} else {
				const pct = st.upload && st.size ? Math.floor(100 * (st.uploaded || 0) / st.size) : -1;
				stateEl.textContent = "Streaming live from the sender's device" + (pct >= 0 ? ` · ${pct}% uploaded` : "") + ".";
			}
			showExpiry(st.expires);
			setActions(st, true);
			if (!hero && STREAMABLE.includes(st.mime)) {
				hero = "video";
				const v = player();
				v.onerror = () => { hero = "file"; heroEl.replaceChildren(); };
				heroEl.replaceChildren(v);
			} else if (!hero) {
				hero = "file"; // not playable here: the download button is the way in
			}
			break;
		}
		case "offline":
			hero = "";
			heroEl.replaceChildren();
			setActions(st, false);
			showExpiry(st.expires);
			stateEl.className = "state warn";
			if (st.replay) {
				stateEl.textContent = "The sender is offline, but the parts already watched replay from memory.";
				const a = Object.assign(document.createElement("a"), { className: "btn", href: `/${id}/file`, textContent: "Replay what's cached" });
				a.download = (st.name || id) + " (replay)";
				heroEl.replaceChildren(a);
			} else if (st.prefix > 0) {
				stateEl.textContent = `The sender is offline, but the first ${sizeText(st.prefix)} is here. Start watching; the rest streams when they're back.`;
				if (STREAMABLE.includes(st.mime)) heroEl.replaceChildren(player());
			} else {
				stateEl.textContent = "This link streams from the sender's device, and they're offline. It comes back when they do.";
				const btn = Object.assign(document.createElement("button"), { type: "button", textContent: "Tell me when it's back" });
				btn.onclick = () => {
					btn.textContent = "Watching for it. Leave this tab open.";
					btn.disabled = true;
					pollFast = true;
				};
				heroEl.replaceChildren(btn);
			}
			break;
		case "expired":
			hero = "";
			heroEl.replaceChildren();
			setActions(st, false);
			showExpiry(0);
			metaEl.textContent = "";
			stateEl.textContent = "This link has expired. Ephemera links leave nothing behind.";
			break;
		default:
			hero = "";
			heroEl.replaceChildren();
			setActions(st, false);
			showExpiry(0);
			nameEl.textContent = "Link not found";
			stateEl.textContent = "This link doesn't exist. Check it was copied whole.";
		}
	}

	$("#copy").onclick = async (e) => {
		try { await navigator.clipboard.writeText(location.href); e.target.textContent = "Copied"; }
		catch { e.target.textContent = location.href; }
	};

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
	const reportEl = $("#report"), reportForm = $("#report-form"), reportMsg = $("#report-msg");
	reportForm.onsubmit = async (e) => {
		e.preventDefault();
		const data = new FormData(reportForm);
		const btn = reportForm.querySelector("button");
		btn.disabled = true;
		try {
			const resp = await fetch(`/${id}/report`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ reason: data.get("reason"), note: data.get("note") }),
			});
			if (resp.ok) {
				reportForm.replaceChildren(Object.assign(document.createElement("p"), {
					textContent: "Thanks. The relay operator will review this link.",
				}));
				return;
			}
			const body = await resp.json().catch(() => ({}));
			reportMsg.textContent = resp.status === 429
				? "Too many reports from here. Try again later."
				: (body.error || "The report didn't go through.");
		} catch {
			reportMsg.textContent = "The report didn't go through. Check your connection.";
		}
		btn.disabled = false;
	};

	// poll reports whether to keep polling: not once the link is gone
	async function poll() {
		try {
			const resp = await fetch(`/${id}/status`);
			if (resp.status === 410) { reportEl.hidden = true; render({ state: "expired" }); return false; }
			if (resp.status === 404) { reportEl.hidden = true; render({ state: "gone" }); return false; }
			const wasOffline = lastState === "offline";
			const st = await resp.json();
			lastState = st.state;
			render(st);
			reportEl.hidden = false;
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
