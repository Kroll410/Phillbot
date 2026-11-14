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
function toast(msg, ms=2600){}
// «зменшити рух» у системі: без блискіток і декоративних перетікань (оберт колеса лишається — це результат)
const RM = matchMedia("(prefers-reduced-motion: reduce)"); // нижні сповіщення прибрано з додатку (дії видно з самого інтерфейсу)
// підказки при наведенні (title) прибрано всюди: знімаємо атрибут і з нових елементів
(() => {
  // кнопкам без тексту (шестерні, іконки) підказка лишається як aria-label — для екранних читачів
  const drop = e => { const t = e.getAttribute("title"); e.removeAttribute("title");
    if(t && !e.hasAttribute("aria-label") && e.matches("button,a") && !e.textContent.trim()) e.setAttribute("aria-label", t); };
  const strip = n => { if(n.nodeType !== 1) return; if(n.hasAttribute("title")) drop(n); n.querySelectorAll("[title]").forEach(drop); };
  new MutationObserver(ms => ms.forEach(m => m.type === "attributes" ? (m.target.hasAttribute("title") && drop(m.target)) : m.addedNodes.forEach(strip)))
    .observe(document.documentElement, {subtree: true, childList: true, attributes: true, attributeFilter: ["title"]});
  strip(document.body);
})();
function confirmBox(text, yes="Підтвердити", no="Скасувати"){ return new Promise(res=>{ $("modalText").textContent=text; $("modalYes").textContent=yes; $("modalNo").textContent=no; $("modal").classList.add("show");
  const done=v=>{ $("modal").classList.remove("show"); $("modalYes").onclick=$("modalNo").onclick=null; res(v); };
  $("modalYes").onclick=()=>done(true); $("modalNo").onclick=()=>done(false); }); }
async function call(fn, ...args){
  try{ return await api()[fn](...args); }
  catch(e){ toast("Помилка: " + (e && e.message || e), 4000); throw e; }
}
/* Індикатор завантаження — єдиний підхід для будь-якого завантаження:
   рамка елемента (вікна, картки) стає анімованим градієнтом зелений → фіолетовий.
   withLoading(елемент або [елементи], async () => {...}) — клас .is-loading знімається сам, навіть при помилці. */
// увімкнути / плавно згасити індикатор (рамка з'являється й зникає м'яко, без різкого обриву)
function loadOn(el){ clearTimeout(el._loadT); el.classList.remove("load-fade"); el.classList.add("is-loading"); }
function loadOff(el){
  if(!el.classList.contains("is-loading")) return;
  el.classList.add("load-fade");
  clearTimeout(el._loadT); el._loadT = setTimeout(() => el.classList.remove("is-loading", "load-fade"), 620);
}
async function withLoading(els, work){
  const list = (Array.isArray(els) ? els : [els]).filter(Boolean);
  list.forEach(loadOn);
  try{ return await work(); }
  finally{ list.forEach(loadOff); }
}
// нове замовлення, коли відкрито «Підсумок» чи «Замовлення» — коротко показуємо індикатор завантаження навколо таблиці
let flashT = 0;
function flashTable(){
  if(mode !== "subday" || !D().live || (tab !== "summary" && tab !== "entries")) return;
  const f = $("tableFrame"); loadOn(f);
  clearTimeout(flashT); flashT = setTimeout(() => loadOff(f), 1300);
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
  if(e.donation) return Number(e.donPoints) || 0; // донат Donatello: бали вже з гривень
  const gifts = Number(e.gifts) || 0; // +1 бал за кожну подаровану сабку за 30 днів (як у бекенді)
  if(!e.tier) return (Number(st.nonSubPoints)||0) + gifts;
  return (Number(st.mult[String(e.tier)])||0) * Math.max(1, Number(e.months)||1) + gifts;
}
// «діюче» замовлення кожного чатерса (останнє / перше / у «ділити» — усі), включно з прихованими — лише вони є в таблиці
function effectiveSet(d = D()){
  const mode = d.settings.dupMode;
  if(mode==="split") return new Set(d.entries.map(e=>e.id));
  const byUser = new Map();
  for(const e of [...d.entries].sort((a,b)=>a.ts-b.ts)){
    const k = (e.userId || e.login || e.user).toLowerCase();
    if(mode==="first"){ if(!byUser.has(k)) byUser.set(k,e.id); } else byUser.set(k,e.id);
  }
  return new Set(byUser.values());
}
// рахується «діюче» замовлення, якщо воно не приховане
function countedSet(d = D()){ const eff = effectiveSet(d); return new Set(d.entries.filter(e=>eff.has(e.id) && !e.excluded).map(e=>e.id)); }
// бали кожного врахованого замовлення (як entryPoints у Go): у «ділити» бали глядача діляться між його замовленнями,
// 10 на 3 = 3, 3, 4 — залишок найсвіжішим
function pointsMap(d = D()){
  const counted = countedSet(d), out = new Map();
  if(d.settings.dupMode !== "split"){ d.entries.forEach(e => { if(counted.has(e.id)) out.set(e.id, pointsOf(e, d.settings)); }); return out; }
  const byUser = new Map();
  for(const e of [...d.entries].sort((a, b) => a.ts - b.ts)){
    if(!counted.has(e.id)) continue;
    const k = (e.userId || e.login || e.user).toLowerCase();
    if(!byUser.has(k)) byUser.set(k, []); byUser.get(k).push(e);
  }
  for(const list of byUser.values()){
    const total = pointsOf(list[list.length - 1], d.settings), n = list.length, base = Math.floor(total / n), rem = total % n;
    list.forEach((e, i) => out.set(e.id, base + (i >= n - rem ? 1 : 0)));
  }
  return out;
}
function aggregate(d = D()){
  const counted = countedSet(d), groups = new Map(), pm = pointsMap(d);
  for(const e of d.entries){
    if(!counted.has(e.id)) continue;
    const k = norm(e.text); if(!k) continue;
    if(!groups.has(k)) groups.set(k,{key:k, variants:new Map(), points:0, count:0, users:[]});
    const g = groups.get(k), p = pm.get(e.id) || 0;
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
  if(resetting) return;
  if(!S || !S.stream) return;
  const curId = pastStream ? pastStream.id : S.stream.id;
  const meta = streams.find(m => m.id === curId) || {id:curId, startedAt: pastStream ? pastStream.startedAt : S.stream.startedAt, collections: streamColls().length + (!pastStream && S.entries.length ? 1 : 0), current: !pastStream};
  $("streamCur").innerHTML = `<b>${esc(fmtStream(meta).replace(" (поточний)",""))}</b>${pastStream ? ' <span class="muted">· минулий</span>' : ' <span class="live-dot"></span>'}`;
  $("streamLabel").textContent = pastStream ? fmtStream(meta) : "";
  $("streamLabel").classList.toggle("hidden", !pastStream);
  // багато зборів — старші стискаються в кружечки з номером; повністю видно вибраний, поточний і два останні
  const colls = streamColls(), keepFull = new Set(colls.slice(-2).map(c => c.n));
  if(view) keepFull.add(view.n);
  const compact = colls.length + (pastStream ? 0 : 1) > 4;
  const chips = colls.map(c => {
    const act = view && view.n === c.n, info = `${fmtHM(c.startedAt)} · ${ordersOf(c)} замовл.`;
    return compact && !keepFull.has(c.n)
      ? `<button class="cchip mini${act?" active":""}" data-n="${c.n}" title="Збір ${c.n} · ${esc(info)}"><b>${c.n}</b></button>`
      : `<button class="cchip${act?" active":""}" data-n="${c.n}"><b>Збір ${c.n}</b><span>${info}</span></button>`;
  });
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
async function openStream(id){
  if(!id || id === S.stream.id){ pastStream = null; view = null; }
  else {
    try{ pastStream = await api().GetStream(id); }catch(e){ toast("Не вдалося відкрити стрім"); return; }
    const cs = pastStream.collections || []; view = cs.length ? {n: cs[cs.length-1].n} : null;
  }
  editMode = false; openGroups.clear(); renderAll();
}
function renderStreamList(){
  const curId = pastStream ? pastStream.id : S.stream.id;
  $("streamList").innerHTML = streams.length ? streams.map(m => `<button class="srow${m.id===curId?" active":""}" data-sid="${esc(m.id)}">
      <span class="sdate">${esc(fmtStream(m).split(" · ")[0])}</span>
      <span class="smeta">${esc(fmtStream(m).replace(" (поточний)","").split(" · ").slice(1).join(" · "))} · ${m.orders} замовл.</span>
      ${m.current ? '<span class="sbadge">поточний</span>' : ""}</button>`).join("")
    : `<div class="hint">Стрімів ще немає.</div>`;
}
$("openJournal").onclick = async () => {
  renderStreamList(); openModal("streamsModal");
  await withLoading($("streamsModal").querySelector(".box"), loadStreams); renderStreamList();
};
$("streamList").addEventListener("click", async e => { const b = e.target.closest("[data-sid]"); if(!b) return; closeModal("streamsModal"); await openStream(b.dataset.sid); });
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
  // «Вийти» — затисканням з червоною «фарбою»; перемальовуємо лише при зміні акаунта (щоб не перервати затискання)
  $("modes").classList.toggle("hidden", !ok); $("generalBtn").classList.toggle("hidden", !ok); if(!ok) $("donPill").classList.add("hidden"); // перемикач вкладок і налаштування — лише після входу
  const ub = ok && a.user ? `<b class="uname">${esc(a.user.display_name)}</b><button class="winbtn holdend logoutbtn" id="logoutBtn" data-hold="logout" title="Затисніть і тримайте, щоб вийти з акаунта Twitch">${LIQUID_RED}<span class="wl">Вийти</span></button>` : "";
  if($("userBox")._k !== ub){ $("userBox")._k = ub; $("userBox").innerHTML = ub; }
  if(ok && a.user && lastAuthStatus !== "ok") showWelcome(a.user.display_name);
  lastAuthStatus = a.status;
}

/* ---------- таймер ---------- */
function remaining(){ const s=S.session; return s.status==="running" ? s.endsAt-Date.now() : s.remainingMs; }
function renderTimer(){
  if(!S) return;
  const s = S.session, r = remaining(), el = $("timer");
  el.textContent = fmtTime(s.status==="finished"?0:r);
  el.className = "timer " + s.status + (s.status==="running" && r<=30000 ? " low" : "") + (Date.now() < blinkUntil ? " gblink" : "");
  $("statusText").textContent = {idle:"Очікування старту", running:"Збір іде", paused:"Пауза", finished:"Збір завершено"}[s.status] || "";
  $("statusText").className = "spill " + (s.status || "idle");
  $("statusText").title = {running:"Замовлення записуються", paused:"Замовлення не записуються, поки пауза"}[s.status] || "";
  $("startBtn").disabled = s.status==="running";
  $("pauseBtn").disabled = s.status!=="running";
  $("stopBtn").disabled = !(s.status==="running"||s.status==="paused");
  const going = s.status==="running" || s.status==="paused"; // ±1 хв — лише коли збір іде або на паузі
  $("minusBtn").disabled = !going; $("plusBtn").disabled = !going;
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
  return `<${tag} class="rtile${opts.sel?" sel":""}${r.is_enabled===false?" off":""}" ${opts.button?`data-rid="${esc(r.id)}" title="${esc(r.title)}"`:""} style="--rbg:${esc(r.background_color||"#9147ff")}${opts.delay!=null?`;animation-delay:${opts.delay}ms`:""}">
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
let showAllRewards = false;
function renderRewardGrid(animate){
  const sel = pendingReward ?? S.settings.rewardId;
  const all = [...(S.rewards||[])].sort((a,b)=>a.cost-b.cost);
  if(sel && !all.some(r=>r.id===sel)){ const r = rewardById(sel); if(r) all.unshift(r); }
  let html;
  if(showAllRewards){
    html = all.map((r,i)=>rewardTile(r,{button:true, sel:r.id===sel, delay: animate ? i*45 : null}).replace('class="rtile', `class="rtile${animate?" appear":""}`)).join("");
    if(!all.length) html = `<div class="hint">Список нагород порожній. Натисніть «Оновити».</div>`;
  } else {
    const cur = all.find(r=>r.id===sel);
    html = (cur ? rewardTile(cur,{button:true, sel:true}) : `<div class="rtile rnone"><div class="rimg"></div><div class="rtitle">Нагороду не вибрано</div></div>`)
      + `<button class="rtile rmore" id="showAllRewards" title="Показати всі нагороди каналу"><div class="rimg"><span class="plus"></span></div><div class="rtitle">Показати всі<br><span class="muted">${all.length} нагород</span></div></button>`;
  }
  $("rewardGrid").innerHTML = html;
  $("rewardsErr").textContent = S.rewardsErr || "";
}
$("rewardGrid").addEventListener("click", e => {
  if(e.target.closest("#showAllRewards")){ showAllRewards = true; renderRewardGrid(true); return; }
  const t = e.target.closest("[data-rid]"); if(!t) return; pendingReward = t.dataset.rid; renderRewardGrid();
});
$("rewardRefresh").onclick = async () => {
  await withLoading([$("subdayModal").querySelector(".box"), $("rewardRefresh")], () => call("RefreshRewards"));
  renderRewardGrid(); toast("Список нагород оновлено");
};

/* ---------- OBS: один віджет (загальні налаштування) ---------- */
function renderObs(){
  const o = S.obs || {}, box = $("obsWidget"); if(!box) return;
  box.innerHTML = o.running
    ? `<div class="obsrow"><span><code>${esc(o.url)}/overlay/widget</code></span>
       <button class="small" data-copy="${esc(o.url)}/overlay/widget">Копіювати</button><button class="small" data-open="/overlay/widget" title="Відкрити в браузері">↗</button></div>`
    : `<div class="hint err">${esc(o.error || "Сервер для OBS не запущено.")}</div>`;
}
$("obsWidget").addEventListener("click", async e => {
  const c = e.target.closest("[data-copy]"), o = e.target.closest("[data-open]");
  if(c){ const ok = await call("CopyText", c.dataset.copy); toast(ok ? "URL скопійовано — OBS → Джерела → Браузер" : "Не вдалося скопіювати"); }
  if(o) call("OpenOverlay", o.dataset.open);
});

