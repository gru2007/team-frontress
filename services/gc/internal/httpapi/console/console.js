// The moderation console. Everything players wrote is put on the page as
// text (textContent), never as markup.
"use strict";

const $ = (id) => document.getElementById(id);
const store = {
	get(k) { try { return sessionStorage.getItem(k) || ""; } catch (e) { return ""; } },
	set(k, v) { try { sessionStorage.setItem(k, v); } catch (e) { /* private mode */ } },
	clear() { try { sessionStorage.clear(); } catch (e) { /* private mode */ } },
};

const state = { status: "open", cases: [], selected: "", key: store.get("key"), actor: store.get("actor") };

const REASONS = { cheating: "Читы", idle: "Бездействие", harassment: "Оскорбления", griefing: "Помехи команде", unknown: "?" };
const DETAILS = {
	aimbot: "Аимбот", wallhack: "Wallhack / ESP", triggerbot: "Триггербот", movement: "Спидхак / движение",
	exploit: "Эксплойт", chat: "Чат", voice: "Голос", name: "Ник / спрей",
};
const SEVERITY = { critical: "Критично", high: "Высокий", medium: "Средний", low: "Низкий", closed: "Закрыто" };
const STATUS = { open: "открыто", actioned: "приняты меры", dismissed: "отклонено" };
const FLAGS = {
	eac_violation: ["Нарушение EAC", "bad"],
	sanctioned: ["Уже забанен", "bad"],
	identity_mismatch: ["Чужая EOS-личность", "bad"],
	vote_kicked: ["Кикнут голосованием за читы", "warn"],
	repeat_offender: ["Уже наказывался", "warn"],
	killed_reporters: ["Убивал авторов жалоб", "warn"],
	many_matches: ["Жалобы из 3+ матчей", "warn"],
	never_protected: ["Не играл под EAC", ""],
};
const EVENTS = {
	verified: "EAC подтвердил клиент", kicked: "Кик (не вердикт)", not_protected: "Зашёл без античита",
	identity_mismatch: "Чужая EOS-личность", violation: "Нарушение целостности", temp_banned: "Временный бан EAC",
	perm_banned: "Постоянный бан EAC", vote_kicked: "Кикнут голосованием за читы", manual_ban: "Бан модератора",
	manual_clear: "Бан снят", steam_game_ban: "Игровой бан Steam", steam_game_ban_failed: "Игровой бан Steam не прошёл",
	steam_game_unban: "Игровой бан Steam снят",
};
const SOURCES = { menu: "меню", scoreboard: "табло", killer: "после смерти", results: "итоги матча" };

function el(tag, cls, text) {
	const e = document.createElement(tag);
	if (cls) e.className = cls;
	if (text !== undefined && text !== null) e.textContent = String(text);
	return e;
}

function when(iso) {
	if (!iso || iso.startsWith("0001")) return "—";
	const d = new Date(iso);
	const s = (Date.now() - d.getTime()) / 1000;
	const abs = d.toLocaleString("ru-RU", { day: "2-digit", month: "2-digit", year: "2-digit", hour: "2-digit", minute: "2-digit" });
	if (s < 0) return abs;
	if (s < 3600) return Math.max(1, Math.round(s / 60)) + " мин назад";
	if (s < 86400) return Math.round(s / 3600) + " ч назад";
	if (s < 86400 * 14) return Math.round(s / 86400) + " дн назад";
	return abs;
}

async function api(path, opts = {}) {
	const headers = { Authorization: "Bearer " + state.key };
	if (opts.body) {
		headers["Content-Type"] = "application/json";
		headers["X-Frontress-Actor"] = state.actor;
	}
	const r = await fetch(path, { method: opts.body ? "POST" : "GET", headers, body: opts.body ? JSON.stringify(opts.body) : undefined, cache: "no-store" });
	let data = null;
	try { data = await r.json(); } catch (e) { /* not JSON */ }
	if (!r.ok) {
		const err = new Error((data && data.error) || (r.status === 401 ? "Неверный ключ" : "Ошибка " + r.status));
		err.status = r.status;
		throw err;
	}
	return data;
}

// --- login ---------------------------------------------------------------------

function showApp(on) {
	$("login").hidden = on;
	$("app").hidden = !on;
	$("who").hidden = !on;
	$("who-actor").textContent = on ? "Модератор " + state.actor : "";
}

async function enter() {
	const key = $("key").value.trim();
	const actor = $("actor").value.trim();
	$("login-err").textContent = "";
	if (!/^7656119\d{10}$/.test(actor)) {
		$("login-err").textContent = "Нужен ваш SteamID64 (17 цифр, начинается с 7656119).";
		return;
	}
	state.key = key;
	state.actor = actor;
	try {
		await loadCases();
	} catch (e) {
		$("login-err").textContent = e.message;
		return;
	}
	store.set("key", key);
	store.set("actor", actor);
	$("key").value = "";
	showApp(true);
}

