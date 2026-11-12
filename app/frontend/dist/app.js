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

/* ---------- бали ---------- */
function pointsOf(e){
  if(!e.tier) return Number(S.settings.nonSubPoints)||0;
  return (Number(S.settings.mult[String(e.tier)])||0) * Math.max(1, Number(e.months)||1);
}
function countedSet(){
  const active = S.entries.filter(e=>!e.excluded);
  const mode = S.settings.dupMode;
  if(mode==="all") return new Set(active.map(e=>e.id));
  const byUser = new Map();
  for(const e of [...active].sort((a,b)=>a.ts-b.ts)){
    const k = (e.userId || e.login || e.user).toLowerCase();
    if(mode==="first"){ if(!byUser.has(k)) byUser.set(k,e.id); } else byUser.set(k,e.id);
  }
  return new Set(byUser.values());
}
function aggregate(){
  const counted = countedSet(), groups = new Map();
  for(const e of S.entries){
    if(!counted.has(e.id)) continue;
    const k = norm(e.text); if(!k) continue;
    if(!groups.has(k)) groups.set(k,{key:k, variants:new Map(), points:0, count:0, users:[]});
    const g = groups.get(k), p = pointsOf(e);
    g.points += p; g.count++; g.users.push({name:e.user, p});
    g.variants.set(e.text.trim(), (g.variants.get(e.text.trim())||0)+1);
  }
  return [...groups.values()].map(g=>{
    const name = [...g.variants.entries()].sort((a,b)=>b[1]-a[1])[0][0]; // при рівності — перший за часом варіант
    g.users.sort((a,b)=>b.p-a.p);
    return {...g, name};
  });
}
// кількість куль = сума балів (кожен бал — одна куля)
function marbleInfo(){
  const groups = aggregate().filter(g=>g.points>0);
  const total = groups.reduce((a,g)=>a+g.points,0);
  return {count: total, total, groups: groups.length};
}

/* ---------- auth / header ---------- */
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
  $("connText").textContent = c.text;
  $("userBox").innerHTML = ok && a.user ? `<b>${esc(a.user.display_name)}</b> <button class="small" id="logoutBtn">Вийти</button>` : "";
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
  $("startBtn").textContent = s.status==="paused" ? "▶ Продовжити" : s.status==="finished" ? "▶ Продовжити збір" : "▶ Почати збір";
  $("pauseBtn").disabled = s.status!=="running";
  $("stopBtn").disabled = !(s.status==="running"||s.status==="paused");
  $("minusBtn").disabled = s.status==="finished";
}
setInterval(renderTimer, 250);

/* ---------- нагороди ---------- */
function renderRewards(){
  const st = S.settings, bound = st.rewardId;
  $("rewardBound").innerHTML = bound ? `Вибрано: <b>${esc(st.rewardTitle || "(без назви)")}</b>` : `<span class="err">Нагороду ще не вибрано — замовлення не записуватимуться.</span>`;
  const sel = $("rewardSelect"), rewards = S.rewards || [];
  const opts = [`<option value="">— виберіть нагороду —</option>`];
  let found = false;
  for(const r of rewards){
    if(r.id===bound) found = true;
    opts.push(`<option value="${esc(r.id)}" ${r.id===bound?"selected":""}>${esc(r.title)} (${r.cost})${r.is_enabled?"":" — вимкнена"}${r.is_user_input_required?"":" — без тексту!"}</option>`);
  }
  if(bound && !found) opts.push(`<option value="${esc(bound)}" selected>${esc(st.rewardTitle || bound)}</option>`);
  if(document.activeElement !== sel) sel.innerHTML = opts.join("");
  $("rewardsErr").textContent = S.rewardsErr || "";
  const seen = Object.values(S.seenRewards || {}).sort((a,b)=>b.ts-a.ts);
  $("seenRewards").innerHTML = seen.length ? seen.map(r=>`<div class="reward ${r.id===bound?"bound":""}"><div><div><b>${esc(r.title)}</b> — «${esc(r.sample||"(без тексту)")}» ${esc(r.user)} <span class="muted">(${r.count}×)</span></div><code>${esc(r.id)}</code></div>
      ${r.id===bound?'<span style="color:var(--ok)">✓</span>':`<button class="small" data-bind="${esc(r.id)}" data-title="${esc(r.title)}">Вибрати</button>`}</div>`).join("")
    : `<div class="hint">Погашень ще не було.</div>`;
}