/* ---------- статистика і таблиці ---------- */
function renderStats(){
  if(resetting) return;
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
const GRIP = `<span class="grip" title="Перетягніть рядок на інше замовлення, щоб об'єднати їх">⠿</span>`;
const DRAG = () => D().live ? ' draggable="true"' : ""; // рядок можна тягнути за будь-яке місце
// натиснули на кнопку / поле в рядку — рядок тимчасово не перетягується (виділення тексту, затискання працюють як звичайно)
document.addEventListener("pointerdown", e => {
  const f = e.target.closest && e.target.closest("input,select,textarea,button,a"), tr = f && f.closest("tr[draggable]");
  if(!tr) return;
  tr.draggable = false;
  const back = () => { tr.draggable = true; document.removeEventListener("pointerup", back, true); document.removeEventListener("pointercancel", back, true); };
  document.addEventListener("pointerup", back, true); document.addEventListener("pointercancel", back, true);
}, true);

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
  const tr = ev.target.closest && ev.target.closest("tr[draggable]"); if(!tr){ ev.preventDefault(); return; }
  drag = dragInfo(tr); if(!drag){ ev.preventDefault(); return; }
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
  if(resetting) return;
  const wrap = $("tableWrap");
  // не перемальовувати, поки користувач редагує клітинку
  if(drag || holding || (wrap.contains(document.activeElement) && /INPUT|SELECT/.test(document.activeElement.tagName))){ tableDirty = true; return; }
  tableDirty = false;
  const q = ""; // пошук прибрано
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
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${D().entries.length?"Нічого не знайдено":"Замовлень поки немає"}</div>`; return; }
    const counted = countedSet(), pm = pointsMap();
    wrap.innerHTML = `<table class="dnd summary"><thead><tr><th class="gripcol"></th><th style="width:44px">#</th>${th("Замовлення","name")}${th("Бали","points","num")}<th class="sharecol">Частка</th>${th("Людей","count","num")}<th style="width:1%"></th><th style="width:44px"></th></tr></thead><tbody>${
      rows.map((g,i)=>{
        const open = openGroups.has(g.key);
        const main = `<tr class="grow${open?" open":""}" data-key="${esc(g.key)}" data-name="${esc(g.name)}"${DRAG()}><td class="gripcol">${D().live?GRIP:""}</td><td class="muted">${i+1}</td><td class="sumname"><b>${esc(g.name)}</b>${g.variants.size>1?` <span class="muted" title="${esc([...g.variants.keys()].join(" | "))}">(${g.variants.size} варіанти)</span>`:""}</td>
        <td class="num pts">${g.points}</td><td class="sharecol"><div class="share"><div class="track"><div class="bar" style="width:${Math.round(g.points/max*100)}%"></div></div><span>${(g.points/total*100).toFixed(0)}%</span></div></td>
        <td class="num">${g.count}</td><td class="wincell">${canPickWinner() && g.points>0 ? `<button class="winbtn" data-win="${esc(g.name)}" title="Затисніть і тримайте, щоб вибрати переможцем">${LIQUID}<span class="wl">Переможець</span></button>` : ""}</td><td class="chev" title="${open?"Згорнути":"Показати замовлення"}"><span></span></td></tr>`;
        if(!open) return main;
        const list = D().entries.filter(e=>counted.has(e.id) && norm(e.text)===g.key).sort((a,b)=>(pm.get(b.id)||0)-(pm.get(a.id)||0) || a.ts-b.ts);
        return main + `<tr class="gdetail"><td colspan="8"><table class="subtable"><thead><tr><th>Час</th><th>Чатерс</th><th>Тір сабки</th><th class="num">Має сабку</th><th class="num">Бали</th></tr></thead><tbody>${
          list.map(e=>`<tr><td class="muted">${fmtClock(e.ts)}</td><td>${userTag(e)}${e.manual?' <span class="muted">(вручну)</span>':""}</td>
            <td>${e.tier?`<span class="tier tier${e.tier}">T${e.tier}</span>`:'<span class="muted">—</span>'}</td>
            <td class="num">${e.tier?`<span class="subcell">${subBadgeFor(e)}${e.months} міс.</span>`:'<span class="muted">—</span>'}</td>
            <td class="num pts">${ptGift(e)}${pm.get(e.id) || 0}</td></tr>`).join("")
        }</tbody></table></td></tr>`;
      }).join("")
    }</tbody></table>`;
  } else {
    const counted = countedSet();
    const eff = effectiveSet();
    const pm = pointsMap();
    let rows = D().entries.filter(e=>eff.has(e.id)).map(e=>({...e, points: counted.has(e.id) ? (pm.get(e.id)||0) : pointsOf(e), counted:counted.has(e.id)}))
      .filter(e=>!q || e.text.toLowerCase().includes(q) || e.user.toLowerCase().includes(q));
    rows = sortRows(rows, sortKey.entries);
    if(!rows.length){ wrap.innerHTML = `<div class="empty">${D().entries.length?"Нічого не знайдено":"Замовлень поки немає"}</div>`; return; }
    wrap.innerHTML = `<table class="dnd"><thead><tr><th class="gripcol"></th>${th("Час","ts")}${th("Чатерс","user")}${th("Тір сабки","tier")}${th("Має сабку","months","num")}${th("Бали","points","num")}${th("Замовлення","text")}<th class="acts">${D().live?`<button class="small editbtn${editMode?" on":""}" data-act="editmode">${editMode?"Готово":"Редагувати"}</button>`:""}</th></tr></thead><tbody>${
      rows.map(e=>`<tr data-id="${e.id}"${DRAG()} class="${e.excluded?"off":!e.counted?"replaced":""} ${freshIds.has(e.id)?"fresh":""}" title="${esc(e.excluded?"Приховано — не рахується":!e.counted?"Не рахується: від цього чатерса враховано інше замовлення":(e.source||""))}">
        <td class="gripcol">${D().live?GRIP:""}</td><td class="muted">${fmtClock(e.ts)}</td>
        <td>${userTag(e)}${e.manual?' <span class="muted">(вручну)</span>':""}</td>
        ${e.donation ? `<td>${donChip(e.uah)}</td><td class="num"><span class="muted">Donatello</span></td>` : editMode ? `<td><select data-f="tier"><option value="0" ${!e.tier?"selected":""}>—</option>${[1,2,3].map(t=>`<option value="${t}" ${e.tier===t?"selected":""}>T${t}</option>`).join("")}</select></td>
        <td class="num"><span class="subcell">${subBadgeFor(e)}<input class="mo" type="number" min="1" data-f="months" value="${e.months||""}" ${!e.tier?"disabled":""}></span></td>`
        : `<td>${e.tier?`<span class="tier tier${e.tier}">T${e.tier}</span>`:'<span class="muted">—</span>'}</td><td class="num">${e.tier?`<span class="subcell">${subBadgeFor(e)}${e.months} міс.</span>`:'<span class="muted">—</span>'}</td>`}
        <td class="num pts">${e.counted ? ptGift(e) : ""}${e.points}</td>
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
/* перемикачі вкладок: градієнтна підкладка ковзає під активну вкладку */
function slideSeg(box){
  if(!box) return;
  let ind = box.querySelector(":scope > .seg-ind");
  if(!ind){ ind = document.createElement("span"); ind.className = "seg-ind"; box.prepend(ind); }
  const a = box.querySelector(":scope > .active"); if(!a){ ind.style.opacity = 0; return; }
  if(!a.offsetWidth) return; // вкладка схована — підкладку виставить наступний рендер
  ind.style.opacity = 1;
  ind.style.transform = `translateX(${a.offsetLeft}px)`; ind.style.width = a.offsetWidth + "px";
}
function slideAll(){ slideSeg($("modes")); slideSeg(document.querySelector(".tabs .seg")); }
addEventListener("resize", slideAll);
if(document.fonts && document.fonts.ready) document.fonts.ready.then(slideAll); // ширина вкладок змінюється, коли довантажиться шрифт
function showMode(m){
  mode = m === "viewing" ? m : "subday"; // вкладку «Чат» прибрано з перемикача
  try{ localStorage.setItem("phillbot-mode", mode); }catch(e){}
  document.querySelectorAll(".mode-tab[data-mode]").forEach(b => b.classList.toggle("active", b.dataset.mode === mode));
  $("modeChat").classList.toggle("hidden", mode !== "chat");
  $("modeSubday").classList.toggle("hidden", mode !== "subday");
  $("modeViewing").classList.toggle("hidden", mode !== "viewing");
  if(mode === "chat"){ chatStick = true; chatWrap.scrollTop = chatWrap.scrollHeight; }
  slideAll();
  const pane = {chat:$("modeChat"), viewing:$("modeViewing")}[mode] || $("modeSubday"); // вміст вкладки теж «прослизає»
  pane.classList.remove("slidein"); void pane.offsetWidth; pane.classList.add("slidein");
}
// СабДей активний (збір іде / пауза / гра й голосування) — на «Переглядовий» не переходимо
function subdayBusy(){
  if(!S) return false;
  const st = S.session && S.session.status;
  return st === "running" || st === "paused" || S.phase === "play";
}
// аукціон іде (ставки приймаються або пауза) — на «СабДей» не переходимо
function auctionBusy(){
  const t = S && S.auction && S.auction.timer && S.auction.timer.status;
  return t === "running" || t === "paused";
}
let denyBusy = false, blinkUntil = 0;
function denyMode(btn){
  if(denyBusy) return; denyBusy = true;
  const box = $("modes"), ind = box.querySelector(":scope > .seg-ind"), cur = box.querySelector(":scope > .active");
  if(ind && cur){
    // 1) підкладка рушає до «Переглядового», але зупиняється на півдорозі
    const x = cur.offsetLeft + (btn.offsetLeft - cur.offsetLeft) * 0.3; // лише третина шляху
    const w = cur.offsetWidth + (btn.offsetWidth - cur.offsetWidth) * 0.3;
    ind.style.transform = `translateX(${x}px)`; ind.style.width = w + "px";
    // 2) вібрує — «не можна»; 3) повертається під «СабДей»
    setTimeout(() => { ind.classList.add("deny"); btn.classList.add("denied"); }, 260);
    setTimeout(() => { ind.classList.remove("deny"); btn.classList.remove("denied"); slideSeg(box); denyBusy = false; }, 820);
  } else denyBusy = false;
  const home = btn.dataset.mode === "subday" ? "viewing" : "subday"; // вкладка, що зайнята
  if(mode !== home) showMode(home);
  modeNote(home === "viewing" ? "Спершу завершіть аукціон (прийом ставок)" : S.phase === "play" ? "Спершу завершіть гру в СабДеї" : "Спершу завершіть збір у СабДеї");
}
// причина відмови — коротка примітка під перемикачем режимів
let modeNoteT = 0;
function modeNote(msg){
  const box = $("modes"); let n = $("modeNote");
  if(!n){ n = document.createElement("div"); n.id = "modeNote"; n.className = "modenote"; n.setAttribute("role", "status"); box.appendChild(n); }
  n.textContent = msg; n.classList.add("show");
  clearTimeout(modeNoteT); modeNoteT = setTimeout(() => n.classList.remove("show"), 2800);
}
document.querySelectorAll(".mode-tab[data-mode]").forEach(b => b.onclick = () => {
  if(b.dataset.mode === mode) return; // уже відкрита — без анімації переходу
  if(b.dataset.mode === "viewing" && subdayBusy()) return denyMode(b);
  if(b.dataset.mode === "subday" && mode !== "subday" && auctionBusy()) return denyMode(b);
  showMode(b.dataset.mode);
});
showMode(mode);

/* ---------- налаштування: окремо для кожної вкладки ---------- */
function initInputs(){}
const n = (id, d) => { const v = Number($(id).value); return Number.isFinite(v) && $(id).value !== "" ? v : d; };
async function saveSettings(patch){ setState(await call("SaveSettings", {...S.settings, ...patch})); }
const openModal = id => $(id).classList.add("show"), closeModal = id => $(id).classList.remove("show");
document.querySelectorAll("[data-close]").forEach(b => b.onclick = () => { closeModal(b.dataset.close); pendingReward = null; auPending = null; });
// будь-яке вікно закривається через Esc або клік поза ним (підтвердження = «Скасувати»)
function dismissModal(m){
  if(m.id === "modal"){ $("modalNo").click(); return; }
  closeModal(m.id); pendingReward = null;
}
// фокус у вікнах: при відкритті — всередину вікна, Tab ходить лише по ньому, при закритті — назад до кнопки, що відкрила
const FOCUSABLE = 'button:not([disabled]),[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
const visibleIn = m => [...m.querySelectorAll(FOCUSABLE)].filter(el => el.offsetParent !== null);
const modalFocusObs = new MutationObserver(ms => ms.forEach(({target: m}) => {
  const shown = m.classList.contains("show");
  if(shown && !m._opener){
    m._opener = document.activeElement && !document.activeElement.closest(".modal") ? document.activeElement : null;
    m._opener = m._opener || document.body;
    requestAnimationFrame(() => { const f = visibleIn(m); const first = f.find(el => el.matches("input,select,textarea")) || f.find(el => !el.matches("[data-close]")) || f[0]; if(first) first.focus({preventScroll: true}); });
  } else if(!shown && m._opener){
    const o = m._opener; m._opener = null;
    if(o !== document.body && o.isConnected && !document.querySelector(".modal.show")) o.focus({preventScroll: true});
  }
}));
document.querySelectorAll(".modal").forEach(m => modalFocusObs.observe(m, {attributes: true, attributeFilter: ["class"]}));
document.addEventListener("keydown", e => {
  if(e.key !== "Tab") return;
  const open = [...document.querySelectorAll(".modal.show")]; if(!open.length) return;
  const f = visibleIn(open[open.length-1]); if(!f.length) return;
  const i = f.indexOf(document.activeElement);
  if(e.shiftKey && i <= 0){ e.preventDefault(); f[f.length-1].focus(); }
  else if(!e.shiftKey && (i === -1 || i === f.length - 1)){ e.preventDefault(); f[0].focus(); }
});
document.addEventListener("keydown", e => {
  if(e.key !== "Escape") return;
  const open = [...document.querySelectorAll(".modal.show")]; if(!open.length) return;
  e.preventDefault(); dismissModal(open[open.length-1]);
});
document.querySelectorAll(".modal").forEach(m => m.addEventListener("mousedown", e => { if(e.target === m) dismissModal(m); }));

// Загальні: віджет і порт OBS, файли; праворуч — Donatello через ngrok
$("generalBtn").onclick = () => {
  $("ngrokToken").value = S.settings.ngrokToken || ""; $("donKey").value = S.settings.donKey || ""; $("donLink")._k = "";
  $("obsPort").value = S.settings.obsPort; renderObs(); openModal("generalModal"); renderDon();
};
$("generalSave").onclick = async () => { await saveSettings({obsPort: Math.round(n("obsPort",17841)), ngrokToken: $("ngrokToken").value.trim(), donKey: $("donKey").value.trim()}); closeModal("generalModal"); toast("Збережено"); };
// Donatello: статус і посилання для колбеків (десктоп — через ngrok, вебверсія — адреса сервера)
function renderDon(){
  const d = S.don || {state: "off"}, pill = $("donPill");
  if(!(S.auth && S.auth.status === "ok")){ pill.classList.add("hidden"); return; } // без входу в Twitch індикатора немає
  pill.className = "donpill " + ({on: "on", error: "err", starting: ""}[d.state] ?? "") + (d.state === "off" ? " hidden" : "");
  if(!$("generalModal").classList.contains("show")) return;
  // підключення до ngrok — рамка завантаження навколо правої панелі
  const panel = document.querySelector("#generalModal .donbox");
  // рамка тримається щонайменше 1.4 с — швидке підключення не дає короткого «блимання»
  if(d.state === "starting"){ clearTimeout(panel._offT); if(!panel._t0){ panel._t0 = performance.now(); loadOn(panel); } }
  else if(panel._t0){ const left = 1400 - (performance.now() - panel._t0); panel._t0 = 0; clearTimeout(panel._offT); panel._offT = setTimeout(() => loadOff(panel), Math.max(0, left)); }
  const box = $("donLink");
  const html = d.state === "on" ? `<div class="donurl"><code>${esc(d.url)}</code><button class="small" id="donCopy">Копіювати</button></div><span class="ok">Підключено</span>`
    : d.state === "starting" ? (d.error ? `<span class="muted">${esc(d.error)}</span>` : "")
    : d.state === "error" ? `<span class="err">${esc(d.error || "Помилка ngrok")}</span>`
    : d.url ? `<div class="donurl"><code>${esc(d.url)}</code><button class="small" id="donCopy">Копіювати</button></div><span class="muted">Це посилання вставте в Donatello.</span>` // вебверсія: посилання є завжди
    : `<span class="muted">Після збереження тут з'явиться посилання для Donatello.</span>`;
  if(box._k !== html){ box._k = html; box.innerHTML = html; }
}
$("donLink").addEventListener("click", async e => {
  if(!e.target.closest("#donCopy")) return;
  const ok = await call("CopyText", (S.don || {}).url || "");
  const b = e.target.closest("#donCopy"); b.textContent = ok ? "Скопійовано" : "Не вдалося"; setTimeout(() => b.textContent = "Копіювати", 1500);
});
// подаровані сабки глядача за 30 днів: +1 бал за кожну
const GIFT_ICON = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 11h16v9a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1zM3 7h18v4H3zM12 7v14" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M12 7c-1.5-3-5-3.6-5.5-1.5S9.5 7 12 7zm0 0c1.5-3 5-3.6 5.5-1.5S14.5 7 12 7z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/></svg>`;
// у колонці «Бали»: скільки з них — за подаровані сабки
const ptGift = e => e.gifts > 0 && !e.donation ? `<span class="ptgift" title="+${e.gifts} за подаровані сабки">${GIFT_ICON}+${e.gifts}</span>` : "";
// бейдж донату Donatello: серце + сума в гривнях
const DON_HEART = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 21s-7.5-4.6-9.6-9.2C.9 8.5 2.9 4.5 6.7 4.5c2.1 0 3.6 1.1 4.3 2.4.7-1.3 2.2-2.4 4.3-2.4 3.8 0 5.8 4 4.3 7.3C19.5 16.4 12 21 12 21z" fill="currentColor"/></svg>`;
const donChip = uah => `<span class="donchip">${DON_HEART}<i>₴</i><b>${fmtUAH(uah)}</b></span>`;
const fmtUAH = v => { const x = Number(v) || 0; return x % 1 ? x.toFixed(2).replace(".", ",") : String(x); };
$("openBase").onclick = () => call("OpenFolder", "");
// експорт за весь час: діалог «Зберегти як», потім програма показує файл у провіднику
document.querySelectorAll("[data-export]").forEach(b => b.onclick = async () => {
  if(b.classList.contains("is-loading")) return;
  await withLoading(b, () => call("ExportAll", b.dataset.export)).catch(() => {});
});

// СабДей: нагорода + правила балів
$("subdaySettingsBtn").onclick = () => {
  pendingReward = null; showAllRewards = false;
  dupPending = null; renderDupSeg();
  $("gameMin").value = S.settings.gameMin || 40; $("voteMin").value = S.settings.voteMin || 3; $("voteRevote").checked = !S.settings.voteFirst; $("donPerPoint").value = S.settings.donUahPerPoint || 30;
  renderRewardGrid(); openModal("subdayModal"); requestAnimationFrame(renderDupSeg);
  if(!(S.rewards||[]).length) withLoading([$("subdayModal").querySelector(".box"), $("rewardRefresh")], () => call("RefreshRewards")).then(() => renderRewardGrid());
};
$("subdaySave").onclick = async () => {
  if(pendingReward !== null && pendingReward !== S.settings.rewardId){
    const r = rewardById(pendingReward);
    setState(await call("SelectReward", pendingReward, r ? r.title : ""));
    if(r && r.is_user_input_required === false) toast("Увага: ця нагорода не вимагає тексту — замовлення без тексту ігноруються", 4500);
  }
  await saveSettings({ dupMode: dupPending || S.settings.dupMode || "last", gameMin: Math.min(600, Math.max(1, Math.round(n("gameMin",40)))), voteFirst: !$("voteRevote").checked, voteMin: Math.min(60, Math.max(1, Math.round(n("voteMin",3)))),
    donUahPerPoint: Math.min(100000, Math.max(1, Math.round(n("donPerPoint",30)))) });
  pendingReward = null; closeModal("subdayModal"); toast("Налаштування СабДею збережено");
};

// кілька замовлень від одного глядача: перше / останнє / ділити бали
let dupPending = null;
const DUP_HINT = {first: "Рахується перше замовлення глядача, наступні ігноруються.",
  last: "Рахується останнє замовлення глядача — він може передумати.",
  split: "Бали глядача діляться між усіма його замовленнями: 10 балів на 2 замовлення — по 5, на 3 — 3, 3 і 4."};
function renderDupSeg(){
  const m = dupPending || S.settings.dupMode || "last";
  document.querySelectorAll("#dupSeg .tab").forEach(b => b.classList.toggle("active", b.dataset.dup === m));
  slideSeg($("dupSeg")); $("dupHint").textContent = DUP_HINT[m] || "";
}
document.querySelectorAll("#dupSeg .tab").forEach(b => b.onclick = () => { dupPending = b.dataset.dup; renderDupSeg(); });
// Чат: вигляд + URL для OBS
$("chatSettingsBtn").onclick = () => {
  const c = S.settings.chat;
  $("chatFont").value = c.fontSize; $("chatFontVal").textContent = c.fontSize + " px";
  $("chatTime").checked = !c.hideTime; $("chatBadges").checked = !c.hideBadges;
  openModal("chatModal");
};
$("chatFont").oninput = () => $("chatFontVal").textContent = $("chatFont").value + " px";
$("chatSave").onclick = async () => {
  await saveSettings({chat: {fontSize: Math.round(n("chatFont",14)), hideTime: !$("chatTime").checked, hideBadges: !$("chatBadges").checked}});
  closeModal("chatModal"); toast("Налаштування чату збережено");
};

let lastStreamKey = "", lastCollKey = "";
/* ---------- анімація «скидання» (новий стрім / новий аукціон) ----------
   картки зборів падають, числа біжать до нуля, рядки таблиці з'їжджають; лише потім — новий стан */
let resetting = false, auResetting = false; // поки йде анімація — відповідні частини не перемальовуються
function countTo0(el, fmt = v => String(v), ms = 650){
  const from = Number(String(el.textContent).replace(/[^\d]/g, "")) || 0;
  if(!from) return Promise.resolve();
  return new Promise(res => {
    let done = false; const t0 = performance.now();
    const fin = () => { if(done) return; done = true; el.textContent = fmt(0); res(); };
    const step = now => { if(done) return; const k = Math.min(1, (now - t0) / ms); el.textContent = fmt(Math.round(from * Math.pow(1 - k, 3))); if(k < 1) requestAnimationFrame(step); else fin(); };
    requestAnimationFrame(step); setTimeout(fin, ms + 300);
  });
}
// кінець анімації — з запасним таймером: у прихованому чи згорнутому вікні анімації можуть стояти
function animDone(anim, ms){ return Promise.race([anim.finished.catch(() => {}), new Promise(r => setTimeout(r, ms))]); }
function animOut(els, frames, {dur = 420, stagger = 30, max = 14} = {}){
  return Promise.all([...els].map((el, i) => new Promise(res => {
    const delay = Math.min(i, max) * stagger, kf = typeof frames === "function" ? frames(el, i) : frames;
    el.animate(kf, {duration: dur, delay, easing: "cubic-bezier(.5,0,.75,0)", fill: "forwards"}).onfinish = res;
    setTimeout(res, dur + delay + 200);
  })));
}
const ROW_OUT = [{transform: "none", opacity: 1}, {transform: "translateX(-48px)", opacity: 0}];
// картки зборів падають і тануть, не виходячи за межі рядка (без смуги прокрутки)
const CHIP_DROP = (el, i) => [{transform: "none", opacity: 1}, {transform: `translateY(6px) rotate(${i % 2 ? 4 : -4}deg) scale(.96)`, opacity: .5, offset: .5},
  {transform: `translateY(12px) rotate(${i % 2 ? 8 : -8}deg) scale(.9)`, opacity: 0}];
async function resetAnimSubday(){
  resetting = true;
  $("collChips").classList.add("dropping");
  // по черзі, щоб було видно: 1) падають збори  2) з'їжджають рядки, цифри біжать до нуля  3) коротка пауза
  const chips = animOut($("collChips").querySelectorAll(".cchip"), CHIP_DROP, {dur: 750, stagger: 110, max: 10});
  await sleep(350);
  await Promise.all([
    chips,
    animOut($("tableWrap").querySelectorAll("tbody > tr"), ROW_OUT, {dur: 560, stagger: 55, max: 16}),
    countTo0($("sEntries"), undefined, 1100), countTo0($("sUsers"), undefined, 1100), countTo0($("sPoints"), undefined, 1100),
  ]);
  await sleep(250);
}
// «Новий аукціон»: сектори колеса швидко вибувають по одному (як при вибуванні — тьмяніють і стискаються,
// решта розходиться на їхнє місце), наприкінці — порожнє колесо
function drainWheel(total = 900){
  return new Promise(res => {
    let done = false; const finish = () => { if(done) return; done = true; wFade = null; wheelDraw([]); wDrawn = []; res(); };
    let secs = wheelSectors().map(s => ({...s}));
    if(wAnim || !secs.length || !$("wheelBox").clientWidth || RM.matches){ finish(); return; }
    $("wResult").className = "wresult";
    const order = secs.slice().sort((a, b) => a.w - b.w).map(s => s.id); // спершу найменші
    const per = Math.max(110, Math.min(260, total / secs.length));
    let i = 0, t0 = performance.now();
    const step = now => {
      if(done) return;
      const id = order[i], t = Math.min(1, (now - t0) / per), e = t * t * (3 - 2 * t);
      wFade = {id, k: 1 - Math.min(1, t * 1.4)};
      const cur = secs.map(s => s.id === id ? {...s, w: s.w * (1 - e)} : s).filter(s => s.w > 1e-6);
      if(cur.length) wheelDraw(cur);
      if(t >= 1){ secs = secs.filter(s => s.id !== id); i++; t0 = now; if(i >= order.length) return finish(); }
      requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
    setTimeout(finish, per * secs.length + 600); // якщо кадри призупинено (вкладка у фоні)
  });
}
// таймер аукціону плавно повертається до заданого часу (як «перемотування»), колір — «очікування»
function rewindAuTimer(ms = 1000){
  const el = $("auTimer"), parts = el.textContent.split(":").map(Number);
  const from = parts.reduce((a, v) => a * 60 + (v || 0), 0) * 1000, to = (AU().cfg.durationMin || 10) * 60000;
  el.className = "timer idle";
  return new Promise(res => {
    let done = false; const t0 = performance.now();
    const fin = () => { if(done) return; done = true; el.textContent = fmtTime(to); res(); };
    const step = now => { if(done) return; const k = Math.min(1, (now - t0) / ms), e = 1 - Math.pow(1 - k, 3);
      el.textContent = fmtTime(Math.round((from + (to - from) * e) / 1000) * 1000); if(k < 1) requestAnimationFrame(step); else fin(); };
    requestAnimationFrame(step); setTimeout(fin, ms + 300);
  });
}
// статус «Очікування старту»: старий напис виїжджає вгору, новий — знизу
async function swapAuStatus(){
  const el = $("auStatus");
  if(el.classList.contains("idle")) return;
  await animDone(el.animate([{opacity: 1, transform: "none"}, {opacity: 0, transform: "translateY(-8px)"}], {duration: 220, easing: "ease-in", fill: "forwards"}), 400);
  el.textContent = "Очікування старту"; el.className = "spill idle";
  el.getAnimations().forEach(a => a.cancel());
  await animDone(el.animate([{opacity: 0, transform: "translateY(8px)"}, {opacity: 1, transform: "none"}], {duration: 260, easing: "ease-out"}), 450);
}
// кнопки під колесом тьмяніють разом із секторами (новий аукціон — крутити ще нічого)
function dimWheelBtns(){
  return Promise.all([...document.querySelectorAll("#auWheelWin .wctl button")].map(b =>
    animDone(b.animate([{opacity: getComputedStyle(b).opacity}, {opacity: .5}], {duration: 700, easing: "ease-out", fill: "forwards"}), 900))); // .5 — як у вимкненої кнопки, без стрибка після
}
async function resetAnimAuction(){
  auResetting = true; auTimerAnim = true;
  await Promise.all([
    rewindAuTimer(1000), swapAuStatus(), dimWheelBtns(),
    animOut($("auList").querySelectorAll("tbody > tr, .autop-i"), ROW_OUT, {dur: 560, stagger: 55, max: 16}),
    animOut($("auFeed").querySelectorAll(".fbid"), [{transform: "none", opacity: 1}, {transform: "translateX(48px)", opacity: 0}], {dur: 560, stagger: 55, max: 16}),
    drainWheel(900),
    countTo0($("auLots"), undefined, 1100), countTo0($("auBids"), undefined, 1100), countTo0($("auSum"), fmtN, 1100),
  ]);
  await sleep(250);
}
/* ---------- етапи СабДею: 1 збір → 2 гра переможця ---------- */
// переможця можна вибрати раз на збір: після експорту в Marbles і поки його ще не вибрано
const canPickWinner = () => D().live && S.exported && S.phase !== "play" && !S.winner;
const HOLD_MS = 1500;
// рідина: смуга з градієнтом, хвилястий передній край, бульбашки, скляний відблиск
const LIQUID = `<span class="liquid"><span class="lq-fill"><b class="bub"></b><b class="bub"></b><b class="bub"></b><b class="bub"></b></span>`
  + `<svg class="lq-edge" viewBox="0 0 12 40" preserveAspectRatio="none" aria-hidden="true"><path d="M0 0 H5 C12 5 12 10 5 15 C-2 20 -2 25 5 30 C12 35 12 40 5 45 L0 45 Z"/><path class="e2" d="M0 0 H4 C9 6 9 12 4 18 C-1 24 -1 30 4 36 C9 42 9 48 4 54 L0 54 Z"/></svg>`
  + `<span class="lq-gloss"></span></span>`;
const LIQUID_RED = LIQUID.replace(/<\/span>$/, `<span class="ink"><i></i><i></i><i></i></span></span>`); // рідина + червона «фарба»
let holding = null; // {btn, name, p, t0, raf, mode:"fill"|"drain"}
const easeFill = t => t < .5 ? 4*t*t*t : 1 - Math.pow(-2*t + 2, 3) / 2; // плавно розганяється і плавно доходить
function setLevel(btn, p){ btn.style.setProperty("--p", p.toFixed(4)); btn.style.setProperty("--w", btn.offsetWidth + "px"); }
function holdStart(btn){
  if(holding && holding.btn !== btn) return;
  if(holding){ cancelAnimationFrame(holding.raf); }
  const p0 = holding ? holding.p : 0;
  holding = {btn, name: btn.dataset.win, p: p0, mode: "fill", t0: performance.now() - p0 * HOLD_MS};
  btn.classList.add("pressed"); btn.classList.remove("drain");
  const step = now => {
    if(!holding || holding.btn !== btn || holding.mode !== "fill") return;
    const t = Math.min(1, (now - holding.t0) / HOLD_MS);
    holding.p = t; setLevel(btn, easeFill(t));
    btn.classList.toggle("near", t > .8);
    if(t >= 1) return holdDone();
    holding.raf = requestAnimationFrame(step);
  };
  holding.raf = requestAnimationFrame(step);
}
function holdCancel(){
  if(!holding || holding.mode !== "fill") return;
  const h = holding; cancelAnimationFrame(h.raf); h.mode = "drain";
  h.btn.classList.remove("pressed","near"); h.btn.classList.add("drain");
  const from = easeFill(h.p), t0 = performance.now(), dur = 260 + 340 * from;
  const step = now => {
    if(holding !== h) return;
    const t = Math.min(1, (now - t0) / dur), lvl = from * (1 - (1 - Math.pow(1 - t, 3)));
    setLevel(h.btn, lvl); h.p = 0;
    if(t < 1) h.raf = requestAnimationFrame(step);
    else { h.btn.classList.remove("drain"); holding = null; if(tableDirty) renderTable(); if(auListDirty) renderAuList(); }
  };
  h.raf = requestAnimationFrame(step);
  setTimeout(() => { if(holding === h){ setLevel(h.btn, 0); holding = null; if(tableDirty) renderTable(); if(auListDirty) renderAuList(); } }, dur + 200);
}
async function holdDone(){
  const {btn, name} = holding; holding.mode = "done";
  setLevel(btn, 1); btn.classList.remove("pressed","near"); btn.classList.add("full");
  await new Promise(r => setTimeout(r, 380)); // «вистрибнула» і спалахнула
  const action = btn.dataset.hold;
  if(action === "stopcollect" || action === "stopauction"){ // завершити збір / прийом ставок
    holding = null;
    try{ setState(await call(action === "stopcollect" ? "FinishTimer" : "AuctionFinish")); } catch(e){}
    finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); }, 500); }
    return;
  }
  if(action === "newauction"){
    holding = null;
    try{
      await resetAnimAuction();
      const snap = await call("AuctionNew");
      auResetting = false; auTimerAnim = false; setState(snap);
      document.querySelectorAll("#auWheelWin .wctl button, #auStatus").forEach(b => b.getAnimations().forEach(a => a.cancel()));
      $("wheel").getAnimations().forEach(a => a.cancel());
      toast("Почато новий аукціон");
    } catch(e){ auResetting = false; auTimerAnim = false; $("wheel").getAnimations().forEach(a => a.cancel()); document.querySelectorAll("#auWheelWin .wctl button, #auStatus").forEach(b => b.getAnimations().forEach(a => a.cancel())); renderAuction(); renderAuTimer(); }
    finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); }, 500); }
    return;
  }
  if(action === "newstream"){
    holding = null;
    try{
      if(pastStream || view){ pastStream = null; view = null; renderAll(); } // з перегляду — спершу до поточного
      await resetAnimSubday();
      const snap = await call("NewStream");
      resetting = false; $("collChips").classList.remove("dropping"); setState(snap); loadStreams();
      const w = $("tableWrap"); w.classList.remove("slidein"); void w.offsetWidth; w.classList.add("slidein");
      $("collChips").querySelectorAll(".cchip").forEach(c => c.animate([{transform: "translateY(-10px)", opacity: 0}, {transform: "none", opacity: 1}], {duration: 380, easing: "ease-out"}));
      toast("Почато новий стрім");
    } catch(e){ resetting = false; $("collChips").classList.remove("dropping"); renderAll(); }
    finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); }, 500); }
    return;
  }
  if(action === "logout"){ holding = null; call("Logout"); return; }
  if(action.startsWith("web:")){ holding = null; if(window.webHold) window.webHold(action.slice(4), btn); return; } // вебверсія: web.js
  if(action === "dellot"){ // видалити лот
    holding = null;
    const l = lotById(btn.dataset.lot);
    try{ setState(await call("AuctionRemoveLot", btn.dataset.lot)); if(l) toast(`Лот «${l.name}» видалено`); } catch(e){ renderAuList(); }
    return;
  }
  if(action === "resetspins"){ // повернути всі лоти на колесо (і зупинити автооберти)
    holding = null;
    stopAutoRun();
    try{ setState(await call("AuctionResetSpins")); toast("Колесо скинуто"); } catch(e){} finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); renderSpinBtn(); }, 500); }
    return;
  }
  if(action === "spin" && AU().cfg.spinMode !== "instant"){ // на вибування — автооберти до переможця
    holding = null;
    setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); }, 500);
    autoRun();
    return;
  }
  if(action === "spin"){ // колесо: результат від Go (random.org), тут лише анімація
    holding = null;
    try{ setState(await call("AuctionSpin")); } catch(e){} finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); renderSpinBtn(); }, 500); }
    return;
  }
  if(action === "finishgame"){ // завершити гру — без блискіток
    holding = null;
    try{ setState(await call("FinishGame")); } finally { setTimeout(() => { btn.classList.remove("full"); setLevel(btn, 0); }, 500); }
    return;
  }
  await sparkles(1500);
  holding = null;
  try{ setState(await call("SetWinner", name)); } catch(e){ renderTable(); }
}
// затискання: «Переможець» у підсумку і «Завершити гру» на етапі 2
document.addEventListener("pointerdown", e => {
  const b = e.target.closest("button[data-win], button[data-hold]"); if(!b || e.button !== 0 || b.disabled) return;
  e.preventDefault(); try{ b.setPointerCapture(e.pointerId); }catch(_){}
  holdStart(b);
});
// відпустили раніше (де завгодно) — рідина стікає назад
["pointerup","pointercancel"].forEach(t => document.addEventListener(t, () => holdCancel()));
// з клавіатури: затиснути Пробіл або Enter на кнопці — те саме, що затиснути мишею
const HOLD_KEYS = [" ", "Enter"];
document.addEventListener("keydown", e => {
  if(!HOLD_KEYS.includes(e.key)) return;
  const b = e.target.closest && e.target.closest("button[data-win], button[data-hold]"); if(!b || b.disabled) return;
  e.preventDefault(); if(!e.repeat) holdStart(b);
});
document.addEventListener("keyup", e => {
  if(!HOLD_KEYS.includes(e.key)) return;
  const b = e.target.closest && e.target.closest("button[data-win], button[data-hold]"); if(!b) return;
  e.preventDefault(); holdCancel();
});
window.addEventListener("blur", () => holdCancel());

