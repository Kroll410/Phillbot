(() => {
"use strict";
/* Інтерфейс СабДею. Стан живе в Go (subday_state.json); тут — показ і таблиці.
   Список куль для Marbles і дані для OBS рахує Go (aggregate.go) — тут лише попередній перегляд. */

const api = () => window.go.main.App;
let S = null;            // знімок стану з Go
let inited = false;
let tab = "summary", sortKey = { summary:["points",-1], entries:["ts",-1] }, freshIds = new Set();
let tableDirty = false;

/* ---------- helpers ---------- */
const $ = id => document.getElementById(id);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const fmtTime = ms => { ms = Math.max(0, ms); const t = Math.ceil(ms/1000), m = Math.floor(t/60), s = t%60; return `${String(m).padStart(2,"0")}:${String(s).padStart(2,"0")}`; };
const fmtClock = ts => new Date(ts).toLocaleTimeString("uk-UA",{hour:"2-digit",minute:"2-digit",second:"2-digit"});
const norm = t => String(t).toLowerCase().replace(/\s+/g," ").trim().replace(/[.!?,;:…]+$/,"").trim();
function toast(msg, ms=2600){ const t=$("toast"); t.textContent=msg; t.classList.add("show"); clearTimeout(t._h); t._h=setTimeout(()=>t.classList.remove("show"),ms); }
function confirmBox(text, yes="Так", no="Скасувати"){ return new Promise(res=>{ $("modalText").textContent=text; $("modalYes").textContent=yes; $("modalNo").textContent=no; $("modal").classList.add("show");
  const done=v=>{ $("modal").classList.remove("show"); $("modalYes").onclick=$("modalNo").onclick=null; res(v); };
  $("modalYes").onclick=()=>done(true); $("modalNo").onclick=()=>done(false); }); }
async function call(fn, ...args){
  try{ return await api()[fn](...args); }
  catch(e){ toast("Помилка: " + (e && e.message || e), 4000); throw e; }
}
function setState(snap){ if(snap && snap.settings) { S = snap; renderAll(); } }

/* ---------- дані: поточний збір або збір, вибраний для перегляду ---------- */
let view = null;        // null — поточний збір; {n} — завершений збір (лише перегляд)
let pastStream = null;  // StreamFile минулого стріму, який переглядаємо (null — поточний стрім)
const LIVE = () => ({entries: S.entries, settings: S.settings, live: true});
function streamColls(){ return pastStream ? (pastStream.collections || []) : (S.past || []); }
function viewColl(){ return view ? streamColls().find(c => c.n === view.n) : null; }
function D(){ const c = viewColl(); return c ? {entries: c.entries || [], settings: c.settings || S.settings, live: false, coll: c} : LIVE(); }

/* ---------- бали ---------- */
function pointsOf(e, st = D().settings){
  if(!e.tier) return Number(st.nonSubPoints)||0;
  return (Number(st.mult[String(e.tier)])||0) * Math.max(1, Number(e.months)||1);
}
// «діюче» замовлення кожного чатерса (останнє / перше / усі), включно з прихованими — лише вони є в таблиці
function effectiveSet(d = D()){
  const mode = d.settings.dupMode;
  if(mode==="all") return new Set(d.entries.map(e=>e.id));
  const byUser = new Map();
  for(const e of [...d.entries].sort((a,b)=>a.ts-b.ts)){
    const k = (e.userId || e.login || e.user).toLowerCase();
    if(mode==="first"){ if(!byUser.has(k)) byUser.set(k,e.id); } else byUser.set(k,e.id);
  }
  return new Set(byUser.values());
}
// рахується «діюче» замовлення, якщо воно не приховане
function countedSet(d = D()){ const eff = effectiveSet(d); return new Set(d.entries.filter(e=>eff.has(e.id) && !e.excluded).map(e=>e.id)); }
function aggregate(d = D()){
  const counted = countedSet(d), groups = new Map();
  for(const e of d.entries){
    if(!counted.has(e.id)) continue;
    const k = norm(e.text); if(!k) continue;
    if(!groups.has(k)) groups.set(k,{key:k, variants:new Map(), points:0, count:0, users:[]});
    const g = groups.get(k), p = pointsOf(e, d.settings);
    g.points += p; g.count++; g.users.push({name:e.user, p, login:e.login, color:e.color, badges:e.badges});
    g.variants.set(e.text.trim(), (g.variants.get(e.text.trim())||0)+1);
  }
  return [...groups.values()].map(g=>{
    const name = [...g.variants.entries()].sort((a,b)=>b[1]-a[1])[0][0]; // при рівності — перший за часом варіант
    g.users.sort((a,b)=>b.p-a.p);
    return {...g, name};
  });
}
// кількість куль поточного збору = сума балів (кожен бал — одна куля)
function marbleInfo(){
  const groups = aggregate(LIVE()).filter(g=>g.points>0);
  const total = groups.reduce((a,g)=>a+g.points,0);
  return {count: total, total, groups: groups.length};
}

/* ---------- стрім і його збори ---------- */
let streams = [];
const fmtHM = ts => ts ? new Date(ts).toLocaleTimeString("uk-UA",{hour:"2-digit",minute:"2-digit"}) : "—";
function fmtStream(m){
  const d = new Date(m.startedAt), today = new Date();
  const day = d.toDateString() === today.toDateString() ? "Сьогодні" : d.toLocaleDateString("uk-UA",{day:"numeric",month:"long"});
  const n = m.collections, word = n%10===1&&n%100!==11 ? "збір" : [2,3,4].includes(n%10)&&![12,13,14].includes(n%100) ? "збори" : "зборів";
  return `${day}, ${fmtHM(m.startedAt)} · ${n} ${word}${m.current?" (поточний)":""}`;
}
function ordersOf(c){ return countedSet({entries: c.entries || [], settings: c.settings || S.settings}).size; }
async function loadStreams(){ try{ streams = await api().ListStreams() || []; }catch(e){ streams = []; } renderStreamBar(); }
function renderStreamBar(){
  if(!S || !S.stream) return;
  const sel = $("streamSel"), curId = pastStream ? pastStream.id : S.stream.id;
  const list = streams.length ? streams : [{id:S.stream.id, startedAt:S.stream.startedAt, collections:(S.past||[]).length + (S.entries.length?1:0), current:true}];
  if(document.activeElement !== sel) sel.innerHTML = list.map(m => `<option value="${esc(m.id)}" ${m.id===curId?"selected":""}>${esc(fmtStream(m))}</option>`).join("");
  const chips = streamColls().map(c => `<button class="cchip${view&&view.n===c.n?" active":""}" data-n="${c.n}"><b>Збір ${c.n}</b><span>${fmtHM(c.startedAt)} · ${ordersOf(c)} замовл.</span></button>`);
  if(!pastStream){
    const st = S.session.status, n = S.collN || (S.past||[]).length + 1;
    const sub = st==="running" ? "іде зараз" : st==="paused" ? "пауза" : st==="finished" ? `${countedSet(LIVE()).size} замовл.` : "ще не почато";
    chips.push(`<button class="cchip live${!view?" active":""}${st==="running"?" on":""}" data-n="live"><b>Збір ${n}</b><span>${sub}</span></button>`);
  }
  $("collChips").innerHTML = chips.join("") || `<span class="muted">У цьому стрімі ще немає зборів.</span>`;
  $("viewNote").classList.toggle("hidden", D().live);
  if(!D().live){ const c = D().coll; $("viewNote").innerHTML = `Перегляд: <b>Збір ${c.n}</b> · ${fmtHM(c.startedAt)}–${fmtHM(c.finishedAt)} · лише перегляд <a href="#" id="backLive">До поточного збору</a>`; }
}
$("collChips").addEventListener("click", e => {
  const b = e.target.closest(".cchip"); if(!b) return;
  view = b.dataset.n === "live" ? null : {n: Number(b.dataset.n)};
  editMode = false; openGroups.clear(); renderAll();
});
$("streamSel").addEventListener("change", async () => {
  const id = $("streamSel").value;
  if(!id || id === S.stream.id){ pastStream = null; view = null; }
  else {
    try{ pastStream = await api().GetStream(id); }catch(e){ toast("Не вдалося відкрити стрім"); return; }
    const cs = pastStream.collections || []; view = cs.length ? {n: cs[cs.length-1].n} : null;
  }
  editMode = false; openGroups.clear(); renderAll();
});
$("streamSel").addEventListener("focus", loadStreams);
document.addEventListener("click", e => { if(e.target.id==="backLive"){ e.preventDefault(); pastStream = null; view = null; renderAll(); } });

/* ---------- auth / header ---------- */
let lastAuthStatus = "";
// вітання стрімера після входу в акаунт
function showWelcome(name){
  const w = $("welcome"); $("wName").textContent = name;
  w.classList.remove("hidden","out","play"); void w.offsetWidth; w.classList.add("play");
  clearTimeout(w._t1); clearTimeout(w._t2);
  const hide = () => { w.classList.add("out"); w._t2 = setTimeout(() => w.classList.add("hidden"), 450); };
  w._t1 = setTimeout(hide, 2600); w.onclick = hide;
}
function renderAuth(){
  const a = S.auth || {status:"checking"};
  const ok = a.status === "ok";
  $("authScreen").classList.toggle("hidden", ok);
  $("mainScreen").classList.toggle("hidden", !ok);
  for(const id of ["authNoClient","authLogin","authPending","authChecking"]) $(id).classList.add("hidden");
  if(a.status==="no_client") $("authNoClient").classList.remove("hidden");
  else if(a.status==="need_login" || a.status==="error"){
    $("authLogin").classList.remove("hidden");
    $("authMsg").textContent = a.message || ""; $("authMsg").classList.toggle("hidden", !a.message);
  }
  else if(a.status==="pending"){ $("authPending").classList.remove("hidden"); $("authCode").textContent = a.code || "—"; $("authUri").textContent = a.uri || ""; $("authUri").dataset.url = a.uri || ""; }
  else if(!ok) $("authChecking").classList.remove("hidden");

  const c = S.conn || {status:"off", text:"Не підключено"};
  $("connDot").className = "dot " + (c.status==="on"?"on":c.status==="wait"?"wait":"");
  $("connDot").title = c.text;
  $("userBox").innerHTML = ok && a.user ? `<b class="uname">${esc(a.user.display_name)}</b><button class="small" id="logoutBtn">Вийти</button>` : "";
  if(ok && a.user && lastAuthStatus !== "ok") showWelcome(a.user.display_name);
  lastAuthStatus = a.status;
}

/* ---------- таймер ---------- */
function remaining(){ const s=S.session; return s.status==="running" ? s.endsAt-Date.now() : s.remainingMs; }
function renderTimer(){
  if(!S) return;
  const s = S.session, r = remaining(), el = $("timer");
  el.textContent = fmtTime(s.status==="finished"?0:r);
  el.className = "timer " + s.status + (s.status==="running" && r<=30000 ? " low" : "");
  $("statusText").textContent = {idle:"Очікування старту", running:"Збір іде — замовлення записуються", paused:"Пауза — замовлення не записуються", finished:"Збір завершено"}[s.status] || "";
  $("startBtn").disabled = s.status==="running";
  $("startBtn").textContent = s.status==="paused" ? "Продовжити" : "Старт";
  $("pauseBtn").disabled = s.status!=="running";
  $("stopBtn").disabled = !(s.status==="running"||s.status==="paused");
  $("minusBtn").disabled = s.status==="finished";
  $("timer").parentElement.classList.toggle("live", s.status==="running");
}
setInterval(renderTimer, 250);

/* ---------- нагороди (плитки як на Twitch) ---------- */
const POINTS_ICON = `<svg viewBox="0 0 20 20" fill="currentColor" aria-hidden="true"><path d="M10 6a4 4 0 0 1 4 4h-2a2 2 0 0 0-2-2V6z"/><path fill-rule="evenodd" d="M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0zm-2 0a6 6 0 1 1-12 0 6 6 0 0 1 12 0z" clip-rule="evenodd"/></svg>`;
const DEFAULT_REWARD_IMG = "https://static-cdn.jtvnw.net/custom-reward-images/default-2.png";
function rewardImg(r){ const im = r.image || r.default_image; return (im && (im.url_2x || im.url_1x)) || DEFAULT_REWARD_IMG; }
function rewardTile(r, opts={}){
  const tags = [];
  if(r.is_enabled===false) tags.push(`<span class="rtag off">вимкнена</span>`);
  if(r.is_user_input_required===false) tags.push(`<span class="rtag" title="Глядачі не вводять текст — замовлення не записуватимуться">без тексту</span>`);
  const tag = opts.button ? "button" : "div";
  return `<${tag} class="rtile${opts.sel?" sel":""}${r.is_enabled===false?" off":""}" ${opts.button?`data-rid="${esc(r.id)}" title="${esc(r.title)}"`:""} style="--rbg:${esc(r.background_color||"#9147ff")}">
    <div class="rimg"><img src="${esc(rewardImg(r))}" alt="" onerror="this.style.visibility='hidden'">${r.cost!=null?`<span class="rcost">${POINTS_ICON}${Number(r.cost).toLocaleString("uk-UA")}</span>`:""}</div>
    <div class="rtitle">${esc(r.title||"(без назви)")}</div>${tags.length?`<div class="rtags">${tags.join("")}</div>`:""}</${tag}>`;
}
function rewardById(id){
  const r = (S.rewards||[]).find(x=>x.id===id); if(r) return r;
  const seen = (S.seenRewards||{})[id];
  return id ? {id, title: (id===S.settings.rewardId && S.settings.rewardTitle) || (seen && seen.title) || id, background_color:"#3a3a40"} : null;
}
// нагорода вибирається в налаштуваннях СабДею; тут — лише попередження, якщо її немає
function renderRewardCurrent(){
  const none = !S.settings.rewardId;
  $("rewardCurrent").classList.toggle("hidden", !none);
  if(none) $("rewardCurrent").innerHTML = `Нагороду не вибрано — замовлення не записуватимуться. <a href="#" id="pickReward">Вибрати</a>`;
}
document.addEventListener("click", e => { if(e.target.id==="pickReward"){ e.preventDefault(); $("subdaySettingsBtn").click(); } });
let pendingReward = null; // вибір у вікні налаштувань до «Зберегти»
function renderRewardGrid(){
  const sel = pendingReward ?? S.settings.rewardId;
  const list = [...(S.rewards||[])].sort((a,b)=>a.cost-b.cost);
  if(sel && !list.some(r=>r.id===sel)){ const r = rewardById(sel); if(r) list.unshift(r); }
  $("rewardGrid").innerHTML = list.length ? list.map(r=>rewardTile(r,{button:true, sel:r.id===sel})).join("")
    : `<div class="hint">Список нагород порожній. Натисніть «Оновити» або виберіть з отриманих погашень нижче.</div>`;
  $("rewardsErr").textContent = S.rewardsErr || "";
  const seen = Object.values(S.seenRewards || {}).sort((a,b)=>b.ts-a.ts);
  $("seenRewards").innerHTML = seen.length ? seen.map(r=>`<div class="reward ${r.id===sel?"bound":""}"><div><div><b>${esc(r.title)}</b> — «${esc(r.sample||"(без тексту)")}» ${esc(r.user)} <span class="muted">(${r.count}×)</span></div><code>${esc(r.id)}</code></div>
      ${r.id===sel?'<span style="color:var(--ok)">✓</span>':`<button class="small" data-bind="${esc(r.id)}">Вибрати</button>`}</div>`).join("")
    : `<div class="hint">Погашень ще не було.</div>`;
}
$("rewardGrid").addEventListener("click", e => { const t = e.target.closest("[data-rid]"); if(!t) return; pendingReward = t.dataset.rid; renderRewardGrid(); });
$("seenRewards").addEventListener("click", e => { const b = e.target.closest("button[data-bind]"); if(!b) return; pendingReward = b.dataset.bind; renderRewardGrid(); });
$("rewardIdUse").onclick = () => { const id = $("rewardIdInput").value.trim(); if(id){ pendingReward = id; renderRewardGrid(); } };
$("rewardRefresh").onclick = async () => { await call("RefreshRewards"); renderRewardGrid(); toast("Список нагород оновлено"); };

/* ---------- OBS ---------- */
const OVERLAYS = [
  ["Таймер збору", "/overlay/timer", "600×220"],
  ["Топ замовлень", "/overlay/top", "520×420"],
  ["Кулі / бали", "/overlay/marbles", "620×80"],
  ["Алерт замовлення", "/overlay/last", "800×180"],
];
let obsKey = "";
function renderObs(){
  const o = S.obs || {};
  const key = JSON.stringify(o);
  if(key === obsKey) return; obsKey = key;
  if(!o.running){
    $("obsStatus").innerHTML = `<span class="err">${esc(o.error || "Сервер для OBS не запущено.")}</span> Змініть порт у загальних налаштуваннях.`;
    $("obsList").innerHTML = ""; return;
  }
  $("obsStatus").textContent = "Браузерні джерела для OBS (розмір — підказка):";
  $("obsList").innerHTML = OVERLAYS.map(([name, p, size])=>`<div class="obsrow"><span><b>${esc(name)}</b> <span class="muted">${size}</span><code>${esc(o.url + p)}</code></span>
    <button class="small" data-copy="${esc(o.url + p)}">Копіювати</button><button class="small" data-open="${esc(p)}" title="Відкрити в браузері">↗</button></div>`).join("")
    + `<div class="obsrow"><span class="muted">Усі параметри й розміри</span><button class="small" data-open="/overlay/index">Усі оверлеї ↗</button></div>`;
}
try{ if(localStorage.getItem("phillbot-obs-open")==="1") $("obsCard").open = true; }catch(e){}
$("obsCard").addEventListener("toggle", () => { try{ localStorage.setItem("phillbot-obs-open", $("obsCard").open ? "1" : "0"); }catch(e){} });
$("obsList").addEventListener("click", async e => {
  const c = e.target.closest("[data-copy]"), o = e.target.closest("[data-open]");
  if(c){ const ok = await call("CopyText", c.dataset.copy); toast(ok ? "URL скопійовано — вставте в OBS → Браузер" : "Не вдалося скопіювати"); }
  if(o) call("OpenOverlay", o.dataset.open);
});

/* ---------- статистика і таблиці ---------- */
function renderStats(){
  const counted = countedSet(), groups = aggregate();
  const cEntries = D().entries.filter(e=>counted.has(e.id));
  $("sEntries").textContent = cEntries.length;
  $("sUsers").textContent = new Set(cEntries.map(e=>(e.userId||e.login).toLowerCase())).size;
  $("sPoints").textContent = groups.reduce((a,g)=>a+g.points,0);
  renderExportBtn();
}

// нік як у чаті Twitch: бейджі + колір ніку (з повідомлення, що прийшло разом із замовленням)
function userTag(u){
  const badges = (u.badges||[]).filter(b=>/^https:\/\//.test(b.url||"")).map(b=>`<img class="cbadge" src="${esc(b.url)}" alt="" title="${esc(b.title||b.setId)}">`).join("");
  const name = u.name || u.user || u.login || "";
  return `<span class="utag">${badges}<span class="cname" style="color:${esc(ChatRender.nameColor(u.login||name, u.color))}">${esc(name)}</span></span>`;
}
// значок підписки каналу для стажу: найбільша версія ≤ місяців для цього тіру (T1: 0…999, T2: 2000+, T3: 3000+)
function subBadgeFor(e){
  if(!e.tier) return "";
  const m = S.subBadges || {}, months = Math.max(1, Number(e.months)||1);
  const pick = base => { let best = -1, url = "";
    for(const [k,u] of Object.entries(m)){
      const v = Number(k); if(!Number.isFinite(v)) continue;
      if(base ? (v < base || v >= base+1000) : v >= 1000) continue;
      const mo = v - base; if(mo <= months && mo > best){ best = mo; url = u; }
    }
    return url; };
  let url = pick(e.tier > 1 ? e.tier*1000 : 0) || pick(0);
  if(!url){ const b = (e.badges||[]).find(b=>b.setId==="subscriber"||b.setId==="founder"); url = b ? b.url : ""; }
  return /^https:\/\//.test(url) ? `<img class="subicon" src="${esc(url)}" alt="" title="Підписка ${months} міс. (T${e.tier})">` : "";
}
let editMode = false;
const openGroups = new Set(); // розгорнуті рядки «Підсумку» // режим редагування у вкладці «Замовлення»

function sortRows(rows, [k,dir]){ return rows.sort((a,b)=>{ const x=a[k], y=b[k]; return (typeof x==="string" ? x.localeCompare(y,"uk") : (x-y))*dir; }); }
function th(label, key, cls=""){ const [k,d]=sortKey[tab]; return `<th class="${cls}" data-sort="${key}">${label}${k===key?(d>0?" ▲":" ▼"):""}</th>`; }

const svg = d => `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
const ICON_EYE = svg('<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>');
const ICON_EYE_OFF = svg('<path d="M17.9 17.9A10.4 10.4 0 0 1 12 19c-6.4 0-10-7-10-7a18.5 18.5 0 0 1 4.1-5.1M9.9 5.2A9.6 9.6 0 0 1 12 5c6.4 0 10 7 10 7a18.6 18.6 0 0 1-2.2 3.2M14.1 14.1a3 3 0 1 1-4.2-4.2"/><path d="M2 2l20 20"/>');
const ICON_TRASH = svg('<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6"/>');
const GRIP = `<span class="grip" draggable="true" title="Перетягніть на інше замовлення, щоб об'єднати їх">⠿</span>`;

/* ---------- drag-n-drop: об'єднання схожих замовлень ---------- */
let drag = null; // {ids, label}
function dragInfo(tr){
  if(!tr) return null;
  if(tr.dataset.key !== undefined){ // Підсумок: уся група
    const ids = S.entries.filter(e=>norm(e.text)===tr.dataset.key).map(e=>e.id);
    return {ids, label: tr.dataset.name, text: tr.dataset.name, key: tr.dataset.key};
  }
  const e = S.entries.find(x=>x.id===tr.dataset.id);
  return e ? {ids:[e.id], label:e.text, text:e.text, key:norm(e.text)} : null;
}
const wrapEl = $("tableWrap");
wrapEl.addEventListener("dragstart", ev => {
  const g = ev.target.closest && ev.target.closest(".grip"); if(!g){ ev.preventDefault(); return; }
  const tr = g.closest("tr"); drag = dragInfo(tr); if(!drag) return;
  tr.classList.add("dragging"); ev.dataTransfer.effectAllowed = "move";
  try{ ev.dataTransfer.setData("text/plain", drag.label); ev.dataTransfer.setDragImage(tr, 20, 16); }catch(e){}
});
wrapEl.addEventListener("dragover", ev => {
  if(!drag) return;
  const tr = ev.target.closest("tbody tr"); if(!tr) return;
  const t = dragInfo(tr); if(!t || t.key===drag.key) return;
  ev.preventDefault(); ev.dataTransfer.dropEffect = "move";
  wrapEl.querySelectorAll("tr.drop").forEach(x=>x!==tr && x.classList.remove("drop")); tr.classList.add("drop");
});
wrapEl.addEventListener("dragleave", ev => { const tr = ev.target.closest && ev.target.closest("tbody tr"); if(tr && !tr.contains(ev.relatedTarget)) tr.classList.remove("drop"); });
function dragEnd(){ drag = null; wrapEl.querySelectorAll("tr.drop,tr.dragging").forEach(x=>x.classList.remove("drop","dragging")); if(tableDirty) renderTable(); }
wrapEl.addEventListener("dragend", dragEnd);
wrapEl.addEventListener("drop", async ev => {
  ev.preventDefault();
  const src = drag, tr = ev.target.closest("tbody tr"), dst = dragInfo(tr);
  dragEnd();
  if(!src || !dst || src.key===dst.key) return;
  const n = src.ids.length;
  const ok = await confirmBox(`Об'єднати «${src.label}» з «${dst.text}»?

Текст ${n===1?"цього замовлення":`${n} замовлень`} буде замінено на «${dst.text}», бали складуться.`, "Об'єднати", "Скасувати");
  if(!ok) return;
  setState(await call("RenameEntries", src.ids, dst.text));
  toast("Об'єднано");
});

function renderTable(){
  const wrap = $("tableWrap");
  $("search").classList.toggle("hidden", tab==="log");
  $("tableHint").classList.toggle("hidden", tab!=="summary" && tab!=="entries");
  // не перемальовувати, поки користувач редагує клітинку
  if(drag || (wrap.contains(document.activeElement) && /INPUT|SELECT/.test(document.activeElement.tagName))){ tableDirty = true; return; }
  tableDirty = false;
  const q = $("search").value.trim().toLowerCase();
  if(tab==="log"){
    const log = (S.log||[]).slice().reverse();
    wrap.innerHTML = log.length ? `<table class="logtable"><thead><tr><th style="width:1%">Час</th><th style="width:1%">Чатерс</th><th>Подія</th></tr></thead><tbody>${
      log.map(l=>`<tr><td class="muted" style="white-space:nowrap">${fmtClock(l.ts)}</td><td style="white-space:nowrap;padding-right:24px">${l.user?userTag({name:l.user, login:l.login, color:l.color, badges:l.badges}):""}</td><td>${esc(l.text)}</td></tr>`).join("")
    }</tbody></table>` : `<div class="empty">Подій поки немає.</div>`;
    return;
  }

  if(tab==="summary"){
    let rows = aggregate().filter(g=>!q || g.name.toLowerCase().includes(q) || g.users.some(u=>u.name.toLowerCase().includes(q)));
    rows = sortRows(rows, sortKey.summary);
    const max = Math.max(1,...rows.map(r=>r.points)), total = rows.reduce((a,r)=>a+r.points,0)||1;
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${D().entries.length?"Нічого не знайдено":"Замовлень поки немає. Виберіть нагороду і натисніть «Почати збір»."}</div>`; return; }
    const counted = countedSet();
    wrap.innerHTML = `<table class="dnd summary"><thead><tr><th class="gripcol"></th><th style="width:44px">#</th>${th("Замовлення","name")}${th("Бали","points","num")}<th style="width:220px">Частка</th>${th("Людей","count","num")}<th style="width:44px"></th></tr></thead><tbody>${
      rows.map((g,i)=>{
        const open = openGroups.has(g.key);
        const main = `<tr class="grow${open?" open":""}" data-key="${esc(g.key)}" data-name="${esc(g.name)}"><td class="gripcol">${D().live?GRIP:""}</td><td class="muted">${i+1}</td><td><b>${esc(g.name)}</b>${g.variants.size>1?` <span class="muted" title="${esc([...g.variants.keys()].join(" | "))}">(${g.variants.size} варіанти)</span>`:""}</td>
        <td class="num pts">${g.points}</td><td><div class="share"><div class="track"><div class="bar" style="width:${Math.round(g.points/max*100)}%"></div></div><span>${(g.points/total*100).toFixed(0)}%</span></div></td>
        <td class="num">${g.count}</td><td class="chev" title="${open?"Згорнути":"Показати замовлення"}"><span></span></td></tr>`;
        if(!open) return main;
        const list = D().entries.filter(e=>counted.has(e.id) && norm(e.text)===g.key).sort((a,b)=>pointsOf(b)-pointsOf(a) || a.ts-b.ts);
        return main + `<tr class="gdetail"><td colspan="7"><table class="subtable"><thead><tr><th>Час</th><th>Чатерс</th><th>Тір сабки</th><th class="num">Має сабку</th><th class="num">Бали</th><th>Замовлення</th></tr></thead><tbody>${
          list.map(e=>`<tr><td class="muted">${fmtClock(e.ts)}</td><td>${userTag(e)}${e.manual?' <span class="muted">(вручну)</span>':""}</td>
            <td>${e.tier?`<span class="tier tier${e.tier}">T${e.tier}</span>`:'<span class="muted">—</span>'}</td>
            <td class="num">${e.tier?`<span class="subcell">${subBadgeFor(e)}${e.months} міс.</span>`:'<span class="muted">—</span>'}</td>
            <td class="num pts">${pointsOf(e)}</td><td>${esc(e.text)}</td></tr>`).join("")
        }</tbody></table></td></tr>`;
      }).join("")
    }</tbody></table>`;
  } else {
    const counted = countedSet();
    const eff = effectiveSet();
    let rows = D().entries.filter(e=>eff.has(e.id)).map(e=>({...e, points:pointsOf(e), counted:counted.has(e.id)}))
      .filter(e=>!q || e.text.toLowerCase().includes(q) || e.user.toLowerCase().includes(q));
    rows = sortRows(rows, sortKey.entries);
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${D().entries.length?"Нічого не знайдено":"Замовлень поки немає."}</div>`; return; }
    wrap.innerHTML = `<table class="dnd"><thead><tr><th class="gripcol"></th>${th("Час","ts")}${th("Чатерс","user")}${th("Тір сабки","tier")}${th("Має сабку","months","num")}${th("Бали","points","num")}${th("Замовлення","text")}<th class="acts">${D().live?`<button class="small editbtn${editMode?" on":""}" data-act="editmode">${editMode?"Готово":"Редагувати"}</button>`:""}</th></tr></thead><tbody>${
      rows.map(e=>`<tr data-id="${e.id}" class="${e.excluded?"off":!e.counted?"replaced":""} ${freshIds.has(e.id)?"fresh":""}" title="${esc(e.excluded?"Приховано — не рахується":!e.counted?"Не рахується: від цього чатерса враховано інше замовлення":(e.source||""))}">
        <td class="gripcol">${D().live?GRIP:""}</td><td class="muted">${fmtClock(e.ts)}</td>
        <td>${userTag(e)}${e.manual?' <span class="muted">(вручну)</span>':""}</td>
        ${editMode ? `<td><select data-f="tier"><option value="0" ${!e.tier?"selected":""}>—</option>${[1,2,3].map(t=>`<option value="${t}" ${e.tier===t?"selected":""}>T${t}</option>`).join("")}</select></td>
        <td class="num"><span class="subcell">${subBadgeFor(e)}<input class="mo" type="number" min="1" data-f="months" value="${e.months||""}" ${!e.tier?"disabled":""}></span></td>`
        : `<td>${e.tier?`<span class="tier tier${e.tier}">T${e.tier}</span>`:'<span class="muted">—</span>'}</td><td class="num">${e.tier?`<span class="subcell">${subBadgeFor(e)}${e.months} міс.</span>`:'<span class="muted">—</span>'}</td>`}
        <td class="num pts">${e.points}</td>
        <td>${editMode ? `<input class="edit" data-f="text" value="${esc(e.text)}">` : `<span class="otext">${esc(e.text)}</span>`}</td>
        <td class="acts">${D().live?`<button class="icon${e.excluded?" on":""}" data-act="toggle" title="${e.excluded?"Показати — знову рахувати":"Приховати — не рахувати (можна повернути)"}">${e.excluded?ICON_EYE_OFF:ICON_EYE}</button>${editMode?`<button class="icon danger" data-act="del" title="Видалити назавжди">${ICON_TRASH}</button>`:""}`:""}</td></tr>`).join("")
    }</tbody></table>`;
  }
}

/* ---------- вкладка «Чат» ---------- */
const CHAT_MAX = 300;
let chatMsgs = [], chatPending = [], chatStick = true, chatLoaded = false, chatOptsKey = "";
const chatWrap = $("chatWrap"), chatList = $("chatList");
const chatOpts = () => ({ time: !S.settings.chat.hideTime, badges: !S.settings.chat.hideBadges });
chatWrap.addEventListener("scroll", () => {
  chatStick = chatWrap.scrollTop + chatWrap.clientHeight >= chatWrap.scrollHeight - 30;
  if(chatStick) $("chatMore").classList.add("hidden");
});
$("chatMore").onclick = () => { chatWrap.scrollTop = chatWrap.scrollHeight; $("chatMore").classList.add("hidden"); };
function chatFlush(){
  if(!chatPending.length || !S) return;
  const add = chatPending.splice(0);
  chatMsgs.push(...add); if(chatMsgs.length > CHAT_MAX) chatMsgs.splice(0, chatMsgs.length - CHAT_MAX);
  const o = chatOpts();
  chatList.append(...add.map(m => ChatRender.render(m, o)));
  while(chatList.children.length > CHAT_MAX) chatList.firstChild.remove();
  if(chatStick) chatWrap.scrollTop = chatWrap.scrollHeight; else $("chatMore").classList.remove("hidden");
}
setInterval(chatFlush, 150);
function chatRerender(){ const o = chatOpts(); chatList.replaceChildren(...chatMsgs.map(m => ChatRender.render(m, o))); chatWrap.scrollTop = chatWrap.scrollHeight; }
function chatDelete(d){
  const ids = new Set(d.ids || []), gone = m => d.all || ids.has(m.id) || (d.userId && m.userId === d.userId);
  chatMsgs = chatMsgs.filter(m => !gone(m)); chatPending = chatPending.filter(m => !gone(m));
  if(d.all){ chatList.replaceChildren(); return; }
  for(const nd of [...chatList.children]) if(ids.has(nd.dataset.id) || (d.userId && nd.dataset.user === d.userId)) nd.remove();
}
function renderChat(){
  const c = S.settings.chat, key = JSON.stringify(c);
  chatList.style.fontSize = c.fontSize + "px";
  if(key !== chatOptsKey){ chatOptsKey = key; chatRerender(); }
  const locked = !!S.chatWindow;
  $("chatLocked").classList.toggle("hidden", !locked); chatWrap.classList.toggle("hidden", locked);
  $("chatWindowBtn").disabled = locked || !(S.obs && S.obs.running);
  $("chatLockIcon").classList.toggle("hidden", !locked);
  $("chatTitle").textContent = "Чат " + (S.auth && S.auth.user ? S.auth.user.display_name : "");
}
$("chatWindowBtn").onclick = () => call("OpenChatWindow");

/* список активних глядачів зліва: найсвіжіші зверху в кожній групі */
const CHAT_GROUPS = [["mod","Модератори"],["vip","VIP"],["follower","Підписники на канал"],["sub","Саби"],["viewer","Глядачі"]];
let chatters = [];
function renderChatters(){
  const box = $("chatUsers"); if(!box) return;
  if(!chatters.length){ box.innerHTML = `<div class="cuempty">Тут з'являться глядачі, які пишуть у чат.</div>`; return; }
  box.innerHTML = CHAT_GROUPS.map(([g, title]) => {
    const list = chatters.filter(c => c.group === g);
    if(!list.length) return "";
    return `<div class="cugroup"><div class="cuhead">${title}<span>${list.length}</span></div>${
      list.map(c => `<div class="curow" title="${esc(c.count)} повідом. · останнє о ${fmtClock(c.lastTs)}">${userTag(c)}</div>`).join("")}</div>`;
  }).join("");
}
$("chatReturn").onclick = () => call("CloseChatWindow");

/* ---------- вкладки-режими: Чат / СабДей / Переглядовий ---------- */
let mode = "subday";
try{ mode = localStorage.getItem("phillbot-mode") || "subday"; }catch(e){}
function showMode(m){
  mode = m === "chat" ? "chat" : "subday";
  try{ localStorage.setItem("phillbot-mode", mode); }catch(e){}
  document.querySelectorAll(".mode-tab[data-mode]").forEach(b => b.classList.toggle("active", b.dataset.mode === mode));
  $("modeChat").classList.toggle("hidden", mode !== "chat");
  $("modeSubday").classList.toggle("hidden", mode !== "subday");
  if(mode === "chat"){ chatStick = true; chatWrap.scrollTop = chatWrap.scrollHeight; }
}
document.querySelectorAll(".mode-tab[data-mode]").forEach(b => b.onclick = () => showMode(b.dataset.mode));
showMode(mode);

/* ---------- налаштування: окремо для кожної вкладки ---------- */
function initInputs(){}
const n = (id, d) => { const v = Number($(id).value); return Number.isFinite(v) && $(id).value !== "" ? v : d; };
async function saveSettings(patch){ setState(await call("SaveSettings", {...S.settings, ...patch})); }
const openModal = id => $(id).classList.add("show"), closeModal = id => $(id).classList.remove("show");
document.querySelectorAll("[data-close]").forEach(b => b.onclick = () => { closeModal(b.dataset.close); pendingReward = null; });
// будь-яке вікно закривається через Esc або клік поза ним (підтвердження = «Скасувати»)
function dismissModal(m){
  if(m.id === "modal"){ $("modalNo").click(); return; }
  closeModal(m.id); pendingReward = null;
}
document.addEventListener("keydown", e => {
  if(e.key !== "Escape") return;
  const open = [...document.querySelectorAll(".modal.show")]; if(!open.length) return;
  e.preventDefault(); dismissModal(open[open.length-1]);
});
document.querySelectorAll(".modal").forEach(m => m.addEventListener("mousedown", e => { if(e.target === m) dismissModal(m); }));

// Загальні: акаунт, порт OBS
$("generalBtn").onclick = () => {
  const u = S.auth && S.auth.user;
  $("accountInfo").innerHTML = u ? `Канал: <b>${esc(u.display_name)}</b> <span class="muted">(${esc(u.login)})</span>` : "Не увійшли";
  $("obsPort").value = S.settings.obsPort; openModal("generalModal");
};
$("generalSave").onclick = async () => { await saveSettings({obsPort: Math.round(n("obsPort",17841))}); closeModal("generalModal"); obsKey = ""; renderObs(); toast("Збережено"); };
$("logoutBtn2").onclick = async () => { if(await confirmBox("Вийти з акаунта Twitch?")){ closeModal("generalModal"); call("Logout"); } };
$("changeClient2").onclick = async () => { if(await confirmBox("Змінити Client ID? Потрібно буде увійти знову.")){ closeModal("generalModal"); call("ChangeClientID"); } };
$("openBase").onclick = () => call("OpenFolder", "");
$("openCsv").onclick = () => call("OpenFolder", "exports");

// СабДей: нагорода + правила балів
$("subdaySettingsBtn").onclick = () => {
  const st = S.settings; pendingReward = null;
  $("m1").value = st.mult["1"]; $("m2").value = st.mult["2"]; $("m3").value = st.mult["3"];
  $("nonSub").value = st.nonSubPoints; $("dupMode").value = st.dupMode;
  renderRewardGrid(); openModal("subdayModal");
  if(!(S.rewards||[]).length) call("RefreshRewards").then(renderRewardGrid);
};
$("settingsReset").onclick = () => { $("m1").value = 1; $("m2").value = 2; $("m3").value = 3; $("nonSub").value = 0; $("dupMode").value = "last"; };
$("subdaySave").onclick = async () => {
  if(pendingReward !== null && pendingReward !== S.settings.rewardId){
    const r = rewardById(pendingReward);
    setState(await call("SelectReward", pendingReward, r ? r.title : ""));
    if(r && r.is_user_input_required === false) toast("Увага: ця нагорода не вимагає тексту — замовлення без тексту ігноруються", 4500);
  }
  await saveSettings({ mult: {"1": Math.max(0,n("m1",1)), "2": Math.max(0,n("m2",2)), "3": Math.max(0,n("m3",3))},
    nonSubPoints: Math.max(0, n("nonSub",0)), dupMode: $("dupMode").value });
  pendingReward = null; closeModal("subdayModal"); toast("Налаштування СабДею збережено");
};

// Чат: вигляд + URL для OBS
$("chatSettingsBtn").onclick = () => {
  const c = S.settings.chat;
  $("chatFont").value = c.fontSize; $("chatFontVal").textContent = c.fontSize + " px";
  $("chatTime").checked = !c.hideTime; $("chatBadges").checked = !c.hideBadges;
  const o = S.obs || {};
  $("chatObs").innerHTML = o.running ? `<div class="obsrow"><span><b>Оверлей чату</b> <span class="muted">420×700, прозорий</span><code>${esc(o.url)}/overlay/chat</code></span>
    <button class="small" data-copy="${esc(o.url)}/overlay/chat">Копіювати</button><button class="small" data-open="/overlay/chat">↗</button></div>
    <div class="hint">Параметри: <code>?fade=30</code> — повідомлення зникають, <code>?hide=nightbot</code> — сховати ботів, <code>?size=20</code> — розмір.</div>` : "Сервер для OBS не запущено.";
  openModal("chatModal");
};
$("chatFont").oninput = () => $("chatFontVal").textContent = $("chatFont").value + " px";
$("chatObs").addEventListener("click", async e => {
  const c = e.target.closest("[data-copy]"), o = e.target.closest("[data-open]");
  if(c){ const ok = await call("CopyText", c.dataset.copy); toast(ok ? "URL скопійовано" : "Не вдалося скопіювати"); }
  if(o) call("OpenOverlay", o.dataset.open);
});
$("chatSave").onclick = async () => {
  await saveSettings({chat: {fontSize: Math.round(n("chatFont",14)), hideTime: !$("chatTime").checked, hideBadges: !$("chatBadges").checked}});
  closeModal("chatModal"); toast("Налаштування чату збережено");
};

let lastStreamKey = "", lastCollKey = "";
$("newStreamBtn").onclick = async () => {
  const st = S.session.status;
  const msg = (st==="running"||st==="paused" ? "Збір ще йде — його буде завершено.\n\n" : "") +
    "Завершити цей стрім і почати новий? Усі збори цього стріму збережено, їх можна переглянути у списку стрімів.";
  if(!(await confirmBox(msg, "Новий стрім", "Скасувати"))) return;
  pastStream = null; view = null;
  setState(await call("NewStream")); loadStreams(); toast("Почато новий стрім");
};
function renderAll(){
  if(!inited){ initInputs(); inited = true; }
  renderAuth();
  if(S.auth && S.auth.status==="ok"){
    if(view && !viewColl()) view = null;             // збір зник (новий стрім) — до поточного
    if(S.stream && S.stream.id !== lastStreamKey){ lastStreamKey = S.stream.id; loadStreams(); }
    const ck = (S.past||[]).length + ":" + S.collN + ":" + S.session.status;
    if(ck !== lastCollKey){ lastCollKey = ck; if(streams.length) loadStreams(); }
    renderStreamBar();
    renderStats(); renderTable(); renderRewardCurrent(); renderObs(); renderChat();
    if($("subdayModal").classList.contains("show")) renderRewardGrid();
    if(!chatLoaded){ chatLoaded = true; api().GetChat().then(m => { chatPending = (m||[]).concat(chatPending); }); }
  }
  renderTimer();
}

/* ---------- події ---------- */
document.addEventListener("click", e => {
  const a = e.target.closest("a[data-url]");
  if(a){ e.preventDefault(); if(a.dataset.url) call("OpenURL", a.dataset.url); }
  if(e.target.id==="logoutBtn") confirmBox("Вийти з акаунта Twitch?").then(ok=>ok && call("Logout"));
});
$("clientIdSave").onclick = () => call("SetClientID", $("clientIdInput").value);
$("clientIdInput").addEventListener("keydown", e=>{ if(e.key==="Enter") $("clientIdSave").click(); });
$("loginBtn").onclick = () => call("Login");
$("cancelLogin").onclick = () => call("CancelLogin");
$("changeClient").onclick = e => { e.preventDefault(); call("ChangeClientID"); };

$("startBtn").onclick = async () => {
  const wasNew = S.session.status !== "paused", hadOrders = S.entries.length > 0;
  setState(await call("StartTimer"));
  if(wasNew) toast(`Почато Збір ${S.collN}` + (hadOrders ? ". Попередній збережено" : ""));
  if(!S.settings.rewardId) toast("Увага: нагороду не вибрано — замовлення не записуватимуться", 4000);
  else if(S.conn.status!=="on") toast("Увага: немає з'єднання з Twitch", 4000);
};
$("pauseBtn").onclick = async () => setState(await call("PauseTimer"));
$("stopBtn").onclick = async () => { setState(await call("FinishTimer")); toast("Збір завершено"); };
$("plusBtn").onclick = async () => setState(await call("AddTime", 60000));
$("minusBtn").onclick = async () => setState(await call("AddTime", -60000));

document.querySelectorAll(".tab").forEach(b => b.onclick = () => {
  tab = b.dataset.tab; document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("active",x===b)); renderTable();
});
$("search").oninput = renderTable;

