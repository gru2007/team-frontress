// team-frontress.org: the language switch and the live numbers.
// English is in the HTML; Russian is below. Live data comes from the worker's
// /api/status and /api/war (src/worker.js).

const ru = {
  nav_war: "Война",
  nav_features: "Что нового",
  nav_join: "Играть и хостить",
  nav_account: "Аккаунт",
  nav_steam: "Steam",
  hero_title: "Одна война. Тысячи боёв.",
  hero_sub: "Мод для Team Fortress 2, где каждый матч — кусочек одной большой войны RED против BLU. Стрельба — та же TF2, которую вы знаете. А вокруг неё всё новое.",
  cta_steam: "Wishlist&Playtest",
  cta_account: "Матчи и ваш аккаунт",
  cta_github: "Код на GitHub",
  hero_eac: "Easy Anti-Cheat вместо VAC. Дорогие читеры, выход там.",
  live_checking: "Проверяем координатор…",
  st_online: "онлайн",
  st_match: "в матче",
  st_queue: "в поиске",
  st_servers: "серверов",
  st_clans: "кланов",
  intro_kicker: "Зачем",
  intro_title: "Каждый матч должен что-то значить",
  intro_p1: "Почти во всех играх с матчмейкингом одна проблема: отыграл, табличка закрылась, всё забыто. А хочется, чтобы комьюнити жило прямо в лобби: зашёл — и видишь знакомых, видно, сколько народу в игре, и на фоне что-то происходит.",
  intro_p2: "Ориентиры — Helldivers 2, Frontline и другие игры с глобальной сюжеткой, где действия игроков двигают историю.",
  war_kicker: "Война в прямом эфире",
  war_title: "Фронт прямо сейчас",
  war_sub: "Все матчи складываются в одну кампанию на карте секторов. Победа двигает наступление, поражение откатывает.",
  war_offline: "Карта войны сейчас недоступна. Она появится здесь, когда координатор будет на связи.",
  war_ops: "Наступления",
  war_feed: "Сводка с фронта",
  war_loading: "Загрузка…",
  war_hint: "Тяните, чтобы повернуть планету. Нажмите на сектор, чтобы увидеть, за что там бой.",
  w1_t: "Сектор — это операция",
  w1_p: "Не одна карта, а цепочка: прорыв на 5CP, продвижение на Payload, штурм на A/D. Прошёл последний этап — сектор твой.",
  w2_t: "Ширина войны зависит от онлайна",
  w2_p: "Мало людей — один фронт, больше — несколько. Даже восемь человек должны чувствовать, что воюют, а не сидят по пустым серверам.",
  w3_t: "Выбери сторону",
  w3_p: "В начале кампании выбираешь RED или BLU и до конца остаёшься с ними. Новая кампания начинается через сутки после конца прошлой.",
  w4_t: "Условия и приказы",
  w4_p: "Бои бывают без критов, с низкой гравитацией, с крюками или со странными волнами респавна. Бой по приказу даёт бонус к опыту — выиграл ты или нет.",
  f_kicker: "Что нового",
  f_title: "Всё, что вокруг стрельбы",
  f1_t: "Свой координатор",
  f1_p: "Координатора Valve нет в SDK, поэтому мы написали свой. Пати никогда не разбиваются, команды выравниваются по размеру и рейтингу, а матч собирается, только когда для него есть свободный сервер.",
  f2_t: "Регионы с честным пингом",
  f2_p: "RU/Asia, EU/Germany, NA/Canada. Игра сама пингует наши сервера, и матч уходит только туда, где у всех нормальный пинг. Никаких «из России в Канаду».",
  f3_t: "Матчи без перерыва",
  f3_p: "После карты лобби едет на следующую. Не надо заново искать и терять тиммейтов.",
  f4_t: "Кланы, которые что-то значат",
  f4_p: "Играешь — клан качается, с клановыми быстрее. Перки: бонус к опыту, флаг на карте войны, еженедельный контракт, свой приказ.",
  f5_t: "Живое лобби",
  f5_p: "Общий, клановый и пати-чат в главном меню. Видно, кто онлайн и чем занят. Кнопка «ищу отряд».",
  f6_t: "Квесты и карьера",
  f6_p: "Три задания в день и одно на неделю: убийства, урон, лечение, победы, классы. Опыт, уровни, итоги матча.",
  f7_t: "Ровные команды, без ранкеда",
  f7_p: "Ранговых матчей нет. Elo нужен только для равных команд: все начинают с 1500 и сравниваются с другой командой.",
  f8_t: "Новый интерфейс",
  f8_p: "Все меню и HUD переделаны с нуля на RmlUi: главная, игра, война, клан, карьера, квесты, шкафчик, итоги. Два HUD на выбор.",
  f9_t: "Easy Anti-Cheat",
  f9_p: "EAC вместо VAC. Он ещё на тестах, но проблема читеров в TF2 острая, и попробовать её решить стоит.",
  ev_kicker: "Вечер во Frontress",
  ev_text: "Заходишь. В чате уже человек двадцать, кто-то из клана зовёт в пати. На карте войны BLU давят на Литейный завод, и сегодня там бой по приказу. Жмёшь «Играть», через минуту ты на Payload — это этап наступления. Выиграли — наступление сдвинулось, в ленте строчка, клан получил опыт, квест закрылся. Следующая карта подъезжает сама.",
  ev_punch: "Каждый матч что-то значит. Вот и вся идея.",
  tc2_kicker: "Основа",
  tc2_title: "Спасибо, Team Comtress 2",
  tc2_p: 'Team Frontress — форк <a href="https://github.com/mastercomfig/tc2">Team Comtress 2</a>: они чинят баги TF2, ускоряют её и добавляют удобства. От них у нас:',
  tc2_1: "Сотни исправлений багов",
  tc2_2: "Быстрые меню и починенный многопоточный рендер",
  tc2_3: "Сабтик-прицеливание: выстрел учитывает прицел между тиками",
  tc2_4: "Переделанный прицел, обводки, исправленный FOV",
  tc2_5: "Десятки исправлений в MvM",
  no_kicker: "Честно",
  no_title: "Чего пока нет",
  no_1: "Ранговых матчей. Только казуал и война.",
  no_2: "Большого онлайна. Проект маленький, и всё сделано так, чтобы было интересно даже вдесятером.",
  no_3: "Готового античита. EAC подключён, но в продакшене ещё не проверен.",
  no_4: "Своего геймплея помимо TC2 пока мало. Будет.",
  j_kicker: "Присоединяйтесь",
  j_title: "Играть, хостить, делать",
  j1_t: "Игрокам",
  j1_p: "Добавьте Team Frontress в список желаемого в Steam, чтобы узнать о запуске. Ваши матчи, статистика, задания и можно ли вам искать матч — на сайте матчей и в игре в «Карьере».",
  j2_t: "Владельцам серверов",
  j2_p: "Любой выделенный сервер может войти в пул: получает ключ, подключается к координатору и получает матчи по мере сборки. У сообществ свои ключи. Инструкция — в репозитории.",
  j2_cta: "Как поднять сервер",
  j3_t: "Разработчикам",
  j3_p: "Игра, координатор (Go) и интерфейс открыты. Помощь нужна везде: баги, тесты матчмейкинга, сервера, UI, карты. Перед большим PR напишите нам.",
  foot_mm: "Сайт матчей",
  legal: "Valve, Steam, Source, Team Fortress и связанные названия, логотипы и материалы — товарные знаки Valve Corporation. Team Frontress — проект сообщества, не спонсируется, не одобрен, не лицензирован Valve Corporation и не связан с ней.",
};