// зелено-фіолетові блискітки, що злітаються від меж екрана
function sparkles(ms){
  if(RM.matches) return Promise.resolve(); // зменшений рух — без блискіток
  return new Promise(resolve => {
    const cv = $("sparkles"), ctx = cv.getContext("2d"), W = cv.width = innerWidth, H = cv.height = innerHeight;
    cv.classList.add("on");
    const parts = [], N = 170, cx = W/2, cy = H/2;
    for(let i=0;i<N;i++){
      const side = i % 4, t = Math.random();
      const x = side===0 ? t*W : side===1 ? W : side===2 ? t*W : 0;
      const y = side===0 ? 0 : side===1 ? t*H : side===2 ? H : t*H;
      const ang = Math.atan2(cy - y, cx - x) + (Math.random()-.5)*0.9, sp = 3 + Math.random()*7;
      const mix = Math.random();
      parts.push({x, y, vx:Math.cos(ang)*sp, vy:Math.sin(ang)*sp, r:1.5+Math.random()*3.5, rot:Math.random()*6, vr:(Math.random()-.5)*.3,
        c:`rgb(${Math.round(0+145*mix)},${Math.round(200-129*mix)},${Math.round(83+172*mix)})`, d:Math.random()*250});
    }
    const t0 = performance.now();
    function star(p, a){
      ctx.save(); ctx.translate(p.x, p.y); ctx.rotate(p.rot); ctx.globalAlpha = a; ctx.fillStyle = p.c;
      ctx.shadowColor = p.c; ctx.shadowBlur = 12;
      ctx.beginPath(); for(let k=0;k<8;k++){ const rr = k%2 ? p.r*.45 : p.r*2.2, aa = k*Math.PI/4; ctx.lineTo(Math.cos(aa)*rr, Math.sin(aa)*rr); } ctx.closePath(); ctx.fill(); ctx.restore();
    }
    function frame(now){
      const el = now - t0; ctx.clearRect(0,0,W,H);
      for(const p of parts){
        if(el < p.d) continue;
        p.x += p.vx; p.y += p.vy; p.vx *= .985; p.vy *= .985; p.rot += p.vr;
        const life = (el - p.d) / (ms - 250), a = Math.max(0, Math.min(1, life < .2 ? life*5 : 1 - (life-.2)/.8)) * (.6 + .4*Math.sin(el/60 + p.r));
        star(p, a);
      }
      if(el < ms && !done) requestAnimationFrame(frame); else finish();
    }
    let done = false;
    function finish(){ if(done) return; done = true; ctx.clearRect(0,0,W,H); cv.classList.remove("on"); resolve(); }
    setTimeout(finish, ms + 150); // гарантія: перехід відбудеться, навіть якщо анімацію кадрів призупинено
    requestAnimationFrame(frame);
  });
}