$("tableWrap").addEventListener("click", async e => {
  const h = e.target.closest("th[data-sort]");
  if(h){ const k=h.dataset.sort, cur=sortKey[tab]; sortKey[tab] = [k, cur[0]===k ? -cur[1] : (k==="name"||k==="user"||k==="text"?1:-1)]; renderTable(); return; }
  const gr = e.target.closest("tr.grow");
  if(gr && !e.target.closest(".grip")){ const k = gr.dataset.key; openGroups.has(k) ? openGroups.delete(k) : openGroups.add(k); renderTable(); return; }
  const btn = e.target.closest("button[data-act]"); if(!btn) return;
  if(btn.dataset.act==="editmode"){ editMode = !editMode; renderTable(); return; }
  const id = btn.closest("tr").dataset.id, ent = S.entries.find(x=>x.id===id); if(!ent) return;
  if(btn.dataset.act==="toggle") setState(await call("UpdateEntry", {...ent, excluded: !ent.excluded}));
  if(btn.dataset.act==="del"){ if(!(await confirmBox(`Видалити замовлення «${ent.text}» від ${ent.user}?`))) return; setState(await call("DeleteEntry", id)); }
});
$("tableWrap").addEventListener("change", async e => {
  const f = e.target.dataset.f; if(!f) return;
  const id = e.target.closest("tr").dataset.id, ent = S.entries.find(x=>x.id===id); if(!ent) return;
  const upd = {...ent};
  if(f==="tier"){ upd.tier = Number(e.target.value); if(upd.tier && !upd.months) upd.months = 1; }
  if(f==="months") upd.months = Math.max(1, Number(e.target.value)||1);
  if(f==="text"){ const v = e.target.value.trim(); if(!v) return; upd.text = v; }
  e.target.blur();
  setState(await call("UpdateEntry", upd));
});
$("tableWrap").addEventListener("focusout", () => setTimeout(()=>{ if(tableDirty) renderTable(); }, 0));