// --- queue -----------------------------------------------------------------------

async function loadCases() {
	const data = await api("/admin/v1/reports?limit=300&status=" + encodeURIComponent(state.status));
	state.cases = data.cases || [];
	renderCases();
}

function renderCases() {
	const q = $("find").value.trim().toLowerCase();
	const list = $("cases");
	list.replaceChildren();
	const counts = {};
	let shown = 0;
	for (const c of state.cases) {
		counts[c.severity] = (counts[c.severity] || 0) + 1;
		if (q && !(c.steam_id.includes(q) || (c.name || "").toLowerCase().includes(q))) continue;
		shown++;
		const li = el("li", c.steam_id === state.selected ? "on" : "");
		li.tabIndex = 0;
		li.append(el("span", "bar sev-" + c.severity));
		const mid = el("div");
		mid.append(el("div", "name", c.name || c.steam_id));
		const reasons = Object.entries(c.reasons || {}).sort((a, b) => b[1] - a[1]).map(([k, n]) => (REASONS[k] || k) + " ×" + n).join(", ");
		const bits = [];
		if (c.status !== "open") bits.push(STATUS[c.status] || c.status);
		if (reasons) bits.push(reasons);
		if (c.eac.violations) bits.push("EAC ×" + c.eac.violations);
		bits.push(when(c.last_report));
		mid.append(el("div", "meta", bits.join(" · ")));
		li.append(mid);
		const num = el("div", "num", c.status === "open" ? c.score : c.total_reports);
		num.append(el("small", null, c.status === "open" ? c.reporters + " автор." : "жалоб"));
		li.append(num);
		const open = () => selectCase(c.steam_id);
		li.addEventListener("click", open);
		li.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
		list.append(li);
	}
	$("cases-empty").hidden = shown > 0;
	const stats = $("stats");
	stats.replaceChildren();
	for (const s of ["critical", "high", "medium", "low", "closed"]) {
		if (!counts[s]) continue;
		const span = el("span");
		span.append(el("i", "sev-" + s), document.createTextNode(SEVERITY[s] + ": " + counts[s]));
		stats.append(span);
	}
}

// --- one case ----------------------------------------------------------------------

async function selectCase(id) {
	state.selected = id;
	renderCases();
	$("a-err").textContent = "";
	let d;
	try {
		d = await api("/admin/v1/reports/" + encodeURIComponent(id));
	} catch (e) {
		$("a-err").textContent = e.message;
		return;
	}
	if (state.selected !== id) return;
	renderCase(d);
}

