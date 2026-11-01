// Ephemera's browser sender: this tab does what `ephemera send` does.
// It announces the file over the same WebSocket and frames as the CLI
// (docs/protocol.md), answers the relay's reads straight from the file
// in order, and uploads the whole file in the background. Once the
// relay holds every byte, the tab can close and the link keeps working.
"use strict";

// ---- protocol ---------------------------------------------------------

const MAGIC = 0x52485045; // "EPHR", little-endian
const T = { HELLO: 1, SEND: 2, READ: 3, DATA: 4, ERR: 5, AWAY: 6, ACK: 7, WELCOME: 8 };
const MAX_BODY = 1 << 20; // largest frame payload
const enc = new TextEncoder();
const dec = new TextDecoder();

// frame encodes one frame: magic, type, payload length, payload.
function frame(type, payload) {
	const buf = new Uint8Array(9 + payload.length);
	const dv = new DataView(buf.buffer);
	dv.setUint32(0, MAGIC, true);
	buf[4] = type;
	dv.setUint32(5, payload.length, true);
	buf.set(payload, 9);
	return buf;
}

function jsonFrame(type, obj) {
	return frame(type, enc.encode(JSON.stringify(obj)));
}

// FrameReader splits a byte stream into frames, whatever the message
// boundaries. push returns the complete frames so far.
class FrameReader {
	constructor() { this.buf = new Uint8Array(0); }
	push(bytes) {
		const joined = new Uint8Array(this.buf.length + bytes.length);
		joined.set(this.buf);
		joined.set(bytes, this.buf.length);
		this.buf = joined;
		const frames = [];
		while (this.buf.length >= 9) {
			const dv = new DataView(this.buf.buffer, this.buf.byteOffset);
			if (dv.getUint32(0, true) !== MAGIC) throw new Error("bad frame magic");
			const n = dv.getUint32(5, true);
			if (n > MAX_BODY) throw new Error("frame too large");
			if (this.buf.length < 9 + n) break;
			const type = this.buf[4];
			const payload = this.buf.slice(9, 9 + n);
			frames.push({ type, payload, json: type === T.DATA ? null : (n ? JSON.parse(dec.decode(payload)) : {}) });
			this.buf = this.buf.slice(9 + n);
		}
		return frames;
	}
}

