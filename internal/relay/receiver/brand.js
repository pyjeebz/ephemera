// The brand page's one control: set the mayfly specimen in flight or
// at rest.
"use strict";
addEventListener("DOMContentLoaded", () => {
	const sw = document.querySelector("#flying");
	const note = document.querySelector("#specimen-note");
	sw.onclick = () => {
		const on = sw.getAttribute("aria-checked") !== "true";
		sw.setAttribute("aria-checked", String(on));
		document.querySelector("#specimen").dataset.flying = String(on);
		note.textContent = on ? "In flight: a file is being transferred." : "At rest: nothing is moving.";
	};
	document.querySelector("#specimen").dataset.flying = "true";
});