/* ---------- OBS ---------- */
const OVERLAYS = [
  ["Таймер збору", "/overlay/timer", "600×220"],
  ["Топ замовлень", "/overlay/top", "520×420"],
  ["Кулі / бали", "/overlay/marbles", "620×80"],
  ["Алерт замовлення", "/overlay/last", "800×180"],
  ["Чат", "/overlay/chat", "420×700"],
];
let obsKey = "";
function renderObs(){
  const o = S.obs || {};
  const key = JSON.stringify(o);
  if(key === obsKey) return; obsKey = key;
  if(!o.running){
    $("obsStatus").innerHTML = `<span class="err">${esc(o.error || "Сервер для OBS не запущено.")}</span> Змініть порт у ⚙ Налаштуваннях.`;
    $("obsList").innerHTML = ""; $("chatWindowBtn").disabled = true; return;
  }
  $("chatWindowBtn").disabled = false;
  $("obsStatus").textContent = "Браузерні джерела для OBS (розмір — підказка):";
  $("obsList").innerHTML = OVERLAYS.map(([name, p, size])=>`<div class="obsrow"><span><b>${esc(name)}</b> <span class="muted">${size}</span><code>${esc(o.url + p)}</code></span>
    <button class="small" data-copy="${esc(o.url + p)}">Копіювати</button><button class="small" data-open="${esc(p)}" title="Відкрити в браузері">↗</button></div>`).join("")
    + `<div class="obsrow"><span class="muted">Усі параметри й розміри</span><button class="small" data-open="/overlay/index">Усі оверлеї ↗</button></div>`;
}
$("obsList").addEventListener("click", async e => {
  const c = e.target.closest("[data-copy]"), o = e.target.closest("[data-open]");
  if(c){ const ok = await call("CopyText", c.dataset.copy); toast(ok ? "URL скопійовано — вставте в OBS → Браузер" : "Не вдалося скопіювати"); }
  if(o) call("OpenOverlay", o.dataset.open);
});