function remainOf(t){ return t.status==="running" ? t.endsAt - Date.now() : (t.remainingMs || 0); }
let shownStep = 0;
function renderSteps(){
  const play = S.phase === "play";
  $("stepper").classList.toggle("hidden", !(play || S.exported));
  $("stepper").querySelectorAll(".step").forEach(el => {
    const n = Number(el.dataset.step);
    el.classList.toggle("active", (play ? 2 : 1) === n);
    el.classList.toggle("done", play && n === 1);
  });
  $("stepper").classList.toggle("at2", play);
  const step = play ? 2 : 1;
  if(step !== shownStep){
    $("stepTrack").classList.toggle("at2", play);
    if(shownStep && !play) toast(S.vote && S.vote.result === "next" ? "Чат вирішив: дроп" : "Етап 1: збір замовлень");
    shownStep = step;
  }
  if(!play) return;
  $("gLabel").textContent = `Збір №${S.collN || ""}` + (S.round > 1 ? ` · раунд ${S.round}` : "");
  $("gName").textContent = S.winner || "—";
  const g = aggregate(LIVE()).find(x => x.name === S.winner || x.key === norm(S.winner||""));
  $("gUsers").innerHTML = g && g.users.length ? `<span class="gwho">${g.users.length > 1 ? "Замовили:" : "Замовив:"}</span> ` + g.users.map(u => `${userTag(u)} <span class="muted">(${u.p})</span>`).join(" ") : "";
  $("vStart").textContent = `Почати голосування · ${S.settings.voteMin || 3} хв`;
  $("voteRule").innerHTML = `Чатерси пишуть <b>1</b> або <b>2</b> — від кожного рахується ${S.settings.voteFirst ? "лише перший голос" : "останній голос"}.`;
  renderGameTimer();
}
function gameSection(){
  const g = S.game || {}, v = S.vote || {};
  if(v.status === "running") return "gVote";
  if(g.status === "finished") return v.status === "finished" && v.result === "tie" ? "gTie" : "gOver";
  return "gPlay";
}
let lastV = "";
function renderGameTimer(){
  if(!S || S.phase !== "play") return;
  const sec = gameSection();
  // час гри вийшов: той самий таймер (00:00) лишається, шапка згортається, знизу плавно розгортається блок з кнопками
  const over = sec === "gOver";
  document.querySelectorAll("#pane2 .gamecard .gsec").forEach(x => x.classList.toggle("on", x.id === sec || (over && x.id === "gPlay")));
  $("gOver").classList.toggle("open", over);
  $("gPlayCtl").classList.toggle("gone", sec !== "gPlay");
  $("gWrap").classList.toggle("ended", sec !== "gPlay");
  if(over){ $("gTimer").textContent = "00:00"; $("gTimer").className = "gtimer finished"; }
  const g = S.game || {}, v = S.vote || {};
  if(sec === "gPlay"){
    const r = remainOf(g);
    $("gTimer").textContent = fmtTime(r);
    $("gTimer").className = "gtimer " + (g.status||"") + (g.status==="running" && r <= 60000 ? " low" : "") + (Date.now() < blinkUntil ? " gblink" : "");
    $("gStatus").textContent = g.status==="paused" ? "Пауза" : "Гра йде. Коли час вийде — запустіть голосування за продовження.";
    $("gPause").textContent = g.status==="paused" ? "Продовжити" : "Пауза";
  }
  if(sec === "gVote"){
    const r = remainOf(v), tot = (v.yes||0) + (v.no||0);
    $("vTimer").textContent = fmtTime(r);
    $("vTimer").classList.toggle("low", r <= 15000);
    $("vf1").style.width = (tot ? v.yes/tot*100 : 0) + "%"; $("vf2").style.width = (tot ? v.no/tot*100 : 0) + "%";
    const k = `${v.yes}:${v.no}`;
    if(k !== lastV){ $("vn1").textContent = `${v.yes||0}${tot?` · ${Math.round(v.yes/tot*100)}%`:""}`; $("vn2").textContent = `${v.no||0}${tot?` · ${Math.round(v.no/tot*100)}%`:""}`; lastV = k; }
  }
  if(sec === "gTie") $("tieInfo").textContent = `Голоси розділились порівну: 1 — ${v.yes||0}, 2 — ${v.no||0}. Переголосуйте або вирішіть самі.`;
}
setInterval(renderGameTimer, 250);
$("gFinish").insertAdjacentHTML("afterbegin", LIQUID);
$("gFinish").querySelector(".liquid").insertAdjacentHTML("beforeend", `<span class="ink"><i></i><i></i><i></i></span>`); // червона «фарба»
$("gPause").onclick = async () => setState(await call(S.game.status==="paused" ? "GameResume" : "GamePause"));
$("gPlus").onclick = async () => setState(await call("GameAddTime", 60000));
$("gMinus").onclick = async () => setState(await call("GameAddTime", -60000));
$("vStart").onclick = $("tRevote").onclick = async () => setState(await call("StartVote")); // нічия — можна переголосувати
$("vPlus").onclick = async () => setState(await call("VoteAddTime", 60000));
$("vFinish").onclick = async () => setState(await call("FinishVote"));
$("gContinue").onclick = $("tContinue").onclick = async () => setState(await call("ContinueGame"));
$("gNext").onclick = $("tNext").onclick = async () => setState(await call("NextCollection"));