function renderCase(d) {
	$("placeholder").hidden = true;
	$("detail").hidden = false;

	const sev = $("d-sev");
	sev.className = "sev sev-" + d.severity;
	sev.textContent = d.status === "open" ? SEVERITY[d.severity] : STATUS[d.status] || d.status;
	$("d-name").textContent = d.name || "Без имени";
	$("d-steamid").textContent = d.steam_id;
	$("d-profile").href = "https://steamcommunity.com/profiles/" + encodeURIComponent(d.steam_id);
	$("d-score").textContent = d.score;

	const flags = $("d-flags");
	flags.replaceChildren();
	for (const f of d.flags) {
		const [label, kind] = FLAGS[f] || [f, ""];
		flags.append(el("span", "flag " + kind, label));
	}

	const eac = $("d-eac");
	eac.replaceChildren();
	const row = (k, v, bad) => { eac.append(el("dt", null, k), el("dd", bad ? "bad" : "", v)); };
	row("Product User ID", d.eac.product_user_id || "неизвестен");
	row("Последний вход под EAC", when(d.eac.verified_at));
	row("Нарушения (30 дн)", d.eac.violations, d.eac.violations > 0);
	row("Чужая EOS-личность", d.eac.identity_mismatch, d.eac.identity_mismatch > 0);
	row("Кики голосованием", d.eac.vote_kicks, d.eac.vote_kicks > 0);
	row("Заходы без античита", d.eac.not_protected);
	if (d.eac.sanction) {
		const s = d.eac.sanction;
		row("Санкция", (s.until && !s.until.startsWith("0001") ? "до " + new Date(s.until).toLocaleString("ru-RU") : "бессрочно") + " — " + s.reason + " (" + s.by + ")", true);
	}

	const bars = $("d-reasons");
	bars.replaceChildren();
	const reasons = Object.entries(d.reasons || {}).sort((a, b) => b[1] - a[1]);
	const max = Math.max(1, ...reasons.map((r) => r[1]));
	for (const [k, n] of reasons) {
		const r = el("div", "row");
		const track = el("div", "track");
		const fill = el("div", "fill");
		fill.style.width = (100 * n / max) + "%";
		track.append(fill);
		r.append(el("span", null, REASONS[k] || k), track, el("span", "n", n));
		bars.append(r);
	}
	if (!reasons.length) bars.append(el("p", "hint", "Открытых жалоб нет."));
	const chips = $("d-details");
	chips.replaceChildren();
	for (const [k, n] of Object.entries(d.details || {}).sort((a, b) => b[1] - a[1])) {
		chips.append(el("span", "chip", (DETAILS[k] || k) + " ×" + n));
	}

	$("d-count").textContent = "· " + d.open_reports + " открытых из " + d.total_reports;
	const body = $("d-reports");
	body.replaceChildren();
	for (const r of d.reports) {
		const tr = el("tr", r.open ? "" : "old");
		tr.append(el("td", null, when(r.at)));
		const who = el("td");
		who.append(el("span", null, r.reporter_name || r.reporter));
		who.append(el("span", "w", "вес " + r.reporter_weight + (r.party ? " · группа" : "") + (r.source ? " · " + (SOURCES[r.source] || r.source) : "")));
		tr.append(who);
		const why = el("td");
		why.append(el("div", null, REASONS[r.reason_name] || r.reason_name));
		for (const dn of r.details || []) why.append(el("span", "tag", DETAILS[dn] || dn));
		if (r.killed_by) why.append(el("span", "tag", "убил автора ×" + r.killed_by));
		if (r.eac) why.append(el("span", "tag eac", "EAC"));
		tr.append(why);
		tr.append(el("td", "cmt", r.comment || "—"));
		const where = [r.map, r.server, r.match_id ? "матч " + r.match_id : ""].filter(Boolean).join(" · ");
		tr.append(el("td", null, where || "—"));
		body.append(tr);
	}

	const events = $("d-events");
	events.replaceChildren();
	const evs = (d.anticheat.events || []).slice().reverse().filter((e) => e.kind !== "verified").slice(0, 60);
	for (const e of evs) {
		const li = el("li");
		li.append(el("b", null, EVENTS[e.kind] || e.kind));
		const extra = [e.details, e.server, e.enforced ? "" : "только лог"].filter(Boolean).join(" · ");
		if (extra) li.append(document.createTextNode(" — " + extra));
		li.append(el("span", "t", when(e.at)));
		events.append(li);
	}
	if (!evs.length) events.append(el("li", "none", "Античит ничего не сообщал."));

	const hist = $("d-history");
	hist.replaceChildren();
	for (const h of d.resolutions.slice().reverse()) {
		const li = el("li");
		li.append(el("b", null, STATUS[h.outcome] || h.outcome));
		li.append(document.createTextNode(" — " + (h.note || "без заметки") + " (" + h.by + ", жалоб: " + h.reports + ")"));
		li.append(el("span", "t", when(h.at)));
		hist.append(li);
	}
	if (!d.resolutions.length) hist.append(el("li", "none", "Это первое дело."));

	$("a-dismiss").disabled = d.open_reports === 0;
}

async function act(action) {
	const id = state.selected;
	const reason = $("a-reason").value.trim();
	$("a-err").textContent = "";
	if (!id) return;
	if (!reason) {
		$("a-err").textContent = "Укажите причину — она попадёт в журнал.";
		$("a-reason").focus();
		return;
	}
	const hours = parseInt($("a-hours").value, 10) || 0;
	const what = action === "ban" ? (hours ? "Забанить на " + $("a-hours").selectedOptions[0].textContent : "Забанить навсегда") : "Отклонить все открытые жалобы";
	if (!window.confirm(what + "?")) return;
	try {
		await api("/admin/v1/reports/" + encodeURIComponent(id), { body: action === "ban" ? { action, hours, reason } : { action, reason } });
	} catch (e) {
		$("a-err").textContent = e.message;
		return;
	}
	$("a-reason").value = "";
	await loadCases().catch(() => {});
	await selectCase(id);
}

// --- wiring ------------------------------------------------------------------------

document.addEventListener("DOMContentLoaded", () => {
	$("enter").addEventListener("click", enter);
	for (const id of ["key", "actor"]) $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") enter(); });
	$("logout").addEventListener("click", () => { store.clear(); location.reload(); });
	$("refresh").addEventListener("click", () => loadCases().catch((e) => { $("a-err").textContent = e.message; }));
	$("find").addEventListener("input", renderCases);
	for (const b of document.querySelectorAll(".tabs [data-status]")) {
		b.addEventListener("click", () => {
			for (const o of document.querySelectorAll(".tabs [data-status]")) o.classList.toggle("on", o === b);
			state.status = b.dataset.status;
			loadCases().catch((e) => { $("a-err").textContent = e.message; });
		});
	}
	$("a-ban").addEventListener("click", () => act("ban"));
	$("a-dismiss").addEventListener("click", () => act("dismiss"));
	$("actor").value = state.actor;
	if (state.key && state.actor) {
		loadCases().then(() => showApp(true)).catch(() => { store.clear(); state.key = ""; showApp(false); });
	}
});