function b64url(bytes) {
	let s = "";
	for (const b of bytes) s += String.fromCharCode(b);
	return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// shareIdFor derives the share id a raw 32-byte owner key certifies:
// the first 16 bytes of sha256("ephemera share id v1\0" + key).
async function shareIdFor(raw) {
	const domain = enc.encode("ephemera share id v1\0");
	const msg = new Uint8Array(domain.length + raw.length);
	msg.set(domain);
	msg.set(raw, domain.length);
	const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", msg));
	return b64url(sum.slice(0, 16));
}

// newOwnerKey draws a secret owner key and the share id it certifies.
// Ids starting with '-' are redrawn, like the CLI: command lines would
// read them as flags.
async function newOwnerKey() {
	for (;;) {
		const raw = crypto.getRandomValues(new Uint8Array(32));
		const id = await shareIdFor(raw);
		if (id[0] !== "-") return { key: b64url(raw), id };
	}
}

// ---- one shared file --------------------------------------------------

// Pacing, as in the CLI: 256 KiB bursts, up to 4 MiB unacked while
// nobody is watching, one burst while a receiver read came in lately.
const CHUNK = 256 << 10;
const WINDOW = 16 * CHUNK;
const READING_MS = 2000;
const ACK_TIMEOUT_MS = 30000;

class Share {
	constructor(file, ttlSec, token, onChange) {
		this.file = file;
		this.token = token;
		this.createdAt = Date.now();
		this.deadline = this.createdAt + ttlSec * 1000;
		this.onChange = onChange;
		this.state = "connecting"; // connecting|live|uploaded|refused|expired
		this.link = "";
		this.uploading = false;    // the relay is keeping the upload
		this.acked = 0;            // bytes the relay holds
		this.latestAck = 0;        // newest ack, even one nobody waited for
		this.rate = 0;             // upload bytes/s
		this.message = "";
		this.lastRead = 0;
		this.attempt = 0;
	}

	async start() {
		const { key, id } = await newOwnerKey();
		this.key = key;
		this.id = id;
		this.dial();
	}

	dial() {
		if (this.finished()) return;
		if (Date.now() >= this.deadline) return this.set("expired");
		const ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/daemon");
		ws.binaryType = "arraybuffer";
		this.ws = ws;
		const reader = new FrameReader();
		let reads = Promise.resolve(); // reads are answered in order
		ws.onopen = () => {
			ws.send(jsonFrame(T.HELLO, { version: "web-0.1.0", name: "browser", token: this.token }));
			ws.send(jsonFrame(T.SEND, {
				share_id: this.id, owner_key: this.key, name: this.file.name, size: this.file.size,
				mime: this.file.type || "application/octet-stream",
				ttl_sec: Math.max(1, Math.round((this.deadline - Date.now()) / 1000)),
				upload: true,
			}));
		};
		ws.onmessage = (ev) => {
			let frames;
			try { frames = reader.push(new Uint8Array(ev.data)); } catch { ws.close(); return; }
			for (const f of frames) {
				switch (f.type) {
				case T.WELCOME: this.welcome(ws, f.json); break;
				case T.READ: reads = reads.then(() => this.serveRead(ws, f.json)); break;
				case T.ACK: this.ack(f.json.have); break;
				case T.ERR:
					// before the welcome, a refusal: the relay won't take
					// this share, and asking again won't change that
					this.message = f.json.message || "the relay refused this file";
					this.refusedCode = f.json.code;
					this.set("refused");
					ws.close();
					break;
				}
			}
		};
		ws.onclose = () => {
			if (this.ws !== ws || this.finished()) return;
			this.ackWaiter?.(-1);
			this.set("connecting");
			const wait = Math.min(8000, 500 * 2 ** Math.min(this.attempt++, 4));
			setTimeout(() => this.dial(), wait / 2 + Math.random() * wait / 2);
		};
	}

	welcome(ws, w) {
		if (w.share_id !== this.id) return;
		this.attempt = 0;
		this.link = w.link || location.origin + "/" + this.id;
		this.uploading = !!w.upload;
		this.acked = this.latestAck = w.have || 0;
		this.set("live");
		if (this.uploading) this.upload(ws);
	}

	// serveRead answers one read with the file's bytes, or a refusal
	// for a range outside the file.
	async serveRead(ws, rd) {
		this.lastRead = Date.now();
		const { offset, length } = rd;
		if (rd.share_id !== this.id || offset < 0 || length < 1 || offset + length > this.file.size) {
			ws.send(jsonFrame(T.ERR, { share_id: rd.share_id, code: "denied", message: "read out of bounds" }));
			return;
		}
		let data;
		try {
			data = new Uint8Array(await this.file.slice(offset, offset + length).arrayBuffer());
		} catch {
			ws.send(jsonFrame(T.ERR, { share_id: rd.share_id, code: "io", message: "read failed" }));
			return;
		}
		if (ws.readyState !== WebSocket.OPEN) return;
		for (let i = 0; i < data.length; i += MAX_BODY) ws.send(frame(T.DATA, data.subarray(i, i + MAX_BODY)));
	}

	// ack keeps the newest ack: one can arrive while the uploader is
	// reading the next chunk from the file, not waiting.
	ack(have) {
		this.latestAck = Math.max(this.latestAck, have);
		const waiter = this.ackWaiter;
		this.ackWaiter = null;
		waiter?.(have);
	}

	nextAck() {
		if (this.latestAck > this.acked) return Promise.resolve(this.latestAck);
		return new Promise((resolve) => {
			this.ackWaiter = resolve;
			setTimeout(() => { if (this.ackWaiter === resolve) { this.ackWaiter = null; resolve(-1); } }, ACK_TIMEOUT_MS);
		});
	}

	// upload sends the file from what the relay already holds, in
	// bursts of an away header plus its data, and finishes when the
	// relay acks the last byte. Acks are cumulative; one that doesn't
	// move forward means the relay stopped taking bytes.
	async upload(ws) {
		const size = this.file.size;
		let sent = this.acked;
		const began = Date.now(), from = this.acked;
		while (this.acked < size && this.ws === ws) {
			const window = Date.now() - this.lastRead < READING_MS ? CHUNK : WINDOW;
			if (sent < size && sent - this.acked < window) {
				const off = sent, n = Math.min(CHUNK, size - sent);
				const data = new Uint8Array(await this.file.slice(off, off + n).arrayBuffer());
				if (this.ws !== ws || ws.readyState !== WebSocket.OPEN) return;
				// header and data back to back: the relay pairs them
				ws.send(jsonFrame(T.AWAY, { share_id: this.id, offset: off }));
				ws.send(frame(T.DATA, data));
				sent += n;
				continue;
			}
			const have = await this.nextAck();
			if (have < 0) return; // gone or silent: a redial resumes
			if (have <= this.acked) { this.uploading = false; this.message = "the relay stopped taking the upload"; this.set("live"); return; }
			this.acked = have;
			sent = Math.max(sent, have);
			this.rate = (this.acked - from) / Math.max((Date.now() - began) / 1000, 0.001);
			this.onChange(this);
		}
		if (this.acked >= size && this.ws === ws) {
			this.set("uploaded");
			ws.close(1000, "uploaded"); // the relay serves it from here
		}
	}

	finished() { return this.state === "uploaded" || this.state === "refused" || this.state === "expired"; }

	set(state) {
		this.state = state;
		this.onChange(this);
	}
}

// ---- page -------------------------------------------------------------

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

function untilText(t) {
	return new Date(t).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function leftText(ms) {
	if (ms <= 0) return "0s";
	const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60);
	if (h) return `${h}h ${m}m`;
	if (m) return `${m}m ${s % 60}s`;
	return `${s}s`;
}

function iconFor(mime) {
	if (mime.startsWith("video/")) return "file-video";
	if (mime.startsWith("audio/")) return "file-audio";
	if (mime === "application/pdf" || mime.startsWith("text/")) return "file-text";
	if (/zip|tar|diskimage|compressed|gzip|7z|rar/.test(mime)) return "file-archive";
	return "file";
}

// badge is the status pill on a share card.
function badge(kind, text) {
	const b = document.createElement("span");
	const look = {
		connecting: ["badge badge-outline muted", "loader"],
		live: ["badge badge-live"],
		stored: ["badge badge-secondary", "check"],
		warning: ["badge badge-warning", "triangle-alert"],
		refused: ["badge badge-destructive", "circle-slash"],
		expired: ["badge badge-outline muted", "hourglass"],
	}[kind];
	b.className = look[0];
	if (look[1]) b.append(ui.icon(look[1], kind === "connecting" ? "spin" : ""));
	if (kind === "live") b.append(Object.assign(document.createElement("span"), { className: "dot" }));
	b.append(text);
	return b;
}

// needsToken: the relay turned a share away until its sender signs in
// (no token, or a guest over the guest limits)
const needsToken = (s) => s.state === "refused" && s.refusedCode === "denied" && /token|sign in/.test(s.message);

// describe says where a share stands: its badge and one line.
function describe(s) {
	switch (s.state) {
	case "connecting":
		return s.link
			? ["connecting", "Reconnecting", "Reconnecting… the link is offline until this tab is back."]
			: ["connecting", "Connecting", "Reaching the relay…"];
	case "live":
		if (s.uploading) {
			const pct = Math.floor(100 * s.acked / s.file.size);
			return ["live", `Live · ${pct}%`, `Streaming from this tab while it uploads${s.rate ? ` at ${sizeText(s.rate)}/s` : ""}. Keep the tab open until it's up.`];
		}
		return ["warning", "Live only",
			`${s.message ? s.message[0].toUpperCase() + s.message.slice(1) : "This relay isn't keeping uploads right now"}. The link streams from this tab, so keep it open until ${untilText(s.deadline)}.`];
	case "uploaded":
		return ["stored", "On the relay", `Fully uploaded. You can close this tab; the link works until ${untilText(s.deadline)}.`];
	case "refused":
		// the relay's reason names the CLI; say what to do here
		if (!needsToken(s)) return ["refused", "Not shared", `The relay turned it away: ${s.message}.`];
		return ["refused", "Not shared", /token/.test(s.message)
			? "This relay needs you to sign in. Sign in and the file is sent at once."
			: `${s.message[0].toUpperCase() + s.message.slice(1)}.`];
	case "expired":
		return ["expired", "Expired", "The link expired and the relay deleted its copy."];
	}
}

const CARD = `<div class="share-life" aria-hidden="true"><span></span></div>
<div class="share-body">
	<div class="share-top">
		<div class="share-icon"></div>
		<div class="share-what"><div class="share-name"></div><div class="mono xs muted share-meta"></div></div>
		<span class="share-badge"></span>
	</div>
	<p class="sm share-line"></p>
	<div class="progress" role="progressbar" aria-label="Uploaded" aria-valuemin="0" aria-valuemax="100" hidden><span></span></div>
	<div class="share-skeleton" aria-hidden="true" hidden></div>
	<div class="share-link" hidden>
		<input class="input mono" readonly aria-label="Link">
		<button class="btn btn-outline" type="button"><i data-icon="copy"></i><span data-label="Copy">Copy</span></button>
		<a class="btn btn-outline" target="_blank" rel="noopener" aria-label="Open link page"><i data-icon="arrow-up-right"></i>Open</a>
	</div>
	<div><button class="btn btn-sm" type="button" data-signin hidden>Sign in</button></div>
</div>
<div class="share-foot"><span><i data-icon="timer"></i><span class="share-until"></span></span><span class="mono num share-left"></span></div>`;

function startPage() {
	const $ = (s, el = document) => el.querySelector(s);
	const TOKEN_KEY = "ephemera.token", ACCOUNT_KEY = "ephemera.account";
	let token = ui.store.get(TOKEN_KEY);
	let account = ui.store.get(ACCOUNT_KEY); // set when the token came from signing in here
	let policy = { max: { max_ttl: 86400, max_size: 5 << 30 } }; // until /config says
	const shares = [];
	let wakeLock = null;

	// ---- who is sending: a guest, a pasted token, or a signed-in account

	const PROVIDERS = { google: "Google", github: "GitHub" };
	const durationText = (s) => s >= 3600 ? `${s / 3600} hour${s === 3600 ? "" : "s"}` : `${Math.round(s / 60)} minutes`;
	const limits = () => (!token && policy.guest) || policy.max;
	// capText says a size cap the way the relay sets it, in binary units:
	// 5 GiB reads "5 GB", not "5.37 GB"
	const capText = (n) => {
		const gb = n / 2 ** 30;
		return gb >= 1 ? `${+gb.toFixed(1)} GB` : `${Math.round(n / 2 ** 20)} MB`;
	};
	// guests get less than a token would: there's a reason to sign in
	const guestLimited = () => !!policy.guest && (policy.guest.max_ttl < policy.max.max_ttl || policy.guest.max_size < policy.max.max_size);
	let ttlPicked = false; // the sender chose a lifetime themselves

	// showSender sets everything that depends on who is sending: the
	// account button, the guest note, the lifetimes on offer, the size
	// cap.
	function showSender() {
		const btn = $("#account-btn");
		$("#account-label").textContent = token ? (account ? account.replace(/^[a-z]+:/, "") : "Token saved") : "Sign in";
		const dot = $(".dot-ok", btn);
		if (token && !dot) btn.append(Object.assign(document.createElement("span"), { className: "dot-ok" }));
		if (!token && dot) dot.remove();

		// an open relay takes everyone in full: nothing to sign in for
		btn.hidden = !token && !!policy.guest && !guestLimited() && !policy.sign_in;
		const guest = !token && policy.guest;
		$("#guest-note").hidden = !guest || !guestLimited() || !policy.sign_in;
		if (guest) {
			$("#guest-ttl").textContent = durationText(policy.guest.max_ttl);
			$("#guest-size").textContent = capText(policy.guest.max_size);
		}
		$("#max-size").textContent = capText(limits().max_size);

		// lifetimes over the cap can't be picked; unless the sender chose
		// one, offer the longest that can
		const radios = [...document.querySelectorAll("input[name=ttl]")];
		for (const r of radios) r.disabled = Number(r.value) > limits().max_ttl;
		const picked = $("input[name=ttl]:checked");
		if (!picked || picked.disabled || !ttlPicked) {
			const ok = radios.filter((r) => !r.disabled);
			(ok[ok.length - 1] || radios[0]).checked = true;
		}
	}

	function setToken(t, name = "") {
		token = t;
		account = t ? name : "";
		// the account first: another tab reads it when the token lands
		ui.store.set(ACCOUNT_KEY, account);
		ui.store.set(TOKEN_KEY, token);
		showSender();
		// retry the files the relay turned away until the sender signed in
		if (token) for (const s of shares) if (needsToken(s)) {
			s.token = token; s.state = "connecting"; s.message = ""; s.dial(); render(s);
		}
	}

	// sign-in dialog: a button per provider, and a field for a token
	// made with `relay token`
	const signinDialog = $("#signin-dialog"), tokenInput = $("#token"), tokenSave = $("#token-save");
	function openSignIn() {
		const providers = policy.sign_in || [];
		$("#signin-title").textContent = providers.length ? "Sign in" : "Sender token";
		$("#signin-text").textContent = guestLimited()
			? `Guests can share files up to ${capText(policy.guest.max_size)}, with links that last ${durationText(policy.guest.max_ttl)}. Sign in for files up to ${capText(policy.max.max_size)} and links up to ${durationText(policy.max.max_ttl)}. Viewers never need an account.`
			: providers.length
				? "This relay shares files from people who've signed in. Viewers never need an account."
				: "This relay only accepts files from people with a token. It's asked for once and stays in this browser. Viewers never need one.";
		const box = $("#signin-buttons");
		box.replaceChildren(...providers.map((p) => {
			const b = Object.assign(document.createElement("button"), { type: "button", className: "btn btn-outline", textContent: `Continue with ${PROVIDERS[p] || p}` });
			b.onclick = () => signIn(p);
			return b;
		}));
		box.hidden = !providers.length;
		$("#paste-token").open = !providers.length;
		signinDialog.showModal();
	}
	signinDialog.addEventListener("close", () => { tokenInput.value = ""; tokenSave.disabled = true; });
	tokenInput.oninput = () => { tokenSave.disabled = !tokenInput.value.trim(); };
	$("#token-form").onsubmit = (e) => {
		e.preventDefault();
		const v = tokenInput.value.trim();
		if (!v) return;
		signinDialog.close();
		setToken(v);
		ui.toast("Sender token saved in this browser");
	};

	// signIn opens the provider in a popup, so files streaming from this
	// tab keep going; the popup lands back on this page, claims the
	// token and stores it, and the storage event brings it here. With
	// popups blocked, this tab goes instead.
	function signIn(provider) {
		const url = `/auth/${provider}`;
		const popup = open(url, "ephemera-signin", "popup,width=520,height=680");
		if (!popup) {
			if (shares.some(inFlight) && !confirm("Signing in leaves this page, and the files streaming from it stop. Sign in anyway?")) return;
			location.href = url;
			return;
		}
		signinDialog.close();
	}
	addEventListener("storage", (e) => {
		if (e.key !== TOKEN_KEY) return;
		const name = ui.store.get(ACCOUNT_KEY);
		setToken(e.newValue || "", name);
		if (e.newValue) ui.toast(name ? `Signed in as ${name}` : "Sender token saved");
	});

	// back from the provider: collect the token the relay parked
	async function claim() {
		const tag = location.hash.slice(1);
		if (!/^(signed-in|signin-failed|signin-busy)$/.test(tag)) return;
		history.replaceState(null, "", location.pathname + location.search);
		const done = () => { if (opener) { try { close(); } catch {} } };
		if (tag === "signin-failed") return ui.toast("Sign-in didn't finish", { description: "Nothing was changed. Try again.", kind: "error" });
		if (tag === "signin-busy") return ui.toast("Too many sign-ins from here", { description: "Try again in a while.", kind: "error" });
		try {
			const resp = await fetch("/auth/claim", { method: "POST" });
			const body = await resp.json();
			if (!resp.ok) throw new Error(body.error);
			setToken(body.token, body.name);
			ui.toast(`Signed in as ${body.name}`, { description: opener ? "You can close this window." : "" });
			done();
		} catch (err) {
			ui.toast("Sign-in didn't finish", { description: String(err.message || err), kind: "error" });
		}
	}

	// account dialog: the token for the CLI, and signing out
	const accountDialog = $("#account-dialog");
	function openAccount() {
		$("#account-title").textContent = account ? "Signed in" : "Sender token";
		$("#account-text").textContent = account
			? `As ${account}. Your files can be up to ${capText(policy.max.max_size)}, with links up to ${durationText(policy.max.max_ttl)}.`
			: "This browser has a sender token saved.";
		$("#token-mask").textContent = "•••• •••• " + token.slice(-4);
		$("#signout").textContent = account ? "Sign out" : "Forget token";
		accountDialog.showModal();
	}
	$("#token-copy").onclick = (e) => ui.copy(token, e.currentTarget, "Token copied");
	$("#signout").onclick = async () => {
		// a token from signing in here is revoked; a pasted one may be in
		// use elsewhere (the CLI), so it's only forgotten
		if (account) fetch("/auth/signout", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }) }).catch(() => {});
		accountDialog.close();
		const was = account;
		setToken("");
		ui.toast(was ? "Signed out" : "Token forgotten", { kind: "" });
	};

	$("#account-btn").onclick = () => (token ? openAccount() : openSignIn());
	for (const r of document.querySelectorAll("input[name=ttl]")) r.addEventListener("change", () => { ttlPicked = true; });
	document.addEventListener("click", (e) => { if (e.target.closest("[data-signin]")) openSignIn(); });

	// keep the screen (and so the tab) awake while anything still
	// depends on it
	async function holdAwake(busy) {
		try {
			if (busy && !wakeLock && navigator.wakeLock) wakeLock = await navigator.wakeLock.request("screen");
			if (!busy && wakeLock) { await wakeLock.release(); wakeLock = null; }
		} catch {}
	}

	const inFlight = (s) => s.state === "live" || s.state === "connecting";
	const clearable = (s) => s.state === "uploaded" || s.state === "refused" || s.state === "expired";

	// showList sets the list's heading and the header mark from all shares.
	function showList() {
		const busy = shares.filter(inFlight).length;
		$("#shares-box").hidden = shares.length === 0;
		$("#shares-note").textContent = busy ? "Keep this tab open while files are live" : "Your links";
		$("#clear").hidden = !shares.some(clearable);
		ui.fly(busy);
		holdAwake(busy > 0);
	}

	// tick drains each card's lifetime bar and counts down; a share
	// whose time is up has expired on the relay too.
	function tick(s) {
		const now = Date.now();
		if (now >= s.deadline && s.state !== "refused" && s.state !== "expired") {
			s.set("expired");
			s.ws?.close();
			return;
		}
		const total = s.deadline - s.createdAt;
		const life = s.state === "expired" ? 0 : Math.max(0, Math.min(1, (s.deadline - now) / total));
		$(".share-life > span", s.card).style.transform = `scaleX(${life})`;
		$(".share-left", s.card).textContent = `${leftText(s.deadline - now)} left`;
	}

	function render(s) {
		const card = s.card;
		const [kind, label, line] = describe(s);
		$(".share-badge", card).replaceChildren(badge(kind, label));
		$(".share-line", card).textContent = line;
		card.classList.toggle("dim", s.state === "expired");

		const bar = $(".progress", card);
		bar.hidden = !(s.state === "live" && s.uploading);
		if (!bar.hidden) {
			const pct = Math.floor(100 * s.acked / s.file.size);
			bar.firstElementChild.style.transform = `scaleX(${s.acked / s.file.size})`;
			bar.setAttribute("aria-valuenow", pct);
		}
		$(".share-skeleton", card).hidden = !(s.state === "connecting" && !s.link);
		const linkRow = $(".share-link", card);
		linkRow.hidden = !s.link || s.state === "refused" || s.state === "expired";
		$("input", linkRow).value = s.link;
		$("button", linkRow).dataset.copy = s.link;
		$("a", linkRow).href = s.link || "#";
		$("[data-signin]", card).hidden = !needsToken(s) || !!token;
		$(".share-foot", card).hidden = s.state === "refused" || s.state === "expired";
		$(".share-until", card).textContent = `Expires at ${untilText(s.deadline)}`;
		tick(s);
		showList();
	}

	function add(file) {
		if (file.size === 0) {
			ui.toast(`${file.name} is empty`, { description: "Empty files can't be shared.", kind: "error" });
			return;
		}
		if (file.size > limits().max_size) {
			const more = !token && policy.guest && policy.sign_in && file.size <= policy.max.max_size;
			ui.toast(`${file.name} is too big`, {
				description: `${!token && policy.guest ? "Guests can share" : "This relay takes"} files up to ${capText(limits().max_size)}.${more ? " Sign in to share it." : ""}`,
				kind: "error",
			});
			return;
		}
		const card = document.createElement("div");
		card.className = "card share";
		card.innerHTML = CARD;
		for (const el of card.querySelectorAll("i[data-icon]")) el.replaceWith(ui.icon(el.dataset.icon));
		$(".share-icon", card).append(ui.icon(iconFor(file.type)));
		$(".share-name", card).textContent = file.name;
		$(".share-meta", card).textContent = [sizeText(file.size), typeText(file.type)].filter(Boolean).join(" · ");
		$("input", card).onfocus = (e) => e.target.select();
		$("#shares").prepend(card);

		const ttl = Number($("input[name=ttl]:checked").value);
		const s = new Share(file, ttl, token, render);
		s.card = card;
		shares.push(s);
		render(s);
		s.start().catch((err) => { s.message = String(err.message || err); s.set("refused"); });
	}

	setInterval(() => { for (const s of shares) tick(s); }, 1000);

	$("#clear").onclick = () => {
		for (let i = shares.length - 1; i >= 0; i--) {
			if (clearable(shares[i])) { shares[i].card.remove(); shares.splice(i, 1); }
		}
		showList();
	};

	const input = $("#files"), drop = $("#drop"), dropTitle = $("#drop-title");
	const over = (on) => {
		drop.classList.toggle("over", on);
		dropTitle.textContent = on ? "Let go to get a link" : "Drop files here";
	};
	input.onchange = () => { for (const f of input.files) add(f); input.value = ""; };
	drop.ondragover = (e) => { e.preventDefault(); over(true); };
	drop.ondragleave = (e) => { if (!drop.contains(e.relatedTarget)) over(false); };
	drop.ondrop = (e) => { e.preventDefault(); over(false); for (const f of e.dataTransfer.files) add(f); };

	// closing the tab before the upload ends takes the link offline
	addEventListener("beforeunload", (e) => {
		if (shares.some((s) => s.state === "live" || (s.state === "connecting" && s.link))) e.preventDefault();
	});

	showSender();
	fetch("/config").then((r) => r.json()).then((p) => { policy = p; showSender(); }).catch(() => {});
	claim();
}

if (typeof document !== "undefined") startPage();
if (typeof module !== "undefined") module.exports = { frame, jsonFrame, FrameReader, shareIdFor, b64url, T };
