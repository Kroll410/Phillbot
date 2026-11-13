/* PhillBot — публічне демо (/demo): бекенд у браузері, без Twitch і без сервера.
   Повторює поведінку Go-бекенда (збори, гра переможця, голосування, аукціон, чесне колесо «на вибування»)
   Дані вигадані, нічого не зберігається — після перезавантаження все спочатку. */
(function(){
  const now0 = Date.now();
  const SET = {rewardId:"r-subday", rewardTitle:"СабДей · Замовлення гри", durationMin:10, nonSubPoints:0, mult:{"1":1,"2":2,"3":3}, dupMode:"last",
    obsPort:17841, chat:{fontSize:14, hideTime:false, hideBadges:false}, gameMin:40, voteMin:3};
  const REWARDS = [
    {id:"r-subday", title:"СабДей · Замовлення гри", cost:500, is_enabled:true, is_user_input_required:true, background_color:"#1F69FF"},
    {id:"q", title:"Квиток", cost:1000, is_enabled:true, is_user_input_required:true, background_color:"#00A383"},
    {id:"s", title:"Середній квиток", cost:5000, is_enabled:true, is_user_input_required:true, background_color:"#7B2FF7"},
    {id:"m", title:"Мегаквиток", cost:10000, is_enabled:true, is_user_input_required:true, background_color:"#E0168A"},
    {id:"h", title:"Гідрація", cost:300, is_enabled:true, is_user_input_required:false, background_color:"#2D7FF9"},
  ];
  const COLORS = ["#1E90FF","#FF69B4","#00C853","#FFB300","#B388FF","#FF5722","#26C6DA","#C6FF00","#FF4081","#7C4DFF"];
  let idn = 0; const nid = p => (p || "id") + (++idn);
  const colorOf = name => COLORS[[...name].reduce((a, c) => a + c.charCodeAt(0), 0) % COLORS.length];

  function fresh(){
    return {settings: JSON.parse(JSON.stringify(SET)), session:{status:"idle", remainingMs:600000}, entries:[], seenRewards:{}, seenRedeems:[],
      stream:{id:"2026-11-12_19-00-00", startedAt: now0 - 50*60000}, past:[], collN:0, phase:"collect", game:{status:"idle"}, winner:"", exported:false, round:0,
      vote:{status:"idle", yes:0, no:0},
      auction:{cfg:{rewardIds:["q","s","m"], rewardsSet:false, durationMin:10, spinMode:"elim", spinGapSec:1, center:{kind:""}},
        timer:{status:"idle", remainingMs:600000}, lots:[], feed:[], winner:"", merges:[], spin:null},
      auth:{status:"ok", user:{id:"1", login:"demo", display_name:"Демо"}}, conn:{status:"on", text:"Підключено до Twitch"},
      rewards: REWARDS, rewardsErr:"", log:[], obs:{running:true, port:17841, url:"http://127.0.0.1:17841"}, chatWindow:false, subBadges:{}};
  }
  let S = fresh();
  const snap = () => JSON.parse(JSON.stringify(S));
  const ev = {};
  const emit = () => ev.state && ev.state(snap());
  const log = t => { S.log.push({ts: Date.now(), text: t}); if(S.log.length > 200) S.log.shift(); };

  /* ---------- СабДей ---------- */
  function archive(){
    if(S.collN && (S.entries.length || S.session.status !== "idle"))
      S.past.push({n:S.collN, startedAt:S.session.startedAt || Date.now(), finishedAt:S.session.finishedAt || Date.now(), settings:S.settings, entries:S.entries, winner:S.winner});
  }
  const T = {
    GetState: () => snap(), GetChat: () => [], GetChatters: () => [],
    ListStreams: () => [{id:S.stream.id, startedAt:S.stream.startedAt, collections:S.past.length + (S.entries.length ? 1 : 0), orders:S.past.reduce((a,c)=>a+c.entries.length,0)+S.entries.length, current:true},
      {id:"2026-11-08_18-30-00", startedAt: now0 - 4*86400000, collections:5, orders:61, current:false},
      {id:"2026-11-01_19-10-00", startedAt: now0 - 11*86400000, collections:4, orders:48, current:false}],
    GetStream: id => ({id, startedAt: now0 - 4*86400000, collections: []}),
    StartTimer: () => {
      const s = S.session;
      if(s.status === "paused"){ S.session = {...s, status:"running", endsAt: Date.now() + s.remainingMs}; }
      else if(s.status !== "running"){
        archive(); S.entries = []; S.collN++; S.winner = ""; S.exported = false; S.phase = "collect"; S.round = 0; S.vote = {status:"idle", yes:0, no:0}; S.game = {status:"idle"};
        S.session = {status:"running", startedAt: Date.now(), endsAt: Date.now() + 600000};
        log("Почато Збір " + S.collN);
      }
      return snap();
    },
    PauseTimer: () => { S.session = {...S.session, status:"paused", remainingMs: S.session.endsAt - Date.now()}; return snap(); },
    FinishTimer: () => { S.session = {...S.session, status:"finished", remainingMs:0, finishedAt: Date.now()}; log("Збір завершено"); return snap(); },
    AddTime: ms => { const s = S.session; if(s.status === "running") s.endsAt = Math.max(Date.now()+1000, s.endsAt + ms); else if(s.status === "paused") s.remainingMs = Math.max(1000, s.remainingMs + ms); return snap(); },
    UpdateEntry: e => { const i = S.entries.findIndex(x => x.id === e.id); if(i >= 0) S.entries[i] = {...S.entries[i], ...e}; return snap(); },
    RenameEntries: (ids, text) => { S.entries.forEach(e => { if(ids.includes(e.id)) e.text = text; }); return snap(); },
    DeleteEntry: id => { S.entries = S.entries.filter(e => e.id !== id); return snap(); },
    SelectReward: (id, title) => { S.settings.rewardId = id; S.settings.rewardTitle = title; return snap(); },
    SaveSettings: s => { S.settings = {...S.settings, ...s, durationMin:10}; return snap(); },
    RefreshRewards: () => null,
    ExportMarbles: () => { S.exported = true; setTimeout(emit, 0); return {copied:true, path:"marbles\\last\\subday_marbles.csv"}; },
    LaunchMarbles: () => true,
    SetWinner: name => { S.winner = name; S.phase = "play"; S.round = 1; S.game = {status:"running", startedAt: Date.now(), endsAt: Date.now() + S.settings.gameMin*60000}; log("Переможець: " + name); return snap(); },
    GamePause: () => { S.game = {...S.game, status:"paused", remainingMs: S.game.endsAt - Date.now()}; return snap(); },
    GameResume: () => { S.game = {...S.game, status:"running", endsAt: Date.now() + S.game.remainingMs}; return snap(); },
    GameAddTime: ms => { const g = S.game; if(g.status === "running") g.endsAt += ms; else if(g.status === "paused") g.remainingMs += ms; return snap(); },
    FinishGame: () => { S.game = {...S.game, status:"finished", remainingMs:0, finishedAt: Date.now()}; return snap(); },
    StartVote: () => { S.vote = {status:"running", startedAt: Date.now(), endsAt: Date.now() + S.settings.voteMin*60000, yes:0, no:0}; return snap(); },
    VoteAddTime: ms => { if(S.vote.status === "running") S.vote.endsAt += ms; return snap(); },
    FinishVote: () => { resolveVote(); return snap(); },
    ContinueGame: () => { S.round++; S.game = {status:"running", startedAt: Date.now(), endsAt: Date.now() + S.settings.gameMin*60000}; S.vote.status = "idle"; return snap(); },
    NextCollection: () => { endPlay(); return snap(); },
    NewStream: () => {
      if(S.session.status === "running" || S.session.status === "paused") S.session = {...S.session, status:"finished"};
      archive(); S.entries = []; S.past = []; S.collN = 0; S.phase = "collect"; S.winner = ""; S.exported = false; S.game = {status:"idle"}; S.vote = {status:"idle", yes:0, no:0};
      S.stream = {id: "2026-11-12_" + new Date().toTimeString().slice(0,8).replace(/:/g,"-"), startedAt: Date.now()}; S.session = {status:"idle", remainingMs:600000};
      return snap();
    },
    OpenFolder: () => null, OpenOverlay: () => null, CopyText: () => true, OpenChatWindow: () => null, OpenURL: () => null,
  };
  function endPlay(){ S.game = {...S.game, status:"finished"}; S.vote.status = S.vote.status === "running" ? "idle" : S.vote.status; S.phase = "collect"; }
  function resolveVote(){
    const v = S.vote; v.status = "finished"; v.finishedAt = Date.now();
    if(v.yes > v.no){ v.result = "continue"; T.ContinueGame(); }
    else if(v.no > v.yes){ v.result = "next"; endPlay(); }
    else v.result = "tie";
  }

  /* ---------- аукціон ---------- */
  const au = () => S.auction;
  const norm = t => String(t).toLowerCase().replace(/\s+/g, " ").trim().replace(/[.!?,;:…]+$/, "").trim();
  const lev = (a, b) => { const d = [...Array(b.length + 1).keys()]; for(let i = 1; i <= a.length; i++){ let p = d[0]; d[0] = i; for(let j = 1; j <= b.length; j++){ const t = d[j]; d[j] = Math.min(d[j] + 1, d[j-1] + 1, p + (a[i-1] === b[j-1] ? 0 : 1)); p = t; } } return d[b.length]; };
  const sim = (a, b) => { a = a.replace(/[ \-_.:'’]/g, ""); b = b.replace(/[ \-_.:'’]/g, ""); if(!a || !b) return 0; if(a === b) return 1;
    if(a.length >= 4 && b.length >= 4 && (a.includes(b) || b.includes(a))) return .85; return 1 - lev(a, b) / Math.max(a.length, b.length); };
  const addTo = (l, b) => { l.amount += b.amount; l.bids++; const u = l.users.find(x => x.login === b.login); if(u) u.amount += b.amount; else l.users.push({name:b.user, login:b.login, color:b.color, amount:b.amount}); };
  const newLot = b => { const l = {id: nid("lot"), name: b.text.trim(), key: norm(b.text), amount:0, bids:0, users:[]}; au().lots.push(l); addTo(l, b); return l.id; };
  // чесне правило «на вибування»: шанс вибути ∝ сумі решти лотів → шанс перемогти = сума / загальна сума
  const elimW = am => { const s = am.reduce((x, y) => x + y, 0); return am.length === 1 ? [1] : am.map(x => s - x); };
  Object.assign(T, {
    AuctionSaveSettings: c => { const a = au(); a.cfg = {...c, rewardsSet:true}; if(a.timer.status === "idle") a.timer.remainingMs = a.cfg.durationMin*60000; return snap(); },
    AuctionStart: () => { const a = au(), t = a.timer;
      a.timer = t.status === "paused" ? {...t, status:"running", endsAt: Date.now() + t.remainingMs} : t.status === "running" ? t : {status:"running", startedAt: Date.now(), endsAt: Date.now() + a.cfg.durationMin*60000};
      return snap(); },
    AuctionPause: () => { const a = au(); if(a.timer.status === "running") a.timer = {...a.timer, status:"paused", remainingMs: a.timer.endsAt - Date.now()}; return snap(); },
    AuctionFinish: () => { au().timer = {status:"finished", remainingMs:0, finishedAt: Date.now()}; return snap(); },
    AuctionAddTime: ms => { const t = au().timer; if(t.status === "running") t.endsAt = Math.max(Date.now()+1000, t.endsAt + ms); else if(t.status === "paused") t.remainingMs = Math.max(1000, t.remainingMs + ms); return snap(); },
    AuctionResolve: (id, ok) => { const a = au(), b = a.feed.find(x => x.id === id); if(!b || b.status !== "pending") return snap();
      const l = a.lots.find(x => x.id === b.suggestId);
      if(ok && l){ addTo(l, b); b.status = "added"; b.lotId = l.id; } else { b.status = "rejected"; b.lotId = newLot(b); } return snap(); },
    AuctionMergeLots: (from, to) => { const a = au(); if(a.winner) throw new Error("переможця вже визначено");
      const i = a.lots.findIndex(l => l.id === from), dst = a.lots.find(l => l.id === to); if(i < 0 || !dst) return snap();
      const src = JSON.parse(JSON.stringify(a.lots[i])); src.users.forEach(u => addTo(dst, {user:u.name, login:u.login, color:u.color, amount:u.amount}));
      dst.bids += src.bids - src.users.length; a.lots.splice(i, 1); a.merges.push({src, index:i, to});
      a.feed.forEach(b => { if(b.lotId === from) b.lotId = to; if(b.suggestId === from){ b.suggestId = to; b.suggestName = dst.name; } }); return snap(); },
    AuctionUndoMerge: () => { const a = au(), m = a.merges.pop(); if(!m) throw new Error("немає об'єднань"); const t = a.lots.find(l => l.id === m.to);
      if(t){ t.amount -= m.src.amount; t.bids -= m.src.bids; m.src.users.forEach(u => { const x = t.users.find(y => y.login === u.login); if(x){ x.amount -= u.amount; if(x.amount <= 0) t.users.splice(t.users.indexOf(x), 1); } }); }
      a.lots.splice(Math.min(m.index, a.lots.length), 0, m.src); a.feed.forEach(b => { if(b.lotId === m.to && norm(b.text) === m.src.key) b.lotId = m.src.id; }); return snap(); },
    AuctionUpdateLot: (id, name, amount) => { const l = au().lots.find(x => x.id === id); if(l){ if(name){ l.name = name; l.key = norm(name); } if(amount >= 0) l.amount = amount; } return snap(); },
    AuctionToggleLot: id => { const l = au().lots.find(x => x.id === id); if(l) l.hidden = !l.hidden; return snap(); },
    AuctionRemoveLot: id => { au().lots = au().lots.filter(x => x.id !== id); return snap(); },
    AuctionSpin: () => { const a = au(); if(a.winner) throw new Error("переможця вже визначено");
      const act = a.lots.filter(l => !l.out && !l.hidden && l.amount > 0), ids = act.map(l => l.id);
      if(!ids.length) throw new Error("на колесі немає лотів");
      let w = act.map(l => l.amount); if(a.cfg.spinMode === "elim") w = elimW(w);
      const tot = w.reduce((x, y) => x + y, 0); let pos = Math.random(), r = pos * tot, k = 0;
      for(; k < w.length - 1; k++){ if(r < w[k]) break; r -= w[k]; }
      const pick = ids[k], sp = {at: Date.now(), mode: a.cfg.spinMode, ids, weights: w, pick, pos, source: "random.org", final:false};
      if(a.cfg.spinMode === "instant" || ids.length <= 1){ a.winner = pick; sp.final = true; }
      else { const rd = Math.max(0, ...a.lots.map(l => l.out || 0)) + 1; a.lots.find(l => l.id === pick).out = rd; if(ids.length === 2){ a.winner = ids.find(i => i !== pick); sp.final = true; } }
      a.spin = sp; return snap(); },
    AuctionResetSpins: () => { const a = au(); a.lots.forEach(l => delete l.out); a.winner = ""; a.spin = null; return snap(); },
    AuctionNew: () => { const a = au(); Object.assign(a, {lots:[], feed:[], winner:"", spin:null, merges:[], timer:{status:"idle", remainingMs: a.cfg.durationMin*60000}}); return snap(); },
    GetChannelEmotes: () => [], PickWheelImage: () => "", SaveWheelImage: () => "",
  });

  /* ---------- тікер (як ticker у Go): таймери, що вийшли ---------- */
  setInterval(() => {
    const t = Date.now(); let ch = false;
    if(S.session.status === "running" && S.session.endsAt <= t){ S.session = {...S.session, status:"finished", remainingMs:0, finishedAt:t}; ch = true; }
    if(S.phase === "play" && S.game.status === "running" && S.game.endsAt <= t){ S.game = {...S.game, status:"finished", remainingMs:0, finishedAt:t}; ch = true; }
    if(S.vote.status === "running" && S.vote.endsAt <= t){ resolveVote(); ch = true; }
    const at = S.auction.timer; if(at.status === "running" && at.endsAt <= t){ S.auction.timer = {status:"finished", remainingMs:0, finishedAt:t}; ch = true; }
    if(ch) emit();
  }, 300);

  /* ---------- Wails-місток ---------- */
  window.runtime = {EventsOn(name, fn){ ev[name] = fn; }, EventsOff(){}};
  window.go = {main:{App: new Proxy({}, {get: (_, k) => async (...args) => {
    const fn = T[k]; if(!fn) return snap();
    const res = fn(...args);
    if(k !== "GetState" && k !== "GetChat" && k !== "GetChatters" && k !== "ListStreams" && k !== "GetStream" && k !== "GetChannelEmotes") setTimeout(emit, 0); // як mutate() у Go: стан іде подією
    return res;
  }})}};

  // імітація подій Twitch
  function order(user, text, tier, months){
    if(S.session.status !== "running") return;
    const e = {id: nid("e"), ts: Date.now(), user, login: user.toLowerCase(), userId: "u-" + user, tier, months: months || (tier ? 3 : 0), text, color: colorOf(user), source: "demo"};
    S.entries.push(e); emit(); ev.redeem && ev.redeem({id: e.id, order:{user, text}});
  }
  function bid(user, text, amount){
    const a = au(); if(a.timer.status !== "running") return;
    const b = {id: nid("b"), ts: Date.now(), user, login: user.toLowerCase(), userId: "u-" + user, color: colorOf(user), text, amount, status: ""}, key = norm(text);
    const same = a.lots.find(l => l.key === key);
    if(same){ addTo(same, b); b.status = "added"; b.lotId = same.id; }
    else { let best = null, bs = 0; a.lots.forEach(l => { const s = sim(key, l.key); if(s > bs){ bs = s; best = l; } });
      if(best && bs >= .72){ b.status = "pending"; b.suggestId = best.id; b.suggestName = best.name; }
      else { b.status = "new"; b.lotId = newLot(b); } }
    a.feed.unshift(b); emit(); ev.auctionbid && ev.auctionbid(b.id);
  }
  function vote(yes, no){ S.vote.yes = yes; S.vote.no = no; emit(); }
  function set(fn){ fn(S); emit(); }
  function reset(fnSeed){ S = fresh(); if(fnSeed) fnSeed(S); emit(); }

  window.DEMO = {get S(){ return S; }, emit, order, bid, vote, set, reset};
})();