// вкладка «Журнал» — лише в тестовій збірці (version = dev); у релізі її немає
function renderLogTab(){
  const t = document.querySelector('.tabs .tab[data-tab="log"]'), prod = !!S.version && S.version !== "dev";
  t.classList.toggle("hidden", prod);
  if(prod && tab === "log"){ tab = "summary"; document.querySelectorAll(".tabs .tab").forEach(x => x.classList.toggle("active", x.dataset.tab === tab)); renderTable(); slideSeg(document.querySelector(".tabs .seg")); }
}
function renderAll(){
  if(!inited){ initInputs(); inited = true; }
  renderAuth();
  if(S.auth && S.auth.status==="ok"){
    if(view && !viewColl()) view = null;             // збір зник (новий стрім) — до поточного
    if(S.stream && S.stream.id !== lastStreamKey){ lastStreamKey = S.stream.id; loadStreams(); }
    const ck = (S.past||[]).length + ":" + S.collN + ":" + S.session.status;
    if(ck !== lastCollKey){ lastCollKey = ck; if(streams.length) loadStreams(); }
    if(!resetting){ renderStreamBar(); renderStats(); renderTable(); renderRewardCurrent(); renderSteps(); }
    renderObs(); renderChat(); slideAll();
    renderAuction(); renderDon(); renderLogTab();
    if($("subdayModal").classList.contains("show")) renderRewardGrid();
    if(!chatLoaded){ chatLoaded = true; api().GetChat().then(m => { chatPending = (m||[]).concat(chatPending); }); }
  }
  renderTimer();
}