/* ---------- статистика і таблиці ---------- */
function renderStats(){
  const counted = countedSet(), groups = aggregate();
  const cEntries = S.entries.filter(e=>counted.has(e.id));
  $("sEntries").textContent = cEntries.length;
  $("sUsers").textContent = new Set(cEntries.map(e=>(e.userId||e.login).toLowerCase())).size;
  $("sUnique").textContent = groups.length;
  $("sPoints").textContent = groups.reduce((a,g)=>a+g.points,0);
  const m = marbleInfo();
  $("marblePreview").innerHTML = m.total ? `<b>${m.count}</b> куль із ${m.groups} замовлень` + (m.count>1000 ? ` <span class="err">— більше 1000: гра може не прийняти весь список</span>` : "") : "Немає замовлень із балами > 0";
}

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
  const isChat = tab==="chat";
  wrap.classList.toggle("hidden", isChat); $("chatWrap").classList.toggle("hidden", !isChat);
  $("chatWindowBtn").classList.toggle("hidden", !isChat); $("search").classList.toggle("hidden", isChat || tab==="log");
  $("tableHint").classList.toggle("hidden", tab!=="summary" && tab!=="entries");
  if(isChat) return;
  // не перемальовувати, поки користувач редагує клітинку
  if(drag || (wrap.contains(document.activeElement) && /INPUT|SELECT/.test(document.activeElement.tagName))){ tableDirty = true; return; }
  tableDirty = false;
  const q = $("search").value.trim().toLowerCase();
  if(tab==="log"){
    const log = (S.log||[]).slice().reverse();
    wrap.innerHTML = `<div class="log">${log.length ? log.map(esc).join("\n") : "Подій поки немає."}</div>`; return;
  }
  if(tab==="summary"){
    let rows = aggregate().filter(g=>!q || g.name.toLowerCase().includes(q) || g.users.some(u=>u.name.toLowerCase().includes(q)));
    rows = sortRows(rows, sortKey.summary);
    const max = Math.max(1,...rows.map(r=>r.points)), total = rows.reduce((a,r)=>a+r.points,0)||1;
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${S.entries.length?"Нічого не знайдено":"Замовлень поки немає. Виберіть нагороду і натисніть «Почати збір»."}</div>`; return; }
    wrap.innerHTML = `<table class="dnd"><thead><tr><th class="gripcol"></th><th style="width:40px">#</th>${th("Замовлення","name")}${th("Бали","points","num")}<th style="width:140px">Частка</th>${th("Людей","count","num")}<th>Хто замовив</th></tr></thead><tbody>${
      rows.map((g,i)=>`<tr data-key="${esc(g.key)}" data-name="${esc(g.name)}"><td class="gripcol">${GRIP}</td><td class="muted">${i+1}</td><td><b>${esc(g.name)}</b>${g.variants.size>1?` <span class="muted" title="${esc([...g.variants.keys()].join(" | "))}">(${g.variants.size} варіанти)</span>`:""}</td>
        <td class="num pts">${g.points}</td><td><div class="bar" style="width:${Math.round(g.points/max*100)}%"></div><span class="muted" style="font-size:11px">${(g.points/total*100).toFixed(1)}%</span></td>
        <td class="num">${g.count}</td><td class="users">${g.users.map(u=>`${esc(u.name)} <span class="muted">(${u.p})</span>`).join(", ")}</td></tr>`).join("")
    }</tbody></table>`;
  } else {
    const counted = countedSet();
    let rows = S.entries.map(e=>({...e, points:pointsOf(e), counted:counted.has(e.id)}))
      .filter(e=>!q || e.text.toLowerCase().includes(q) || e.user.toLowerCase().includes(q));
    rows = sortRows(rows, sortKey.entries);
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${S.entries.length?"Нічого не знайдено":"Замовлень поки немає."}</div>`; return; }
    wrap.innerHTML = `<table class="dnd"><thead><tr><th class="gripcol"></th>${th("Час","ts")}${th("Чатерс","user")}${th("Тір","tier")}${th("Місяці","months","num")}${th("Бали","points","num")}${th("Замовлення","text")}<th></th></tr></thead><tbody>${
      rows.map(e=>`<tr data-id="${e.id}" class="${e.excluded?"off":!e.counted?"replaced":""} ${freshIds.has(e.id)?"fresh":""}" title="${esc(e.excluded?"Приховано — не рахується":!e.counted?"Не рахується: від цього чатерса враховано інше замовлення":(e.source||""))}">
        <td class="gripcol">${GRIP}</td><td class="muted">${fmtClock(e.ts)}</td>
        <td>${esc(e.user)}${e.manual?' <span class="muted">(вручну)</span>':""}</td>
        <td><select data-f="tier"><option value="0" ${!e.tier?"selected":""}>—</option>${[1,2,3].map(t=>`<option value="${t}" ${e.tier===t?"selected":""}>T${t}</option>`).join("")}</select></td>
        <td class="num"><input class="mo" type="number" min="1" data-f="months" value="${e.months||""}" ${!e.tier?"disabled":""}></td>
        <td class="num pts">${e.tier?`<span class="tier tier${e.tier}">T${e.tier}</span> `:""}${e.points}</td>
        <td><input class="edit" data-f="text" value="${esc(e.text)}"></td>
        <td class="acts"><button class="icon${e.excluded?" on":""}" data-act="toggle" title="${e.excluded?"Показати — знову рахувати":"Приховати — не рахувати (можна повернути)"}">${e.excluded?ICON_EYE_OFF:ICON_EYE}</button><button class="icon danger" data-act="del" title="Видалити назавжди">${ICON_TRASH}</button></td></tr>`).join("")
    }</tbody></table>`;
  }
}

/* ---------- чат ---------- */
const CHAT_MAX = 300;
let chatPending = [], chatStick = true, chatLoaded = false;
const chatWrap = $("chatWrap"), chatList = $("chatList");
chatWrap.addEventListener("scroll", () => {
  chatStick = chatWrap.scrollTop + chatWrap.clientHeight >= chatWrap.scrollHeight - 30;
  if(chatStick) $("chatMore").classList.add("hidden");
});
$("chatMore").onclick = () => { chatWrap.scrollTop = chatWrap.scrollHeight; $("chatMore").classList.add("hidden"); };
function chatFlush(){
  if(!chatPending.length) return;
  const nodes = chatPending.splice(0).map(m => ChatRender.render(m, {time:true}));
  chatList.append(...nodes);
  while(chatList.children.length > CHAT_MAX) chatList.firstChild.remove();
  if(chatStick) chatWrap.scrollTop = chatWrap.scrollHeight; else $("chatMore").classList.remove("hidden");
}
setInterval(chatFlush, 150);
function chatDelete(d){
  if(d.all){ chatList.replaceChildren(); chatPending = []; return; }
  const ids = new Set(d.ids || []);
  for(const n of [...chatList.children]) if(ids.has(n.dataset.id) || (d.userId && n.dataset.user === d.userId)) n.remove();
  chatPending = chatPending.filter(m => !ids.has(m.id) && m.userId !== d.userId);
}
$("chatWindowBtn").onclick = () => call("OpenChatWindow");

