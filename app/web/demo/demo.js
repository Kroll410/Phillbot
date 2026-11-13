// Публічне демо: початковий стан (?scene=subday|game|auction|wheel|settings|subdaysettings|ausettings|streams; &embed=1 — без позначки, для /demo/all)
(function(){
  const q = new URLSearchParams(location.search), scene = q.get("scene") || "subday", embed = q.get("embed") === "1";
  const VIEW = ["auction", "wheel", "ausettings"]; // сцени вкладки «Переглядовий»
  try{
    localStorage.setItem("phillbot-mode", VIEW.includes(scene) ? "viewing" : "subday");
    localStorage.setItem("phillbot-auswap", scene === "wheel" ? "1" : "0");
  }catch(e){}
  addEventListener("DOMContentLoaded", () => document.body.classList.add("nowelcome"));
  // вигляд як у вебверсії (без суто десктопних налаштувань) і позначка, що це демо
  document.documentElement.classList.add("web");
  addEventListener("DOMContentLoaded", () => {
    const st = document.createElement("style");
    st.textContent = `#demoBadge{display:flex;gap:8px;align-items:center;font-size:12.5px;color:var(--muted);margin-right:6px;padding-right:12px;border-right:1px solid var(--line)}
      #demoBadge b{color:#ffd27a;font-weight:600}`;
    if(embed) return; // у «палітрі» підпис уже є над кадром
    const b = document.createElement("span"); b.id = "demoBadge"; b.setAttribute("role", "note");
    b.innerHTML = '<b>Демо</b><span>дані вигадані, нічого не зберігається</span><a href="/">Увійти</a>';
    document.head.appendChild(st); document.querySelector("header .conn").prepend(b);
  });
  // дії, що в демо нікуди не ведуть
  const App = window.go.main.App, noop = new Set(["ExportAll", "OpenFolder", "RevealFile", "OpenOverlay", "CopyText", "PickWheelImage", "SaveWheelImage", "Login", "ChangeClientID"]);
  window.go.main.App = new Proxy({}, {get: (_, k) => k === "Logout" ? async () => { location.href = "/"; } : noop.has(k) ? async () => "" : App[k]});
  const now = Date.now(), M = 60000;
  const C = ["#1E90FF","#FF69B4","#00C853","#FFB300","#B388FF","#FF5722","#26C6DA","#C6FF00"];
  const E = (i, user, text, tier, months, extra) => Object.assign({id: "e" + i, ts: now - (40 - i) * 9000, user, login: user.toLowerCase(), userId: "u" + user,
    tier, months, text, color: C[i % C.length]}, extra || {});
  const past = n => ({n, startedAt: now - (4 - n) * 50 * M, finishedAt: now - (4 - n) * 50 * M + 10 * M, settings: null,
    entries: [E(n, "Viewer" + n, "Hades", 1, 3)], winner: ["Celeste", "Hades", "Outer Wilds"][n - 1]});
  const orders = () => [
    E(1, "Mira", "Hollow Knight", 3, 12, {gifts: 3}), E(2, "Taras", "Hollow Knight", 1, 8), E(3, "Olya", "Celeste", 2, 5),
    E(4, "Bohdan", "Disco Elysium", 1, 14), E(5, "Ira", "celeste", 1, 3), E(6, "Max", "Outer Wilds", 0, 0, {gifts: 2}),
    E(7, "Sasha", "Hades II", 2, 9), E(8, "Lev", "Disco Elysium", 3, 4),
    {id: "d1", ts: now - 30000, user: "Olena K.", login: "donatello", userId: "don:1", text: "Hades II", donation: true, donPoints: 4, uah: 120, source: "Donatello"}];
  const L = (id, name, amount, users, extra) => Object.assign({id, name, key: name.toLowerCase(), amount, bids: users.length,
    users: users.map(([n, a], k) => ({name: n, login: n.toLowerCase(), color: C[(id.length + k) % C.length], amount: a}))}, extra || {});
  const lots = () => [
    L("l1", "Elden Ring", 26000, [["Mira", 10000], ["Zo", 11000], ["Kat", 5000]]),
    L("l2", "Hades II", 15000, [["Taras", 10000], ["Bob", 5000]]),
    L("l3", "Disco Elysium", 11000, [["Lev", 11000]]),
    L("l4", "Celeste", 6000, [["Olya", 6000]]),
    L("l5", "Outer Wilds", 3000, [["Ann", 3000]]),
    L("l6", "Hollow Knight: Silksong", 2000, [["Max", 2000]])];
  const feed = () => [
    {id: "f1", ts: now - 5000, user: "Sasha", login: "sasha", color: "#FF5722", text: "Elden Rng", amount: 5000, status: "pending", suggestId: "l1", suggestName: "Elden Ring"},
    {id: "f2", ts: now - 20000, user: "Ira", login: "donatello:ira", color: "#26C6DA", text: "Hades II", amount: 10000, status: "added", lotId: "l2", donation: true, uah: 100},
    {id: "f3", ts: now - 40000, user: "Max", login: "max", color: "#C6FF00", text: "Hollow Knight: Silksong", amount: 1000, status: "new", lotId: "l6"},
    {id: "f4", ts: now - 60000, user: "Zo", login: "zo", color: "#FF69B4", text: "elden ring", amount: 10000, status: "added", lotId: "l1"}];

  addEventListener("load", () => setTimeout(() => {
    const D = window.DEMO;
    D.set(S => {
      S.auth.user = {id: "1", login: "demo", display_name: "Демо"};
      S.stream = {id: "2026-11-13_19-00-00", startedAt: now - 3 * 50 * M};
      if(!VIEW.includes(scene)){
        S.past = [past(1), past(2)]; S.collN = 3; S.entries = orders();
        S.session = scene === "subday" ? {status: "running", startedAt: now - 4 * M, endsAt: now + 6 * M + 12000} : {status: "finished", remainingMs: 0, startedAt: now - 14 * M, finishedAt: now - 4 * M};
      }
      if(scene === "game"){
        S.exported = true; S.winner = "Hollow Knight"; S.phase = "play"; S.round = 1;
        S.game = {status: "finished", remainingMs: 0, startedAt: now - 40 * M, finishedAt: now - 30000};
        S.vote = {status: "running", startedAt: now - 70000, endsAt: now + 110000, yes: 14, no: 9};
      }
      if(VIEW.includes(scene)){
        S.auction.lots = lots(); S.auction.feed = feed();
        S.auction.timer = scene === "auction" ? {status: "running", startedAt: now - 3 * M, endsAt: now + 7 * M + 5000} : {status: "finished", remainingMs: 0};
      }
      if(scene === "wheel"){
        S.auction.feed = S.auction.feed.slice(1);
        S.auction.lots.forEach((l, i) => { if(i >= 3) l.out = 6 - i; });
        S.auction.winner = "l1";
      }
      if(scene === "settings" || scene === "ausettings" || scene === "subdaysettings"){
        S.settings.ngrokToken = "x"; S.settings.donKey = "x";
        S.don = {state: "on", url: "https://your-name.ngrok-free.dev/donatello"};
      }
    });
    const open = {settings: "generalBtn", subdaysettings: "subdaySettingsBtn", ausettings: "auSettingsBtn", streams: "openJournal"}[scene];
    if(open) setTimeout(() => document.getElementById(open).click(), 300);
  }, 700));
})();