/* ---------- події ---------- */
document.addEventListener("click", e => {
  const a = e.target.closest("a[data-url]");
  if(a){ e.preventDefault(); if(a.dataset.url) call("OpenURL", a.dataset.url); }
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
$("stopBtn").insertAdjacentHTML("afterbegin", LIQUID_RED); // «Завершити» збір — затисканням
$("plusBtn").onclick = async () => setState(await call("AddTime", 60000));
$("minusBtn").onclick = async () => setState(await call("AddTime", -60000));

document.querySelectorAll(".tabs .tab").forEach(b => b.onclick = () => {
  if(b.dataset.tab === tab) return; // уже відкрита — без анімації переходу
  tab = b.dataset.tab; document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("active",x===b)); renderTable();
  slideSeg(document.querySelector(".tabs .seg"));
  const w = $("tableWrap"); w.classList.remove("slidein"); void w.offsetWidth; w.classList.add("slidein");
});
document.fonts && document.fonts.ready.then(slideAll); // після завантаження шрифтів ширини вкладок уточнюються

$("tableWrap").addEventListener("click", async e => {
  const h = e.target.closest("th[data-sort]");
  if(h){ const k=h.dataset.sort, cur=sortKey[tab]; sortKey[tab] = [k, cur[0]===k ? -cur[1] : (k==="name"||k==="user"||k==="text"?1:-1)]; renderTable(); return; }
  if(e.target.closest("button[data-win]")){ e.stopPropagation(); return; } // вибір — лише затисканням
  const gr = e.target.closest("tr.grow");
  if(gr && !e.target.closest(".grip")){ const k = gr.dataset.key; openGroups.has(k) ? openGroups.delete(k) : openGroups.add(k); renderTable(); return; }
  const btn = e.target.closest("button[data-act]"); if(!btn) return;
  if(btn.dataset.act==="editmode"){ editMode = !editMode; renderTable(); return; }
  const id = btn.closest("tr").dataset.id, ent = S.entries.find(x=>x.id===id); if(!ent) return;
  if(btn.dataset.act==="toggle") setState(await call("UpdateEntry", {...ent, excluded: !ent.excluded}));
  if(btn.dataset.act==="del"){ if(!(await confirmBox(`Видалити замовлення «${ent.text}» від ${ent.user}?`, "Видалити замовлення"))) return; setState(await call("DeleteEntry", id)); }
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
    btn.className = "mbtn ok"; label.textContent = r.copied ? "✓ Шлях скопійовано" : document.documentElement.classList.contains("web") ? "✓ Файл завантажено" : "✓ Файл створено";
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
  if(S.exported && S.phase !== "play" && tab === "summary") toast("Тепер виберіть переможця в «Підсумку»", 3500);
}
$("exportMarbles").onclick = exportMarbles;
setInterval(renderExportBtn, 500); // таймер може завершитися сам
/* ---------- «Переглядовий»: аукціон ----------
   Ставки — нагороди за бали каналу (до 5), сума = ціна нагороди, текст = назва лота.
   Два вікна (лоти / колесо) міняються місцями з анімацією; під колесом завжди стрічка ставок. */
const AU = () => S.auction || {cfg:{rewardIds:[]}, timer:{status:"idle"}, lots:[], feed:[]};
const fmtN = x => Number(x||0).toLocaleString("uk-UA");
// лоти — чітко різні кольори (не відтінки одного), щоб на колесі й у списку кожен знаходився одразу
const PAL = ["#4f8ff7","#e0952e","#e5604f","#2fb59a","#d66ba0","#8f6cf0","#7fae32","#3aa9cc","#e07a3a","#b25bd6","#c9a43a","#5d6ff0"];
function lotColor(id){ const i = AU().lots.findIndex(l => l.id === id); return PAL[(i < 0 ? 0 : i) % PAL.length]; }
const lotById = id => AU().lots.find(l => l.id === id);
let auSwapped = false;
try{ auSwapped = localStorage.getItem("phillbot-auswap") === "1"; }catch(e){}
$("auWrap").classList.toggle("swapped", auSwapped);

// таймер прийому ставок
let auTimerAnim = false; // «Новий аукціон»: таймер сам відкручується до заданого часу — не перемальовувати поверх
function renderAuTimer(){
  if(!S || auTimerAnim) return;
  const t = AU().timer, r = t.status === "running" ? t.endsAt - Date.now() : (t.remainingMs || 0), el = $("auTimer");
  el.textContent = fmtTime(t.status === "finished" ? 0 : r);
  el.className = "timer " + t.status + (t.status === "running" && r <= 30000 ? " low" : "");
  $("auStatus").textContent = {idle:"Очікування старту", running:"Ставки приймаються", paused:"Пауза", finished:"Ставки закрито"}[t.status] || "";
  $("auStatus").className = "spill " + (t.status || "idle");
  $("auStart").disabled = t.status === "running";
  $("auPause").disabled = t.status !== "running";
  $("auStop").disabled = !(t.status === "running" || t.status === "paused");
  const going = t.status === "running" || t.status === "paused";
  $("auMinus").disabled = !going; $("auPlus").disabled = !going;
  renderSpinBtn();
}
setInterval(renderAuTimer, 250);

// частки на колесі: «одразу результат» — пропорційно сумі; «на вибування» — шанс вибути ∝ сумі решти лотів
// (лише так шанс перемогти точно дорівнює сума/загальна сума — див. elimWeights у auction.go)
function wheelSectors(){
  const au = AU(), elim = au.cfg.spinMode !== "instant";
  const act = au.lots.filter(l => !l.out && !l.hidden && l.amount > 0), S = act.reduce((s, l) => s + l.amount, 0);
  return act.map(l => ({id: l.id, w: elim ? (act.length > 1 ? S - l.amount : 1) : l.amount}));
}
function chanceOf(lot){
  // шанс перемогти — від сум усіх лотів на колесі до обертів (вибування його не обнуляє)
  const act = AU().lots.filter(l => !l.hidden && l.amount > 0); if(lot.hidden || !act.length) return 0;
  return lot.amount / act.reduce((a, l) => a + l.amount, 0); // шанс перемогти пропорційний сумі
}

// ----- вікно 1: лоти -----
const auFresh = new Set();
let auDrag = null, auListDirty = false, auEdit = false, auRun = null, auLeaving = false; // auRun — автооберти «на вибування» // id лота, який перетягують; режим редагування
function renderAuList(){
  // не перемальовуємо таблицю посеред перетягування чи затискання кнопки в ній
  const typing = document.activeElement && document.activeElement.matches("input.edit") && auListEl.contains(document.activeElement);
  if(auDrag || typing || (holding && auListEl.contains(holding.btn))){ auListDirty = true; return; }
  auListDirty = false;
  const gone = l => l.out || l.hidden ? 1 : 0;
  const au = AU(), lots = [...au.lots].sort((a, b) => gone(a) - gone(b) || b.amount - a.amount);
  const active = au.lots.filter(l => !gone(l)).length;
  $("auListHint").textContent = au.lots.length ? `${active} на колесі` + (au.lots.length > active ? ` · ${au.lots.length - active} вибуло` : "") : "";
  if(!lots.length){ $("auList").innerHTML = `<div class="empty">Лотів ще немає</div>`; return; }
  if(auSwapped){ // стисло: топ-3 та інформація
    // завжди топ-3: переможець — найвище, далі за сумою (вибулі теж, приглушені)
    const win = lots.find(l => l.id === au.winner);
    // під час автообертів — усі лоти, що ще на колесі; інакше — топ-3 (переможець найвище)
    const top = auRun && !au.winner ? lots.filter(l => !gone(l)) : (win ? [win, ...lots.filter(l => l !== win)] : lots).slice(0, 3);
    if(auRun){
      const keep = new Set(top.map(l => l.id));
      const leaving = [...auListEl.querySelectorAll(".autop-i[data-lot]")].filter(el => !keep.has(el.dataset.lot));
      if(auLeaving) return;
      if(leaving.length){ // вибулий лот виїжджає вправо, рядок згортається — потім перемальовуємо
        auLeaving = true;
        Promise.all(leaving.map(el => { const h = el.offsetHeight + "px";
          const an = el.animate([{transform: "none", opacity: 1, height: h}, {transform: "translateX(70px)", opacity: 0, height: h, offset: .55},
            {transform: "translateX(70px)", opacity: 0, height: "0px", paddingTop: "0px", paddingBottom: "0px", marginTop: "-8px"}],
            {duration: 700, easing: "ease-in", fill: "forwards"}); return animDone(an, 900); }))
          .then(() => { leaving.forEach(el => el.remove()); auLeaving = false; renderAuList(); }); // прибрати рядки, що виїхали, — інакше вони «виїжджали» б знову й знову
        return;
      }
    }
    // FLIP: запам'ятати, де були рядки, щоб плавно пересунути (переможець «злітає» нагору)
    const before = {};
    auListEl.querySelectorAll(".autop-i[data-lot]").forEach(el => before[el.dataset.lot] = el.getBoundingClientRect().top);
    $("auList").innerHTML = `<div class="autop">` + top.map((l, i) => `<div class="autop-i${au.winner === l.id ? " win wglow" : ""}${gone(l) && au.winner !== l.id ? " out" : ""}" data-lot="${esc(l.id)}" style="--c:${lotColor(l.id)}">
        <span class="nm"><span class="ldot" style="background:${lotColor(l.id)}"></span>${esc(l.name)}</span><span class="am" title="${fmtN(l.amount)} балів">${l.hidden ? "приховано" : au.winner === l.id ? `Переміг із ${(chanceOf(l) * 100).toFixed(1)}%` : `${(chanceOf(l) * 100).toFixed(1)}%`}</span>
        <span class="bar"><i style="width:${(chanceOf(l) * 100).toFixed(1)}%"></i></span></div>`).join("")
      + `</div>` + (!auRun && au.lots.length > 3 ? `<div class="auinfo">і ще ${au.lots.length - 3} ${au.lots.length - 3 === 1 ? "лот" : "лотів"}</div>` : "");
    auListEl.querySelectorAll(".autop-i[data-lot]").forEach(el => {
      const was = before[el.dataset.lot], now = el.getBoundingClientRect().top;
      if(was === undefined){ if(Object.keys(before).length) el.animate([{opacity: 0, transform: "translateY(10px)"}, {opacity: 1, transform: "none"}], {duration: 450, easing: "ease-out"}); return; }
      if(Math.abs(was - now) < 1) return;
      const up = was > now; // рядок піднімається — над іншими, з легким збільшенням
      el.style.zIndex = up ? 2 : 1;
      el.animate([{transform: `translateY(${was - now}px)` + (up ? " scale(1.04)" : "")}, {transform: up ? "scale(1.04)" : "none", offset: .7}, {transform: "none"}],
        {duration: 750, easing: "cubic-bezier(.2,.9,.25,1)"}).onfinish = () => el.style.zIndex = "";
    });
    return;
  }
  $("auList").innerHTML = `<table class="autable"><thead><tr><th class="gripcol"></th><th class="num">#</th><th>Лот</th><th class="num">Сума</th><th class="num">Шанс</th><th>Хто ставив</th><th class="acts"><button class="small editbtn${auEdit ? " on" : ""}" data-lotedit>${auEdit ? "Готово" : "Редагувати"}</button></th></tr></thead><tbody>`
    + lots.map((l, i) => {
      const who = [...l.users].sort((a, b) => b.amount - a.amount);
      const users = who.slice(0, 6).map(u => `<span title="${fmtN(u.amount)} балів">${userTag(u)}</span>`).join(" ") + (who.length > 6 ? ` <span class="muted">+${who.length - 6}</span>` : "");
      const pct = `${(chanceOf(l) * 100).toFixed(1)}%`;
      const st = l.hidden ? `<span class="muted">приховано</span>` : au.winner === l.id ? `<span class="lwin">Переміг із ${pct}</span>` : pct;
      return `<tr data-lot="${esc(l.id)}"${au.winner ? "" : ' draggable="true"'} class="${l.hidden ? "off " : ""}${l.out ? "out" : ""}${au.winner === l.id ? " win" : ""}${auFresh.has(l.id) ? " fresh" : ""}">
        <td class="gripcol"><span class="grip" title="Перетягніть рядок на інший лот, щоб об'єднати їх">⠿</span></td>
        <td class="num">${l.out ? "" : i + 1}</td>
        <td><span class="ldot" style="background:${lotColor(l.id)}"></span>${auEdit ? `<input class="edit lnamein" data-lf="name" value="${esc(l.name)}">` : `<b class="lname">${esc(l.name)}</b>`}<span class="mtag"></span></td>
        <td class="num lamt">${auEdit ? `<input class="edit lamtin" data-lf="amount" type="number" min="0" step="100" value="${l.amount}">` : fmtN(l.amount)}</td><td class="num">${st}</td>
        <td class="users">${users}</td>
        <td class="acts"><button class="icon${l.hidden ? " on" : ""}" data-lothide="${esc(l.id)}" title="${l.hidden ? "Показати — повернути на колесо" : "Приховати — прибрати з колеса (можна повернути)"}">${l.hidden ? ICON_EYE_OFF : ICON_EYE}</button>${auEdit ? `<button class="winbtn holdend iconhold" data-hold="dellot" data-lot="${esc(l.id)}" title="Затисніть і тримайте, щоб видалити лот">${LIQUID_RED}<span class="wl">${ICON_TRASH}</span></button>` : ""}</td></tr>`;
    }).join("") + `</tbody></table>`;
}
// пряме редагування: назва і сума (Enter або вихід з поля — зберегти)
$("auList").addEventListener("change", async e => {
  const f = e.target.dataset.lf; if(!f) return;
  const id = e.target.closest("tr[data-lot]").dataset.lot, l = lotById(id); if(!l) return;
  const v = e.target.value.trim();
  e.target.blur();
  if(f === "name"){ if(!v || v === l.name){ e.target.value = l.name; return; } setState(await call("AuctionUpdateLot", id, v, -1)); }
  if(f === "amount"){ const n = Math.max(0, Math.round(Number(v))); if(!Number.isFinite(n) || n === l.amount){ e.target.value = l.amount; return; } setState(await call("AuctionUpdateLot", id, "", n)); }
});
$("auList").addEventListener("keydown", e => { if(e.key === "Enter" && e.target.matches("input.edit")) e.target.blur(); });
$("auList").addEventListener("focusout", () => setTimeout(() => { if(auListDirty) renderAuList(); }, 0));
// приховати / показати лот (прихований не бере участі в колесі)
$("auList").addEventListener("click", async e => {
  if(e.target.closest("[data-lotedit]")){ auEdit = !auEdit; renderAuList(); return; }
  const b = e.target.closest("[data-lothide]"); if(!b) return;
  setState(await call("AuctionToggleLot", b.dataset.lothide));
});

/* drag-n-drop: перетягніть лот на інший, щоб об'єднати (як замовлення в СабДеї) */
const auListEl = $("auList");
auListEl.addEventListener("dragstart", ev => {
  const tr = ev.target.closest && ev.target.closest("tr[data-lot][draggable]"); if(!tr){ ev.preventDefault(); return; }
  auDrag = tr.dataset.lot; tr.classList.add("dragging"); ev.dataTransfer.effectAllowed = "move";
  try{ ev.dataTransfer.setData("text/plain", (lotById(auDrag) || {}).name || ""); ev.dataTransfer.setDragImage(tr, 20, 16); }catch(e){}
});
auListEl.addEventListener("dragover", ev => {
  if(!auDrag) return;
  const tr = ev.target.closest("tr[data-lot]"); if(!tr || tr.dataset.lot === auDrag) return;
  ev.preventDefault(); ev.dataTransfer.dropEffect = "move";
  if(tr.classList.contains("drop")) return;
  auListEl.querySelectorAll("tr.drop").forEach(x => { x.classList.remove("drop"); x.querySelector(".mtag").textContent = ""; });
  tr.classList.add("drop");
  // одразу видно, що куди зіллється і яка буде сума
  const from = lotById(auDrag), to = lotById(tr.dataset.lot);
  if(from && to){
    tr.querySelector(".mtag").innerHTML = `<i>+</i> ${esc(from.name)} <b>= ${fmtN(from.amount + to.amount)}</b>`;
    const src = auListEl.querySelector(`tr[data-lot="${CSS.escape(auDrag)}"] .mtag`);
    if(src) src.innerHTML = `<i>→</i> ${esc(to.name)}`;
  }
});
function auClearTags(){ auListEl.querySelectorAll(".mtag").forEach(x => x.textContent = ""); }
auListEl.addEventListener("dragleave", ev => {
  const tr = ev.target.closest && ev.target.closest("tr[data-lot]");
  if(tr && !tr.contains(ev.relatedTarget)){ tr.classList.remove("drop"); auClearTags(); }
});
function auDragEnd(){ auDrag = null; auListEl.querySelectorAll("tr.drop,tr.dragging").forEach(x => x.classList.remove("drop", "dragging")); auClearTags(); if(auListDirty) renderAuList(); }
auListEl.addEventListener("dragend", auDragEnd);
auListEl.addEventListener("drop", async ev => {
  ev.preventDefault();
  const from = lotById(auDrag), tr = ev.target.closest("tr[data-lot]"), to = tr && lotById(tr.dataset.lot);
  auDragEnd();
  if(!from || !to || from.id === to.id) return;
  setState(await call("AuctionMergeLots", from.id, to.id)); // без підтвердження — є «Скасувати об'єднання»
  auFresh.add(to.id); setTimeout(() => auFresh.delete(to.id), 2500); renderAuList();
  toast(`«${from.name}» → «${to.name}»`);
});
// «Скасувати об'єднання» — останнє об'єднання повертається
function renderAuUndo(){
  const m = AU().merges || [], last = m[m.length - 1], b = $("auUndo");
  b.disabled = !last || !!AU().winner;
  const to = last && lotById(last.to);
  b.title = last ? `Скасувати лише останнє: «${last.src.name}» → «${to ? to.name : "?"}». «${last.src.name}» знову стане окремим лотом` + (m.length > 1 ? ` (ще ${m.length - 1} об'єднань можна скасувати далі)` : "") : "Немає об'єднань";
}
$("auUndo").onclick = async () => {
  const m = AU().merges || [], last = m[m.length - 1]; if(!last) return;
  setState(await call("AuctionUndoMerge"));
  auFresh.add(last.src.id); setTimeout(() => auFresh.delete(last.src.id), 2500); renderAuList();
  toast(`«${last.src.name}» знову окремим лотом`);
};

// ----- стрічка ставок -----
function renderAuFeed(){
  const au = AU(), pend = au.feed.filter(b => b.status === "pending").length;
  $("auFeedHint").innerHTML = pend ? `<span class="aupend">${pend} чекає рішення</span>` : (au.feed.length ? `${au.feed.length}` : "");
  if(!au.feed.length){ $("auFeed").innerHTML = `<div class="empty small">Тут з'являтимуться ставки по мірі надходження.</div>`; return; }
  $("auFeed").innerHTML = au.feed.map(b => {
    const lot = lotById(b.lotId);
    let st = "";
    if(b.status === "pending") st = `<div class="fsug">Додати до існуючого <b>«${esc(b.suggestName)}»</b>?
        <div class="fbtns"><button class="primary small" data-res="${esc(b.id)}" data-ok="1">Додати</button><button class="small" data-res="${esc(b.id)}" data-ok="0" title="Залишити окремим лотом">Ні</button></div></div>`;
    else if(b.status === "added") st = `<div class="fst">до лота <b>«${esc(lot ? lot.name : b.text)}»</b></div>`;
    else st = `<div class="fst">${b.status === "rejected" ? "окремий лот" : "новий лот"}</div>`;
    return `<div class="fbid ${b.status}${auFresh.has(b.id) ? " fresh" : ""}" style="--c:${lotColor(b.lotId || b.suggestId)}">
      <div class="frow">${userTag(b)}${b.donation ? donChip(b.uah) : ""}<span class="famt">${POINTS_ICON}${fmtN(b.amount)}</span><span class="ftime">${fmtHM(b.ts)}</span></div>
      <div class="ftext">${esc(b.text)}</div>${st}</div>`;
  }).join("");
}
$("auFeed").addEventListener("click", async e => {
  const b = e.target.closest("[data-res]"); if(!b) return;
  await withLoading(b.closest(".fbid"), async () => setState(await call("AuctionResolve", b.dataset.res, b.dataset.ok === "1")));
});

// ----- вікно 2: колесо -----
const wheelCv = $("wheel"), wctx = wheelCv.getContext("2d");
let wRot = 0, wAnim = null, lastSpinAt = null, wFade = null; // wFade — сектор, що вибуває (згасає)
let wFadeMap = null, wDrawn = null, wMorphTok = 0; // wFadeMap — прозорість секторів під час морфу; wDrawn — сектори, які зараз на колесі
function wheelDraw(secs){
  if(!S) return;
  if(!secs){ secs = wAnim ? wAnim.secs : wheelSectors(); if(!wAnim) wDrawn = secs; }
  const box = $("wheelBox"); if(!box.clientWidth) return;
  const size = Math.max(60, Math.floor(Math.min(box.clientWidth, box.clientHeight || box.clientWidth)));
  const dpr = devicePixelRatio || 1;
  if(wheelCv.width !== size * dpr){ wheelCv.width = wheelCv.height = size * dpr; wheelCv.style.width = wheelCv.style.height = size + "px"; }
  const c = wctx, R = size / 2 - 6, cx = size / 2, big = auSwapped;
  c.setTransform(dpr, 0, 0, dpr, 0, 0); c.clearRect(0, 0, size, size);
  // зовнішнє кільце — фірмовий фіолетовий
  const ring = "#9147ff";
  c.beginPath(); c.arc(cx, cx, R + 4, 0, Math.PI * 2); c.fillStyle = ring; c.fill();
  if(!secs.length){
    c.beginPath(); c.arc(cx, cx, R, 0, Math.PI * 2); c.fillStyle = "#1f1f23"; c.fill();
    if(size < 160) return; // у міні-колесі напис не вміщається (поруч уже є «Лотів поки немає»)
    c.fillStyle = "#adadb8"; c.font = `600 ${Math.max(12, size / 22)}px Inter, sans-serif`;
    c.textAlign = "center"; c.textBaseline = "middle"; c.fillText("Немає лотів", cx, cx); return;
  }
  const total = secs.reduce((a, s) => a + s.w, 0);
  let a0 = wRot - Math.PI / 2;
  for(const s of secs){
    const a1 = a0 + s.w / total * Math.PI * 2, lot = lotById(s.id), fade = wFade && wFade.id === s.id ? wFade.k : wFadeMap && wFadeMap.has(s.id) ? wFadeMap.get(s.id) : 1;
    c.beginPath(); c.moveTo(cx, cx); c.arc(cx, cx, R, a0, a1); c.closePath();
    c.globalAlpha = .25 + .75 * fade; c.fillStyle = lotColor(s.id); c.fill(); c.globalAlpha = 1;
    c.strokeStyle = "rgba(14,14,16,.55)"; c.lineWidth = secs.length > 60 ? .5 : 1.5; c.stroke();
    // підпис уздовж радіуса, якщо сектор достатньо широкий
    const arc = (a1 - a0) * R * .62, fs = big ? Math.min(18, Math.max(11, size / 40)) : Math.min(13, Math.max(10, size / 30));
    if(arc > fs * 1.15 && lot){
      const two = big && arc > fs * 2.6;
      // на лівій половині колеса текст розвертаємо, щоб не читався догори дриґом
      const mid = (a0 + a1) / 2, norm = ((mid % (Math.PI * 2)) + Math.PI * 2) % (Math.PI * 2), flip = norm > Math.PI / 2 && norm < Math.PI * 1.5;
      c.save(); c.translate(cx, cx); c.rotate(flip ? mid + Math.PI : mid);
      c.fillStyle = "#fff"; c.globalAlpha = fade; c.textAlign = flip ? "left" : "right"; c.textBaseline = "middle";
      c.shadowColor = "rgba(0,0,0,.6)"; c.shadowBlur = 3;
      c.font = `700 ${fs}px Inter, sans-serif`;
      const maxW = R * (big ? .62 : .7);
      let name = lot.name; while(name.length > 1 && c.measureText(name).width > maxW) name = name.slice(0, -2) + "…";
      const tx = flip ? -(R - 12) : R - 12;
      c.fillText(name, tx, two ? -fs * .45 : 0);
      if(two){ c.font = `600 ${fs * .78}px Inter, sans-serif`; c.globalAlpha = .85 * fade; c.fillText((s.w / total * 100).toFixed(1).replace(".0", "") + "%", tx, fs * .6); } // шанс лота
      c.restore();
    }
    a0 = a1;
  }
  // центр: з картинкою — більший круг, сама картинка (і GIF) — елемент <img> поверх колеса
  const hasImg = !!centerSrc(), hub = hasImg ? Math.max(24, R * (big ? .22 : .2)) : Math.max(14, R * .14);
  c.beginPath(); c.arc(cx, cx, hub, 0, Math.PI * 2); c.fillStyle = "#18181b"; c.fill();
  c.lineWidth = 3; c.strokeStyle = ring; c.stroke();
  placeCenter(hub);
}
new ResizeObserver(() => wheelDraw()).observe($("wheelBox"));
// картинка в центрі колеса: емодзі каналу (CDN Twitch) або власний файл (/userimg/wheel/…)
function centerSrc(c = (AU().cfg || {}).center){
  if(!c || !c.kind) return "";
  if(c.kind === "emote") return c.url || "";
  if(c.kind === "file" && c.name) return "/userimg/wheel/" + encodeURIComponent(c.name);
  return "";
}
function placeCenter(hub){
  const img = $("wCenter"), src = centerSrc(), c = (AU().cfg || {}).center || {};
  img.classList.toggle("hidden", !src);
  if(!src) return;
  if(img.getAttribute("src") !== src) img.src = src;
  const sz = Math.round((hub - 3) * 2);
  img.style.width = img.style.height = sz + "px";
  img.style.padding = c.kind === "emote" ? Math.round(sz * .12) + "px" : "0";
  img.classList.toggle("emote", c.kind === "emote");
}
const easeSpin = t => 1 - Math.pow(1 - t, 4);
let spinToken = 0, spinEndP = Promise.resolve(); // токен — щоб «Скинути колесо» зупиняло оберт; проміс — кінець анімації
function animateSpin(sp){
  const tok = ++spinToken; let endRes; spinEndP = new Promise(r => endRes = r);
  const secs = sp.ids.map((id, i) => ({id, w: sp.weights[i]}));
  const TAU = Math.PI * 2, tgt = -sp.pos * TAU; // точка pos на колі — під стрілкою зверху
  // «перчинка» — довший оберт, що наприкінці ледь повзе до межі сектора
  const spicy = !!sp.spicy, from = wRot, delta = ((tgt - from) % TAU + TAU) % TAU + TAU * (spicy ? 7 : 6), dur = spicy ? 10500 : 7000, t0 = performance.now();
  const ease = spicy ? (t => 1 - Math.pow(1 - t, 6)) : easeSpin;
  wAnim = {secs, sp}; wFade = null; $("wResult").className = "wresult"; renderSpinBtn();
  let finished = false;
  const done = () => {
    if(finished) return; finished = true;
    if(tok !== spinToken){ endRes(); return; } // оберт зупинено («Скинути колесо»)
    wRot = tgt;
    const lot = lotById(sp.pick);
    if(sp.mode === "instant" || sp.ids.length <= 1){ wAnim = null; wheelDraw(secs); showWinner(); endRes(); return; }
    // вибування: сектор тьмяніє і плавно стискається до нуля, решта секторів розходиться до нових розмірів
    $("wResult").innerHTML = `Вибуває: <b>${esc(lot ? lot.name : "?")}</b>`; $("wResult").className = "wresult show out";
    renderAuList(); // лот, що вибуває, виїжджає зі списку одночасно з «Вибуває: …»
    const sum = a => a.reduce((x, s) => x + s.w, 0), T0 = sum(secs);
    const next = wheelSectors(), T1 = sum(next) || 1, nw = Object.fromEntries(next.map(s => [s.id, s.w / T1]));
    // поворот: точка під стрілкою зберігається, колесо доїжджає до нульового кута найкоротшим шляхом
    const r0 = wRot, dr = (((-r0) % TAU) + TAU * 1.5) % TAU - TAU / 2;
    const HOLD = 350, DUR = 1100, f0 = performance.now() + HOLD;
    const ease = t => t < .5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
    wFade = {id: sp.pick, k: 1};
    const morph = now => {
      if(tok !== spinToken || !wFade) return;
      const t = Math.max(0, Math.min(1, (now - f0) / DUR)), e = ease(t);
      wFade.k = 1 - Math.min(1, t * 1.6); // спершу тьмяніє, потім зникає
      const cur = secs.map(s => ({id: s.id, w: (s.w / T0) * (1 - e) + (s.id === sp.pick ? 0 : (nw[s.id] || 0)) * e}));
      wRot = r0 + dr * e; wheelDraw(cur);
      if(t < 1) requestAnimationFrame(morph);
    };
    requestAnimationFrame(morph);
    setTimeout(() => {
      if(tok !== spinToken){ endRes(); return; }
      wAnim = null; wFade = null; wRot = 0; if(!sp.final) $("wResult").className = "wresult"; wheelDraw(); renderSpinBtn(); renderAuList(); if(sp.final) showWinner(); endRes();
    }, HOLD + DUR + 120);
  };
  const step = now => {
    if(finished) return;
    if(tok !== spinToken){ finished = true; endRes(); return; }
    const t = Math.min(1, (now - t0) / dur); wRot = from + delta * ease(t); wheelDraw(secs);
    if(t < 1) requestAnimationFrame(step); else done();
  };
  requestAnimationFrame(step);
  setTimeout(done, dur + 400); // гарантія, якщо кадри призупинено
}
async function showWinner(){
  renderSpinBtn(); renderAuList();
  const l = lotById(AU().winner); if(!l) return;
  $("wResult").innerHTML = `Переможець: <b>${esc(l.name)}</b>`; $("wResult").className = "wresult show win";
  if(mode === "viewing") await sparkles(1500);
}
// ----- міні-колесо: у правій колонці колесо згорнуте в смужку (крутять його у великому вікні), стрічка ставок займає висоту -----
const plural = (n, one, few, many) => { const d = n % 10, h = n % 100; return d === 1 && h !== 11 ? one : d >= 2 && d <= 4 && (h < 12 || h > 14) ? few : many; };
let wMiniOpen = false, wMiniWas = null; // wMiniOpen — стрімер розгорнув колесо вручну (запам'ятовується)
try{ wMiniOpen = localStorage.getItem("phillbot-wminiopen") === "1"; }catch(e){}
function renderMini(){
  const au = AU(), mini = !auSwapped && !wMiniOpen && !auRun;
  const wrap = $("auWrap");
  wrap.classList.toggle("bidding", !auSwapped); // «Згорнути» — для розгорнутого колеса в правій колонці
  if(mini !== wMiniWas){ wMiniWas = mini; wrap.classList.toggle("mini", mini); requestAnimationFrame(() => wheelDraw()); }
  if(!mini) return;
  const live = au.lots.filter(l => !l.out && !l.hidden && l.amount > 0), sum = live.reduce((a, l) => a + l.amount, 0);
  const top = live.slice().sort((a, b) => b.amount - a.amount)[0];
  $("wMiniText").innerHTML = top
    ? `<b>${esc(top.name)}</b> <span class="pct">${(top.amount / sum * 100).toFixed(1).replace(".0", "")}%</span><small>${live.length} ${plural(live.length, "лот", "лоти", "лотів")} · ${fmtN(sum)} балів</small>`
    : `<small>Лотів поки немає</small>`;
}
const setMiniOpen = v => { wMiniOpen = v; try{ localStorage.setItem("phillbot-wminiopen", v ? "1" : "0"); }catch(e){} renderMini(); };
$("wMiniExpand").onclick = () => setMiniOpen(true);
$("wMiniCollapse").onclick = () => setMiniOpen(false);
// зміна лотів на колесі — плавно: новий лот проявляється й розростається (дзеркально до вибування),
// решта сектори розходяться/стискаються до нових розмірів; прибрані — тьмяніють і стискаються
function morphWheel(){
  const next = wheelSectors(), prev = wDrawn;
  const same = prev && prev.length === next.length && prev.every((s, i) => s.id === next[i].id && Math.abs(s.w - next[i].w) < 1e-9);
  if(!prev || same || !$("wheelBox").clientWidth || RM.matches){ wheelDraw(); return; }
  const tok = ++wMorphTok, sum = a => a.reduce((x, s) => x + s.w, 0) || 1;
  const pf = new Map(prev.map(s => [s.id, s.w / sum(prev)])), nf = new Map(next.map(s => [s.id, s.w / sum(next)]));
  const order = next.map(s => s.id); // прибрані лишаються на своїх старих місцях
  prev.forEach((s, i) => { if(!nf.has(s.id)) order.splice(Math.min(i, order.length), 0, s.id); });
  const DUR = 650, t0 = performance.now(), ease = t => t < .5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
  const step = now => {
    if(tok !== wMorphTok || wAnim) return;
    const t = Math.min(1, (now - t0) / DUR), e = ease(t);
    wFadeMap = new Map();
    const cur = order.map(id => {
      const a = pf.get(id) || 0, b = nf.get(id) || 0;
      if(!pf.has(id)) wFadeMap.set(id, Math.min(1, t * 1.6)); // новий — проявляється
      if(!nf.has(id)) wFadeMap.set(id, 1 - Math.min(1, t * 1.6)); // прибраний — тьмяніє
      return {id, w: a * (1 - e) + b * e};
    }).filter(s => s.w > 1e-6);
    if(cur.length) wheelDraw(cur);
    if(t < 1) requestAnimationFrame(step);
    else { wFadeMap = null; wheelDraw(); }
  };
  requestAnimationFrame(step);
  setTimeout(() => { if(tok === wMorphTok && wFadeMap){ wFadeMap = null; wheelDraw(); } }, DUR + 400); // якщо кадри призупинено
}
function renderWheel(){
  const au = AU(), sp = au.spin;
  renderMini();
  $("auModeLbl").textContent = au.cfg.spinMode === "instant" ? "одразу результат" : "на вибування";
  if(sp && lastSpinAt !== null && sp.at !== lastSpinAt){ lastSpinAt = sp.at; if(Date.now() - sp.at < 15000){ animateSpin(sp); return; } }
  if(lastSpinAt === null) lastSpinAt = sp ? sp.at : 0;
  if(!sp) lastSpinAt = 0;
  if(wAnim) return;
  morphWheel();
  const w = lotById(au.winner);
  if(w){ $("wResult").innerHTML = `Переможець: <b>${esc(w.name)}</b>`; $("wResult").className = "wresult show win"; }
  else $("wResult").className = "wresult";
}
function renderSpinBtn(){
  if(!S) return;
  const au = AU(), t = au.timer.status, btn = $("auSpin"), act = au.lots.filter(l => !l.out && !l.hidden && l.amount > 0).length;
  if(holding && (holding.btn === btn || holding.btn === $("auReset"))) return;
  const why = auRun ? "Іде вибування…" : wAnim ? "Колесо крутиться…" : (t === "running" || t === "paused") ? "Спершу завершіть прийом ставок" : au.winner ? "Переможця визначено" : !act ? "На колесі немає лотів"
    : !auSwapped ? "Розгорніть колесо у велике вікно, щоб крутити" : "";
  // поки колесо крутиться — вікна не міняються місцями
  document.querySelectorAll("[data-swap]").forEach(s => { s.disabled = !!wAnim || !!auRun; s.title = wAnim || auRun ? "Колесо крутиться — дочекайтеся результату" : "Поміняти вікна місцями"; });
  $("auWrap").querySelector(".wctl").classList.toggle("auto", !!auRun); // під час вибування — лише «Скинути колесо»
  btn.disabled = !!why; btn.title = why || "Затисніть і тримайте, щоб крутити колесо";
  btn.querySelector(".wl").textContent = au.cfg.spinMode === "instant" || act <= 2 ? "Крутити" : "Крутити на вибування";
  $("auReset").disabled = auRun ? false : (!!wAnim || (!au.lots.some(l => l.out) && !au.winner));
}
$("auSpin").insertAdjacentHTML("afterbegin", LIQUID);
/* ---------- автооберти «на вибування» ----------
   крутить сам: після кожного вибування — пауза 1 с і наступний оберт */
const SPIN_GAP = 1000;
function setRunUI(on){
  $("auWrap").classList.toggle("autorun", on);
  renderAuList(); renderSpinBtn();
}
async function autoRun(){
  if(auRun) return;
  const run = auRun = {stop: false};
  setRunUI(true);
  try{
    while(!run.stop){
      setState(await call("AuctionSpin"));
      await spinEndP;
      if(run.stop || AU().winner) break;
      await new Promise(r => setTimeout(r, SPIN_GAP)); // коротка пауза між обертами, без напису
    }
  } catch(e){}
  finally { if(auRun === run){ auRun = null; setRunUI(false); } }
}
function stopAutoRun(){
  if(auRun){ auRun.stop = true; auRun = null; }
  spinToken++; wAnim = null; wFade = null; // зупинити оберт, що йде
  $("wResult").className = "wresult";
  setRunUI(false);
}
// «Скинути колесо» — затисканням, червона «фарба» (як «Завершити гру»)
$("auReset").insertAdjacentHTML("afterbegin", LIQUID);
$("auReset").querySelector(".liquid").insertAdjacentHTML("beforeend", `<span class="ink"><i></i><i></i><i></i></span>`);

// ----- обмін вікон місцями (FLIP-анімація): колесо стає великим і детальним, список — топ-3 -----
function placeWins(){
  const wrap = $("auWrap"), side = wrap.querySelector(".auside");
  if(auSwapped){ side.prepend($("auListWin")); wrap.prepend($("auWheelWin")); }
  else { side.prepend($("auWheelWin")); wrap.prepend($("auListWin")); }
  wrap.classList.toggle("swapped", auSwapped);
  if(S) renderMini(); // велике колесо ліворуч — міні-режим не потрібен
}
document.querySelectorAll("[data-swap]").forEach(b => b.onclick = () => {
  if(wAnim) return; // під час оберту колесо не мінімізується
  const wins = [$("auListWin"), $("auWheelWin")], before = wins.map(w => w.getBoundingClientRect());
  const feed = document.querySelector(".aufeedcard"), fb = feed.getBoundingClientRect(); // стрічка «Ставки» під верхнім вікном
  auSwapped = !auSwapped; try{ localStorage.setItem("phillbot-auswap", auSwapped ? "1" : "0"); }catch(e){}
  placeWins(); renderAuList(); wheelDraw(); renderSpinBtn();
  // Плавний обмін: лише transform і opacity (їх рахує відеокарта), жодних filter/box-shadow у кадрах.
  // Рух — один плавний перехід (FLIP від центру). «Глибина» — окрема м'яка хвиля масштабу (синус), без зламу посередині.
  const DUR = 640, EASE = "cubic-bezier(.22,.8,.24,1)";
  const wave = (peak, n = 8) => Array.from({length: n + 1}, (_, k) => 1 + (peak - 1) * Math.sin(Math.PI * k / n)); // 1 → peak → 1
  wins.forEach((w, i) => {
    const a = before[i], z = w.getBoundingClientRect();
    const grows = z.width * z.height > a.width * a.height; // це вікно стає великим — летить зверху й «наближається»
    const dx = (a.left + a.width / 2) - (z.left + z.width / 2), dy = (a.top + a.height / 2) - (z.top + z.height / 2);
    const sx = a.width / z.width, sy = a.height / z.height;
    w.style.position = "relative"; w.style.zIndex = grows ? 6 : 4; w.style.transformOrigin = "50% 50%"; w.style.willChange = "transform, opacity";
    w.classList.toggle("flying", grows); // статична тінь на час польоту (не анімується)
    const move = w.animate([{transform: `translate(${dx}px,${dy}px) scale(${sx},${sy})`}, {transform: "none"}], {duration: DUR, easing: EASE});
    // глибина: окрема властивість scale складається з transform; синус дає плавне наближення/віддалення
    w.animate(wave(grows ? 1.045 : .9).map(v => ({scale: String(v)})), {duration: DUR, easing: "linear"});
    if(!grows) w.animate(wave(.62).map(v => ({opacity: v})), {duration: DUR, easing: "linear"}); // віддалене — трохи тьмяніє
    const done = () => { w.style.zIndex = ""; w.style.willChange = ""; w.style.transformOrigin = ""; w.classList.remove("flying"); };
    move.onfinish = done; setTimeout(done, DUR + 200);
    // вміст поки летить розтягнутий — приглушений, проявляється ближче до кінця
    const inner = w.querySelector(".aulist, .wheelbox");
    if(inner) inner.animate([{opacity: .25}, {opacity: .25, offset: .35}, {opacity: 1}], {duration: DUR, easing: "ease-out"});
  });
  // «Ставки» переповзають на нове місце тим самим рухом
  const fa = feed.getBoundingClientRect(), fdy = fb.top - fa.top;
  if(Math.abs(fdy) > 1) feed.animate([{transform: `translateY(${fdy}px)`}, {transform: "none"}], {duration: DUR, easing: EASE});
});
placeWins();

// ----- нагороди-ставки (налаштування, як у СабДеї) -----
let auPending = null, auShowAll = false, auModePending = null, auCenterPending = null, emotes = null;
const curCenter = () => auCenterPending || (AU().cfg || {}).center || {kind: ""};
function auCenterUI(){
  const c = curCenter();
  document.querySelectorAll("#auCenterSeg .tab").forEach(b => b.classList.toggle("active", b.dataset.c === (c.kind || "")));
  slideSeg($("auCenterSeg"));
  const box = $("auCenterBox"), src = centerSrc(c);
  const prev = src ? `<span class="wcprev${c.kind === "emote" ? " emote" : ""}"><img src="${esc(src)}" alt=""></span>` : "";
  if(!c.kind) box.innerHTML = `<div class="hint">У центрі колеса — звичайний круг.</div>`;
  else if(c.kind === "file") box.innerHTML = `<div class="wcdrop" id="wcDrop">${prev}<div class="wcdrop-t"><b>Перетягніть файл сюди</b><span>або</span><button id="auPickImg">${c.name ? "Вибрати інший файл…" : "Вибрати файл…"}</button></div><span class="hint wcdrop-h">PNG, JPG, GIF або WEBP, до 15 МБ. GIF у центрі анімується.</span></div>`;
  else {
    if(!emotes){ box.innerHTML = `<div class="hint">Завантажую емодзі каналу…</div>`; return; }
    if(!emotes.length){ box.innerHTML = `<div class="hint">У каналу немає власних емодзі.</div>`; return; }
    box.innerHTML = `<div class="emgrid">` + emotes.map(e => `<button class="em${c.url === e.url ? " sel" : ""}" data-em="${esc(e.id)}" title="${esc(e.name)}${e.animated ? " (анімоване)" : ""}"><img src="${esc(e.url)}" alt="" loading="lazy"></button>`).join("") + `</div>`;
  }
}
async function loadEmotes(){
  if(emotes) return;
  try{ emotes = await withLoading($("auCenterBox"), () => api().GetChannelEmotes()) || []; }
  catch(e){ emotes = null; $("auCenterBox").innerHTML = `<div class="hint err">Не вдалося отримати емодзі: ${esc(e && e.message || e)}</div>`; return; }
  auCenterUI();
}
document.querySelectorAll("#auCenterSeg .tab").forEach(b => b.onclick = () => {
  const k = b.dataset.c, cur = (AU().cfg || {}).center || {};
  auCenterPending = k === (cur.kind || "") ? {...cur} : {kind: k};
  auCenterUI();
  if(k === "emote") loadEmotes();
});
// перетягування файлу картинки в «Своя картинка або GIF»
const IMG_RE = /\.(png|jpe?g|gif|webp)$/i;
$("auCenterBox").addEventListener("dragover", e => {
  const z = e.target.closest("#wcDrop"); if(!z) return;
  e.preventDefault(); e.dataTransfer.dropEffect = "copy"; z.classList.add("over");
});
$("auCenterBox").addEventListener("dragleave", e => { const z = e.target.closest("#wcDrop"); if(z && !z.contains(e.relatedTarget)) z.classList.remove("over"); });
$("auCenterBox").addEventListener("drop", async e => {
  const z = e.target.closest("#wcDrop"); if(!z) return;
  e.preventDefault(); z.classList.remove("over");
  const f = [...(e.dataTransfer.files || [])].find(x => IMG_RE.test(x.name)); if(!f) return;
  if(f.size > 15 * 1024 * 1024) return;
  const data = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(f); });
  const name = await withLoading(z, () => call("SaveWheelImage", f.name, data));
  if(name){ auCenterPending = {kind: "file", name}; auCenterUI(); }
});
// файл, кинутий повз зону, не відкривається у вікні програми
["dragover", "drop"].forEach(t => document.addEventListener(t, e => { if(e.dataTransfer && [...(e.dataTransfer.types || [])].includes("Files") && !e.defaultPrevented) e.preventDefault(); }));
$("auCenterBox").addEventListener("click", async e => {
  const em = e.target.closest("[data-em]");
  if(em){ const x = emotes.find(v => v.id === em.dataset.em); if(x){ auCenterPending = {kind: "emote", url: x.url, name: x.name}; auCenterUI(); } return; }
  if(e.target.closest("#auPickImg")){
    const name = await call("PickWheelImage");
    if(name){ auCenterPending = {kind: "file", name}; auCenterUI(); }
  }
});
function auRewardGrid(animate){
  const sel = auPending ?? AU().cfg.rewardIds ?? [];
  const all = [...(S.rewards || [])].sort((a, b) => a.cost - b.cost);
  sel.forEach(id => { if(!all.some(r => r.id === id)){ const r = rewardById(id); if(r) all.unshift(r); } });
  let html;
  if(auShowAll){
    html = all.filter(r => r.id !== S.settings.rewardId).map((r, i) => rewardTile(r, {button: true, sel: sel.includes(r.id), delay: animate ? i * 45 : null}).replace('class="rtile', `class="rtile${animate ? " appear" : ""}`)).join("");
    if(!all.length) html = `<div class="hint">Список нагород порожній. Натисніть «Оновити».</div>`;
  } else {
    html = sel.map(id => rewardTile(rewardById(id) || {id, title: id}, {button: true, sel: true})).join("")
      + `<button class="rtile rmore" id="auShowAll" title="Показати всі нагороди каналу"><div class="rimg"><span class="plus"></span></div><div class="rtitle">${sel.length ? "Змінити" : "Вибрати"}<br><span class="muted">${all.length} нагород</span></div></button>`;
  }
  $("auRewardGrid").innerHTML = html;
  $("auRwCount").textContent = `${sel.length} / 5`;
  $("auRewardsErr").textContent = S.rewardsErr || "";
}
$("auRewardGrid").addEventListener("click", e => {
  if(e.target.closest("#auShowAll")){ auShowAll = true; auRewardGrid(true); return; }
  const t = e.target.closest("[data-rid]"); if(!t) return;
  const sel = [...(auPending ?? AU().cfg.rewardIds ?? [])], id = t.dataset.rid, i = sel.indexOf(id);
  if(i >= 0) sel.splice(i, 1);
  else if(sel.length >= 5){ toast("Можна вибрати до 5 нагород"); t.classList.remove("denied"); void t.offsetWidth; t.classList.add("denied"); return; }
  else sel.push(id);
  auPending = sel; auRewardGrid();
});
$("auRewardRefresh").onclick = async () => {
  await withLoading([$("auctionModal").querySelector(".box"), $("auRewardRefresh")], () => call("RefreshRewards"));
  auRewardGrid(); toast("Список нагород оновлено");
};
function auModeSeg(){
  const m = auModePending || AU().cfg.spinMode || "elim";
  document.querySelectorAll("#auModeSeg .tab").forEach(b => b.classList.toggle("active", b.dataset.mode === m));
  slideSeg($("auModeSeg"));
  $("auModeHint").textContent = m === "instant" ? "Один оберт — і переможець відомий. Шанс лота пропорційний його сумі."
    : "Кожен оберт вибуває один лот, останній — переможець.";
}
document.querySelectorAll("#auModeSeg .tab").forEach(b => b.onclick = () => { auModePending = b.dataset.mode; auModeSeg(); });
$("auSettingsBtn").onclick = () => {
  auPending = null; auShowAll = false; auModePending = null; auCenterPending = null;
  $("auMin").value = AU().cfg.durationMin || 10; $("auDonPer1000").value = AU().cfg.donUahPer1000 || 10;
  auRewardGrid(); openModal("auctionModal"); requestAnimationFrame(() => { auModeSeg(); auCenterUI(); });
  if(((AU().cfg || {}).center || {}).kind === "emote") loadEmotes();
  if(!(S.rewards || []).length) withLoading([$("auctionModal").querySelector(".box"), $("auRewardRefresh")], () => call("RefreshRewards")).then(() => auRewardGrid());
};
$("auctionSave").onclick = async () => {
  let center = curCenter();
  if(center.kind === "emote" && !center.url) center = {kind: ""}; // вкладку відкрили, але емодзі не вибрали
  if(center.kind === "file" && !center.name) center = {kind: ""};
  const cfg = {...AU().cfg, center, rewardIds: auPending ?? AU().cfg.rewardIds, spinMode: auModePending || AU().cfg.spinMode,
    durationMin: Math.min(180, Math.max(1, Math.round(n("auMin", 10)))),
    donUahPer1000: Math.min(100000, Math.max(1, Math.round(n("auDonPer1000", 10))))};
  setState(await call("AuctionSaveSettings", cfg));
  const noText = cfg.rewardIds.map(rewardById).filter(r => r && r.is_user_input_required === false);
  closeModal("auctionModal"); auPending = null; auCenterPending = null; wheelDraw();
  toast(noText.length ? "Увага: «" + noText[0].title + "» не вимагає тексту — такі ставки ігноруються" : "Налаштування аукціону збережено", noText.length ? 4500 : 2600);
};
function renderAuWarn(){
  const none = !(AU().cfg.rewardIds || []).length;
  $("auRewardWarn").classList.toggle("hidden", !none);
  if(none) $("auRewardWarn").innerHTML = `Нагороди-ставки не вибрано — ставки не записуватимуться. <a href="#" id="pickAuReward">Вибрати</a>`;
}
document.addEventListener("click", e => { if(e.target.id === "pickAuReward"){ e.preventDefault(); $("auSettingsBtn").click(); } });