/* ---------- налаштування ---------- */
function fillSettings(){
  const st = S.settings;
  $("m1").value = st.mult["1"]; $("m2").value = st.mult["2"]; $("m3").value = st.mult["3"];
  $("nonSub").value = st.nonSubPoints; $("dupMode").value = st.dupMode; $("obsPort").value = st.obsPort;
}
function initInputs(){
  $("duration").value = S.settings.durationMin;
}
const n = (id, d) => { const v = Number($(id).value); return Number.isFinite(v) && $(id).value !== "" ? v : d; };
async function saveSettings(patch){ setState(await call("SaveSettings", {...S.settings, ...patch})); }

$("settingsBtn").onclick = () => { fillSettings(); $("settingsModal").classList.add("show"); };
$("settingsCancel").onclick = () => $("settingsModal").classList.remove("show");
$("settingsReset").onclick = () => { $("m1").value = 1; $("m2").value = 2; $("m3").value = 3; $("nonSub").value = 0; $("dupMode").value = "last"; };
$("settingsSave").onclick = async () => {
  await saveSettings({
    mult: {"1": Math.max(0,n("m1",1)), "2": Math.max(0,n("m2",2)), "3": Math.max(0,n("m3",3))},
    nonSubPoints: Math.max(0, n("nonSub",0)), dupMode: $("dupMode").value, obsPort: Math.round(n("obsPort",17841)) });
  $("settingsModal").classList.remove("show"); obsKey = ""; renderObs(); toast("Налаштування збережено");
};
$("duration").addEventListener("change", async () => { await saveSettings({durationMin: Math.max(1, Math.round(n("duration",10)))}); initInputs(); });
function renderAll(){
  if(!inited){ initInputs(); inited = true; }
  renderAuth();
  if(S.auth && S.auth.status==="ok"){
    renderStats(); renderTable(); renderRewards(); renderObs();
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
  setState(await call("StartTimer"));
  if(!S.settings.rewardId) toast("Увага: нагороду не вибрано — замовлення не записуватимуться", 4000);
  else if(S.conn.status!=="on") toast("Увага: немає з'єднання з Twitch", 4000);
};
$("pauseBtn").onclick = async () => setState(await call("PauseTimer"));
$("stopBtn").onclick = async () => { setState(await call("FinishTimer")); toast("Збір завершено"); };
$("plusBtn").onclick = async () => setState(await call("AddTime", 60000));
$("minusBtn").onclick = async () => setState(await call("AddTime", -60000));

$("rewardSelect").onchange = async () => {
  const id = $("rewardSelect").value, r = (S.rewards||[]).find(x=>x.id===id);
  setState(await call("SelectReward", id, r ? r.title : ""));
  if(r && !r.is_user_input_required) toast("Увага: ця нагорода не вимагає тексту — замовлення без тексту ігноруються", 4500);
};
$("rewardRefresh").onclick = async () => { await call("RefreshRewards"); toast("Список нагород оновлено"); };
$("seenRewards").onclick = async e => { const b = e.target.closest("button[data-bind]"); if(!b) return; setState(await call("SelectReward", b.dataset.bind, b.dataset.title)); toast("Нагороду вибрано"); };
$("rewardIdSave").onclick = async () => { const id = $("rewardIdInput").value.trim(); setState(await call("SelectReward", id, "")); toast(id?"ID збережено":"ID очищено"); };

document.querySelectorAll(".tab").forEach(b => b.onclick = () => {
  tab = b.dataset.tab; document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("active",x===b)); renderTable();
  if(tab==="chat"){ chatStick = true; chatWrap.scrollTop = chatWrap.scrollHeight; }
});
$("search").oninput = renderTable;