/* ---------- експорт ---------- */

/* ---------- експорт у Marbles: одна кнопка під таймером, анімація замість модалки ---------- */
let mBusy = false;
function renderExportBtn(){
  if(!S || mBusy) return;
  const m = marbleInfo(), st = S.session.status, collecting = st === "running" || st === "paused";
  const btn = $("exportMarbles");
  btn.disabled = collecting || !m.total;
  btn.className = "mbtn" + (m.count > 1000 ? " warn" : "");
  $("mLabel").textContent = "Експорт у Marbles";
  $("mCount").textContent = m.total ? `${m.count} ${m.count%10===1&&m.count%100!==11?"куля":[2,3,4].includes(m.count%10)&&![12,13,14].includes(m.count%100)?"кулі":"куль"}` : "";
  btn.title = collecting ? "Йде збір — завершіть таймер, щоб експортувати" : !m.total ? "Немає замовлень з балами" : m.count > 1000 ? "Більше 1000 куль — гра може не прийняти весь список" : "Створити файл, скопіювати шлях і запустити гру";
}
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function exportMarbles(){
  if(mBusy || !marbleInfo().total) return;
  const btn = $("exportMarbles"), label = $("mLabel");
  mBusy = true; btn.disabled = true;
  btn.className = "mbtn busy"; label.textContent = "Створюю файл…"; $("mCount").textContent = "";
  try{
    const r = await api().ExportMarbles(false);
    await sleep(350);
    btn.className = "mbtn ok"; label.textContent = r.copied ? "✓ Шлях скопійовано" : "✓ Файл створено";
    await sleep(1300);
    btn.className = "mbtn ok go"; label.textContent = "Запускаю гру…";
    const launched = await api().LaunchMarbles(); // запуск саме зараз
    if(!launched) label.textContent = "Гра вже запущена — Ctrl+V";
    await sleep(1800);
  }catch(e){
    btn.className = "mbtn fail"; label.textContent = "✕ " + (e && e.message || e);
    await sleep(2500);
  }
  mBusy = false; renderExportBtn();
}
$("exportMarbles").onclick = exportMarbles;
setInterval(renderExportBtn, 500); // таймер може завершитися сам
/* ---------- старт ---------- */
function boot(){
  if(!window.go || !window.runtime){ setTimeout(boot, 50); return; }
  window.runtime.EventsOn("state", snap => setState(snap));
  window.runtime.EventsOn("redeem", d => { const id = d && d.id; if(!id) return; freshIds.add(id); setTimeout(()=>freshIds.delete(id), 3000); });
  window.runtime.EventsOn("chat", m => chatPending.push(m));
  window.runtime.EventsOn("chatdel", chatDelete);
  window.runtime.EventsOn("chatters", list => { chatters = list || []; renderChatters(); });
  api().GetChatters().then(list => { chatters = list || []; renderChatters(); });
  api().GetState().then(setState);
}
boot();
})();