// ----- кнопки таймера, новий аукціон -----
$("auStart").onclick = async () => {
  const wasNew = AU().timer.status !== "paused";
  setState(await call("AuctionStart"));
  if(wasNew) toast("Ставки приймаються");
  if(!(AU().cfg.rewardIds || []).length) toast("Увага: нагороди-ставки не вибрано", 4000);
  else if(S.conn.status !== "on") toast("Увага: немає з'єднання з Twitch", 4000);
};
$("auPause").onclick = async () => setState(await call("AuctionPause"));
$("auStop").insertAdjacentHTML("afterbegin", LIQUID_RED); // «Завершити» прийом ставок — затисканням
$("auPlus").onclick = async () => setState(await call("AuctionAddTime", 60000));
$("auMinus").onclick = async () => setState(await call("AuctionAddTime", -60000));
// «Новий стрім» і «Новий аукціон» — затисканням з червоною «фарбою» (попередні зберігаються)
$("newStreamHold").insertAdjacentHTML("afterbegin", LIQUID_RED);
$("auNew").insertAdjacentHTML("afterbegin", LIQUID_RED);

function renderAuction(){
  if(auResetting) return;
  const au = AU();
  $("auLots").textContent = au.lots.filter(l => !l.out && !l.hidden).length;
  $("auBids").textContent = au.feed.length;
  $("auSum").textContent = fmtN(au.lots.reduce((a, l) => a + l.amount, 0));
  renderAuWarn(); renderAuUndo(); renderWheel(); if(!wAnim) renderAuList(); renderAuFeed(); renderAuTimer();
  if($("auctionModal").classList.contains("show")) auRewardGrid();
}
// нова ставка — підсвітка й рамка-індикатор навколо стрічки (як нові замовлення в СабДеї)
let auFlashT = 0;
function onAuctionBid(id){
  auFresh.add(id); setTimeout(() => auFresh.delete(id), 3000);
  renderAuFeed(); // стан із цією ставкою вже намальовано (подія приходить після нього) — перемалювати з підсвіткою
  if(mode !== "viewing") return;
  const f = document.querySelector(".aufeedcard"); loadOn(f); clearTimeout(auFlashT); auFlashT = setTimeout(() => loadOff(f), 1300);
}

/* ---------- старт ---------- */
function boot(){
  if(!window.go || !window.runtime){ setTimeout(boot, 50); return; }
  window.runtime.EventsOn("state", snap => setState(snap));
  window.runtime.EventsOn("redeem", d => { const id = d && d.id; if(!id) return; freshIds.add(id); setTimeout(()=>freshIds.delete(id), 3000); flashTable(); });
  window.runtime.EventsOn("chat", m => chatPending.push(m));
  window.runtime.EventsOn("chatdel", chatDelete);
  window.runtime.EventsOn("auctionbid", onAuctionBid);
  window.runtime.EventsOn("chatters", list => { chatters = list || []; renderChatters(); });
  api().GetChatters().then(list => { chatters = list || []; renderChatters(); });
  api().GetState().then(setState);
}
boot();
})();