// Words the live parts are built from.
const words = {
  en: {
    live_on: "Coordinator online",
    live_off: "Coordinator offline",
    campaign: (name, day) => `${name}${day ? `, day ${day}` : ""}`,
    won: (side) => `${side} won the campaign`,
    next: (when) => `The next campaign starts ${when}`,
    no_ops: "No offensive is on. The next one opens when players are around.",
    no_events: "Nothing on the front yet.",
    stage_of: (a, b) => `Stage ${a} of ${b}`,
    from: (name) => `from ${name}`,
    momentum: (a, b) => `momentum ${a}/${b}`,
    order: "Order",
    hq: "HQ",
    battles: (n) => `${n} battle${n === 1 ? "" : "s"}`,
    sector: "Sector",
    hq_sector: "Headquarters",
    held: (side) => `Held by ${side}`,
    attacked: (side) => `${side} offensive`,
    quiet: "No fighting here right now.",
    next_battle: (mode, map) => `Next battle: ${mode}${map ? ` on ${map}` : ""}`,
    order_note: (xp) => `On the war's order: +${xp} XP for everyone in it, win or lose.`,
    stage: { skirmish: "Skirmish", breakthrough: "Breakthrough", advance: "Advance", assault: "Assault" },
    cond: { nocrits: "No crits", lowgrav: "Low gravity", longwaves: "Stretched supply", quickwaves: "Reserves close", grapples: "Captured grapples" },
    ev: {
      campaign_started: () => "The campaign began",
      campaign_won: (s) => `${s} won the campaign`,
      front_opened: (s, n) => `${s} opened an offensive on ${n}`,
      stage_won: (s, n, k) => `${s} won the ${k} stage at ${n}`,
      stage_lost: (s, n) => `${s} held ${n}`,
      node_captured: (s, n) => `${s} took ${n} and the front moved`,
      offensive_broken: (s, n) => `The ${s} offensive on ${n} broke`,
    },
    ago: (s) => (s < 60 ? "now" : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`),
    in: (s) => (s < 3600 ? `in ${Math.max(1, Math.round(s / 60))} min` : `in ${Math.round(s / 3600)} h`),
  },
  ru: {
    live_on: "Координатор на связи",
    live_off: "Координатор недоступен",
    campaign: (name, day) => `${name}${day ? `, день ${day}` : ""}`,
    won: (side) => `${side} выиграли кампанию`,
    next: (when) => `Новая кампания начнётся ${when}`,
    no_ops: "Наступлений сейчас нет. Новое откроется, когда соберутся игроки.",
    no_events: "На фронте пока тихо.",
    stage_of: (a, b) => `Этап ${a} из ${b}`,
    from: (name) => `из «${name}»`,
    momentum: (a, b) => `натиск ${a}/${b}`,
    order: "Приказ",
    hq: "Штаб",
    battles: (n) => `боёв: ${n}`,
    sector: "Сектор",
    hq_sector: "Штаб",
    held: (side) => `Держат ${side}`,
    attacked: (side) => `Наступление ${side}`,
    quiet: "Здесь сейчас тихо.",
    next_battle: (mode, map) => `Следующий бой: ${mode}${map ? `, ${map}` : ""}`,
    order_note: (xp) => `По приказу: +${xp} опыта всем участникам, выиграли или нет.`,
    stage: { skirmish: "Стычка", breakthrough: "Прорыв", advance: "Продвижение", assault: "Штурм" },
    cond: { nocrits: "Без критов", lowgrav: "Низкая гравитация", longwaves: "Растянутое снабжение", quickwaves: "Резервы рядом", grapples: "Трофейные крюки" },
    ev: {
      campaign_started: () => "Кампания началась",
      campaign_won: (s) => `${s} выиграли кампанию`,
      front_opened: (s, n) => `${s} начали наступление на «${n}»`,
      stage_won: (s, n, k) => `${s} прошли этап «${k}» у «${n}»`,
      stage_lost: (s, n) => `${s} отбили атаку на «${n}»`,
      node_captured: (s, n) => `${s} захватили «${n}» — фронт сдвинулся`,
      offensive_broken: (s, n) => `Наступление ${s} на «${n}» сорвано`,
    },
    ago: (s) => (s < 60 ? "сейчас" : s < 3600 ? `${Math.floor(s / 60)} мин` : s < 86400 ? `${Math.floor(s / 3600)} ч` : `${Math.floor(s / 86400)} д`),
    in: (s) => (s < 3600 ? `через ${Math.max(1, Math.round(s / 60))} мин` : `через ${Math.round(s / 3600)} ч`),
  },
};

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const sideName = (s) => (s === "red" ? "RED" : s === "blu" ? "BLU" : "");
const sideHTML = (s) => (s ? `<span class="${s === "red" ? "r" : "b"}">${sideName(s)}</span>` : "");

let lang = "en";
let lastStatus = null;
let lastWar = null;
let planet = null; // the globe (planet.js), or null for the flat map
let chosen = "";

// Language

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch {
    return null;
  }
  return null;
}

function setLang(next) {
  lang = next === "ru" ? "ru" : "en";
  document.documentElement.lang = lang;
  for (const el of document.querySelectorAll("[data-i18n]")) {
    if (el.dataset.en === undefined) el.dataset.en = el.innerHTML;
    const key = el.dataset.i18n;
    el.innerHTML = lang === "ru" && ru[key] ? ru[key] : el.dataset.en;
  }
  $("lang").textContent = lang === "ru" ? "EN" : "RU";
  document.title = lang === "ru" ? "Team Frontress: одна война, тысячи боёв" : "Team Frontress: one war, thousands of battles";
  if (lastStatus) renderStatus(lastStatus);
  if (lastWar) renderWar(lastWar);
  if (chosen) renderDossier(chosen);
}

$("lang").addEventListener("click", () => {
  setLang(lang === "ru" ? "en" : "ru");
  store("lang", lang);
});

const wanted = new URLSearchParams(location.search).get("lang") || store("lang") || (navigator.language || "").slice(0, 2);
setLang(["ru", "uk", "be", "kk"].includes(wanted) ? "ru" : "en");

// Live status

async function get(path) {
  const res = await fetch(path, { headers: { accept: "application/json" } });
  const body = await res.json().catch(() => ({ offline: true }));
  return res.ok ? body : { offline: true, ...body };
}

function renderStatus(s) {
  const w = words[lang];
  const dot = $("live-dot");
  const label = $("live-label");
  label.removeAttribute("data-i18n");
  if (s.offline) {
    dot.className = "dot off";
    label.textContent = w.live_off;
    for (const id of ["st-online", "st-match", "st-queue", "st-servers", "st-clans"]) $(id).textContent = "–";
    return;
  }
  dot.className = "dot on";
  label.textContent = w.live_on;
  $("st-online").textContent = s.online;
  $("st-match").textContent = s.in_match;
  $("st-queue").textContent = s.in_queue;
  $("st-servers").textContent = s.servers;
  $("st-clans").textContent = s.clans;
}

// The war

const svgNS = "http://www.w3.org/2000/svg";
function svg(tag, attrs, parent) {
  const el = document.createElementNS(svgNS, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  if (parent) parent.appendChild(el);
  return el;
}

function star(cx, cy, r) {
  const pts = [];
  for (let i = 0; i < 10; i++) {
    const a = (Math.PI / 5) * i - Math.PI / 2;
    const rr = i % 2 ? r * 0.45 : r;
    pts.push(`${(cx + Math.cos(a) * rr).toFixed(1)},${(cy + Math.sin(a) * rr).toFixed(1)}`);
  }
  return pts.join(" ");
}

function drawMap(war) {
  const map = $("war-map");
  map.replaceChildren();
  const byId = new Map(war.sectors.map((n) => [n.id, n]));
  // Fit the view to the sectors (the theater is 1140 x 640, mostly margin).
  if (war.sectors.length) {
    const xs = war.sectors.map((n) => n.x);
    const ys = war.sectors.map((n) => n.y);
    const x0 = Math.min(...xs) - 110;
    const y0 = Math.min(...ys) - 50;
    const w = Math.max(...xs) - Math.min(...xs) + 220;
    const h = Math.max(...ys) - Math.min(...ys) + 120;
    map.setAttribute("viewBox", `${x0} ${y0} ${w} ${h}`);
  }
  const links = svg("g", {}, map);
  for (const l of war.links) {
    const a = byId.get(l.a);
    const b = byId.get(l.b);
    if (!a || !b) continue;
    svg("line", { x1: a.x, y1: a.y, x2: b.x, y2: b.y, class: `lnk ${l.side}` }, links);
  }
  const w = words[lang];
  for (const n of war.sectors) {
    const g = svg("g", { class: `node ${n.owner}` }, map);
    const r = n.hq ? 30 : 22;
    if (n.attacker) svg("circle", { cx: n.x, cy: n.y, r, class: "ring" }, g);
    svg("circle", { cx: n.x, cy: n.y, r, class: "base" }, g);
    if (n.hq) {
      const t = svg("text", { x: n.x, y: n.y, class: "hqmark" }, g);
      t.textContent = "★";
    }
    if (n.id === war.order) svg("polygon", { points: star(n.x + r * 0.85, n.y - r * 0.85, 13), class: "star" }, g);
    const label = svg("text", { x: n.x, y: n.y + r + 24 }, g);
    label.textContent = n.name;
    const title = svg("title", {}, g);
    title.textContent = `${n.name} — ${sideName(n.owner)}${n.id === war.order ? ` · ${w.order}` : ""}`;
  }
}

function renderWar(war) {
  const w = words[lang];
  const empty = $("war-empty");
  const ops = $("war-ops");
  const feed = $("war-feed");
  const down = war.offline || !war.running;
  $("planet").style.display = down ? "none" : "";
  document.querySelector(".war-balance").style.display = down ? "none" : "";
  if (down) {
    empty.hidden = false;
    $("war-map").replaceChildren();
    $("dossier").hidden = true;
    ops.innerHTML = `<li class="muted">${esc(w.no_ops)}</li>`;
    feed.innerHTML = `<li class="muted">${esc(w.no_events)}</li>`;
    return;
  }
  empty.hidden = true;

  const title = $("war-title");
  title.removeAttribute("data-i18n");
  title.textContent = w.campaign(war.name, war.day);
  if (war.winner) {
    const sub = $("war-sub");
    sub.removeAttribute("data-i18n");
    const left = war.next_campaign_at - Date.now() / 1000;
    sub.textContent = w.won(sideName(war.winner)) + (left > 0 ? `. ${w.next(w.in(left))}.` : ".");
  }

  const total = war.red_fronts + war.blu_fronts;
  $("war-red").textContent = war.red_fronts;
  $("war-blu").textContent = war.blu_fronts;
  $("war-bar").style.width = `${total ? (war.red_fronts / total) * 100 : 50}%`;

  if (planet) {
    planet.setWar(war);
    const pick = war.sectors.some((n) => n.id === chosen) ? chosen : war.order || (war.ops[0] && war.ops[0].node) || "";
    if (pick && pick !== planet.selected) planet.select(pick, pick !== chosen);
    else renderDossier(chosen);
  } else {
    drawMap(war);
  }

  ops.innerHTML = war.ops.length
    ? war.ops
        .map((o) => {
          const plan = o.plan.length ? o.plan : Array.from({ length: o.stages }, () => "");
          const steps = plan.map((_, i) => `<i class="${i + 1 < o.stage ? "done" : i + 1 === o.stage ? "now" : ""}"></i>`).join("");
          const kind = w.stage[o.plan[o.stage - 1]] || "";
          const tags = [
            o.order ? `<span class="tag order">${esc(w.order)} +XP</span>` : "",
            o.hq ? `<span class="tag">${esc(w.hq)}</span>` : "",
            ...o.conditions.map((c) => `<span class="tag">${esc(w.cond[c] || c)}</span>`),
          ].join("");
          return `<li class="op ${o.attacker}">
            <b>${sideName(o.attacker)} → ${esc(o.name)}</b>
            <small>${esc(w.stage_of(o.stage, o.stages))}${kind ? ` · ${esc(kind)}` : ""}${o.map ? ` · ${esc(o.map)}` : ""}</small>
            <div class="steps">${steps}</div>
            <small>${o.from ? esc(w.from(o.from)) + " · " : ""}${esc(w.momentum(o.momentum, o.momentum_max))} · ${esc(w.battles(o.battles))}</small>
            ${tags}
          </li>`;
        })
        .join("")
    : `<li class="muted">${esc(w.no_ops)}</li>`;

  feed.innerHTML = war.events.length
    ? war.events
        .map((e) => {
          const make = w.ev[e.kind];
          if (!make) return "";
          const text = make(sideHTML(e.side), esc(e.name), esc(w.stage[e.stage_kind] || e.stage_kind));
          return `<li><span>${text}</span><span class="when">${esc(w.ago(e.ago))}</span></li>`;
        })
        .join("")
    : `<li class="muted">${esc(w.no_events)}</li>`;
}

// The chosen sector, beside the planet.
function renderDossier(id) {
  const box = $("dossier");
  const war = lastWar;
  const n = war && war.sectors ? war.sectors.find((x) => x.id === id) : null;
  if (!n) {
    box.hidden = true;
    return;
  }
  const w = words[lang];
  const op = war.ops.find((o) => o.node === id);
  let html = `<div class="dim">${esc(n.hq ? w.hq_sector : w.sector)}</div><h4>${esc(n.name)}</h4>`;
  html += `<p><span class="owner ${n.owner}">${esc(w.held(sideName(n.owner)))}</span></p>`;
  if (op) {
    const plan = op.plan.length ? op.plan : Array.from({ length: op.stages }, () => "");
    html += `<p><span class="owner ${op.attacker}">${esc(w.attacked(sideName(op.attacker)))}</span>${op.from ? ` · ${esc(w.from(op.from))}` : ""}</p>`;
    html += `<div class="steps">${plan.map((_, i) => `<i class="${i + 1 < op.stage ? "done" : i + 1 === op.stage ? "now" : ""}"></i>`).join("")}</div>`;
    html += `<div class="stage-names">${plan.map((k, i) => `<span class="${i + 1 === op.stage ? "now" : ""}">${esc(w.stage[k] || "")}</span>`).join("")}</div>`;
    html += `<p style="margin-top:8px">${esc(w.next_battle((op.mode || "").toUpperCase(), op.map))}</p>`;
    html += `<p>${esc(w.momentum(op.momentum, op.momentum_max))} · ${esc(w.battles(op.battles))}</p>`;
    const tags = op.conditions.map((c) => `<span class="tag">${esc(w.cond[c] || c)}</span>`).join("");
    if (tags) html += `<div>${tags}</div>`;
    if (op.order) html += `<p style="margin-top:8px"><span class="tag order">${esc(w.order)}</span> ${esc(w.order_note(war.order_xp))}</p>`;
  } else {
    html += `<p>${esc(w.quiet)}</p>`;
  }
  box.innerHTML = html;
  box.hidden = false;
}

async function bootPlanet() {
  try {
    const { WarPlanet } = await import("/planet.js");
    planet = WarPlanet.create($("planet"), {
      onSelect: (id) => {
        chosen = id;
        renderDossier(id);
      },
    });
  } catch (err) {
    console.warn("no planet", err);
  }
  $("planet").classList.add(planet ? "globe" : "is-flat");
  if (lastWar) renderWar(lastWar);
}

async function refresh() {
  const [status, war] = await Promise.all([get("/api/status"), get("/api/war")]).catch(() => [{ offline: true }, { offline: true }]);
  lastStatus = status;
  lastWar = war;
  renderStatus(status);
  renderWar(war);
}

bootPlanet();
refresh();
setInterval(() => {
  if (!document.hidden) refresh();
}, 30000);