$("tableWrap").addEventListener("click", async e => {
  const h = e.target.closest("th[data-sort]");
  if(h){ const k=h.dataset.sort, cur=sortKey[tab]; sortKey[tab] = [k, cur[0]===k ? -cur[1] : (k==="name"||k==="user"||k==="text"?1:-1)]; renderTable(); return; }
  const btn = e.target.closest("button[data-act]"); if(!btn) return;
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
const stamp = () => { const d=new Date(), p=n=>String(n).padStart(2,"0"); return `${d.getFullYear()}-${p(d.getMonth()+1)}-${p(d.getDate())}_${p(d.getHours())}-${p(d.getMinutes())}`; };

$("exportMarbles").onclick = async () => {
  if(!marbleInfo().total) return toast("Немає замовлень з балами > 0");
  const r = await call("ExportMarbles", true);
  let msg = `Файл для Marbles: ${r.lines} куль\n${r.path}\n\n`;
  msg += r.copied ? "✓ Шлях скопійовано в буфер обміну.\n" : "";
  msg += r.alreadyRunning ? "Marbles on Stream уже запущено.\n" : r.launched ? "Запускаю Marbles on Stream через Steam…\n" : "";
  msg += "\nУ грі: Race → Simulate → ⚙ Settings → Simulation Settings → Use Custom Names → Add Names → Open → Ctrl+V → Enter.";
  if(await confirmBox(msg, "Показати файл", "Закрити")) call("RevealFile", r.path);
};
$("copyMarbles").onclick = async () => {
  const list = await call("MarbleLines"); if(!list || !list.length) return toast("Немає що копіювати");
  const ok = await call("CopyText", list.join("\r\n")); toast(ok ? `Скопійовано ${list.length} рядків` : "Не вдалося скопіювати"); };
$("openMarbles").onclick = () => call("OpenFolder", "marbles");
$("openExports").onclick = () => call("OpenFolder", "exports");
$("openHistory").onclick = () => call("OpenFolder", "history");

$("exportCsv").onclick = async () => {
  const counted = countedSet();
  // Excel в українській локалі очікує «;» як роздільник
  const cell = v => { const s = String(v ?? ""); return /[";\n]/.test(s) ? `"${s.replace(/"/g,'""')}"` : s; };
  const head = ["Час","Чатерс","Логін","Тір","Місяці","Бали","Замовлення","Рахується"];
  const rows = S.entries.map(e=>[new Date(e.ts).toLocaleString("uk-UA"), e.user, e.login, e.tier||"—", e.tier?e.months:"", pointsOf(e), e.text, e.excluded?"виключено":counted.has(e.id)?"так":"ні (дубль)"]);
  const sum = aggregate().sort((a,b)=>b.points-a.points).map(g=>[g.name, g.points, g.count, g.users.map(u=>`${u.name} (${u.p})`).join(", ")]);
  const csv = "sep=;\r\n" + [head, ...rows].map(r=>r.map(cell).join(";")).join("\r\n")
    + "\r\n\r\nПідсумок\r\n" + [["Замовлення","Бали","Людей","Хто"], ...sum].map(r=>r.map(cell).join(";")).join("\r\n") + "\r\n";
  const p = await call("SaveExport", `subday_${stamp()}.csv`, csv);
  toast("Збережено: " + p, 4000); call("RevealFile", p);
};
$("exportJson").onclick = async () => {
  const data = JSON.stringify({exportedAt:new Date().toISOString(), settings:S.settings, session:S.session,
    entries:S.entries.map(e=>({...e, points:pointsOf(e)})), summary:aggregate().map(({variants,...g})=>g)}, null, 2);
  const p = await call("SaveExport", `subday_${stamp()}.json`, data);
  toast("Збережено: " + p, 4000); call("RevealFile", p);
};
$("newSession").onclick = async () => {
  if(S.entries.length && !(await confirmBox("Почати новий збір? Поточні замовлення буде збережено в папку history і очищено з таблиці."))) return;
  const p = await call("NewSession");
  toast(p ? "Архів: " + p : "Новий збір", 4000);
};
$("mAdd").onclick = async () => {
  const tier = Number($("mTier").value);
  setState(await call("AddManual", $("mUser").value, tier, Math.max(1, Number($("mMonths").value)||1), $("mText").value));
  $("mText").value = ""; toast("Додано");
};

/* ---------- старт ---------- */
function boot(){
  if(!window.go || !window.runtime){ setTimeout(boot, 50); return; }
  window.runtime.EventsOn("state", snap => setState(snap));
  window.runtime.EventsOn("redeem", d => { const id = d && d.id; if(!id) return; freshIds.add(id); setTimeout(()=>freshIds.delete(id), 3000); });
  window.runtime.EventsOn("chat", m => chatPending.push(m));
  window.runtime.EventsOn("chatdel", chatDelete);
  api().GetState().then(setState);
}
boot();
})();
