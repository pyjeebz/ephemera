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
		this.deadline = Date.now() + ttlSec * 1000;
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

function untilText(t) {
	return new Date(t).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function startPage() {
	const $ = (s) => document.querySelector(s);
	const TOKEN_KEY = "ephemera.token";
	const storage = {
		get() { try { return localStorage.getItem(TOKEN_KEY) || ""; } catch { return ""; } },
		set(v) { try { v ? localStorage.setItem(TOKEN_KEY, v) : localStorage.removeItem(TOKEN_KEY); } catch {} },
	};
	let token = storage.get();
	const shares = [];
	let wakeLock = null;

	function showWho() {
		const who = $("#who");
		who.replaceChildren();
		if (!token) return;
		who.append("Sender token saved · ");
		const forget = Object.assign(document.createElement("a"), { href: "#", textContent: "forget it" });
		forget.onclick = (e) => { e.preventDefault(); token = ""; storage.set(""); showWho(); };
		who.append(forget);
	}

	$("#token-form").onsubmit = (e) => {
		e.preventDefault();
		token = new FormData(e.target).get("token").trim();
		storage.set(token);
		$("#token").hidden = true;
		showWho();
		// retry the files the relay turned away for want of a token
		for (const s of shares) if (s.state === "refused" && s.refusedCode === "denied" && /token/.test(s.message)) {
			s.token = token; s.state = "connecting"; s.message = ""; s.dial();
		}
	};

	// keep the screen (and so the tab) awake while anything still
	// depends on it
	async function holdAwake() {
		const busy = shares.some((s) => s.state === "live" || s.state === "connecting");
		try {
			if (busy && !wakeLock && navigator.wakeLock) wakeLock = await navigator.wakeLock.request("screen");
			if (!busy && wakeLock) { await wakeLock.release(); wakeLock = null; }
		} catch {}
	}

	function render(s) {
		const card = s.card;
		card.querySelector(".name").textContent = `${s.file.name} · ${sizeText(s.file.size)}`;
		const state = card.querySelector(".state");
		const bar = card.querySelector("progress");
		const linkRow = card.querySelector(".link");
		state.className = "state";
		bar.hidden = true;
		switch (s.state) {
		case "connecting":
			state.textContent = s.link ? "Reconnecting… the link is offline until this tab is back." : "Connecting to the relay…";
			break;
		case "live":
			if (s.uploading) {
				const pct = Math.floor(100 * s.acked / s.file.size);
				state.textContent = `Live from this tab · ${pct}% uploaded${s.rate ? ` at ${sizeText(s.rate)}/s` : ""}. Keep the tab open until it's up.`;
				bar.hidden = false;
				bar.value = s.acked;
				bar.max = s.file.size;
			} else {
				state.className = "state warn";
				state.textContent = `${s.message ? s.message[0].toUpperCase() + s.message.slice(1) : "This relay isn't keeping uploads right now"}: the link streams from this tab, so keep it open until ${untilText(s.deadline)}.`;
			}
			break;
		case "uploaded":
			state.className = "state ok";
			state.textContent = `Uploaded. You can close this tab; the link works until ${untilText(s.deadline)}.`;
			break;
		case "refused":
			state.className = "state bad";
			if (/token/.test(s.message)) {
				// the relay's reason names the CLI; say what to do here
				state.textContent = "Not shared yet: this relay needs a valid sender token. Paste it above and the file is sent at once.";
				$("#token").hidden = false;
			} else {
				state.textContent = `Not shared: ${s.message}.`;
			}
			break;
		case "expired":
			state.textContent = "The link expired.";
			break;
		}
		linkRow.hidden = !s.link || s.state === "refused" || s.state === "expired";
		card.querySelector("input").value = s.link;
		card.querySelector("a.btn").href = s.link || "#";
		holdAwake();
	}

	function add(file) {
		const card = document.createElement("div");
		card.className = "card";
		card.innerHTML = `<div class="name"></div><div class="state"></div><progress hidden></progress>
			<div class="row link" hidden><input readonly aria-label="Link"><button type="button">Copy</button><a class="btn" target="_blank" rel="noopener">Open</a></div>`;
		card.querySelector("button").onclick = async (e) => {
			try { await navigator.clipboard.writeText(card.querySelector("input").value); e.target.textContent = "Copied"; }
			catch { card.querySelector("input").select(); }
		};
		$("#shares").prepend(card);
		if (file.size === 0) {
			card.querySelector(".name").textContent = file.name;
			card.querySelector(".state").textContent = "Empty files can't be shared.";
			return;
		}
		const s = new Share(file, Number($("#ttl").value), token, render);
		s.card = card;
		shares.push(s);
		render(s);
		s.start().catch((err) => { s.message = String(err.message || err); s.set("refused"); });
	}

	const input = $("#files"), drop = $("#drop");
	input.onchange = () => { for (const f of input.files) add(f); input.value = ""; };
	drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
	drop.ondragleave = () => drop.classList.remove("over");
	drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove("over"); for (const f of e.dataTransfer.files) add(f); };

	// closing the tab before the upload ends takes the link offline
	addEventListener("beforeunload", (e) => {
		if (shares.some((s) => s.state === "live" || (s.state === "connecting" && s.link))) e.preventDefault();
	});

	showWho();
}

if (typeof document !== "undefined") startPage();
if (typeof module !== "undefined") module.exports = { frame, jsonFrame, FrameReader, shareIdFor, b64url, T };
