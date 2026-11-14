/* Спільне для оверлеїв: параметри URL, підключення до SSE (/api/events), таймер із поправкою на годинник сервера. */
(function () {
  "use strict";
  const Q = new URLSearchParams(location.search);
  const param = (k, d) => (Q.has(k) ? Q.get(k) : d);
  const num = (k, d) => { const v = Number(param(k, d)); return Number.isFinite(v) ? v : d; };
  const flag = (k, d) => { const v = param(k, d ? "1" : "0"); return v === "1" || v === "true"; };

  document.documentElement.style.setProperty("--scale", num("scale", 1));
  if (Q.has("color")) document.documentElement.style.setProperty("--accent", "#" + param("color", "").replace(/[^0-9a-f]/gi, "").slice(0, 6));
  document.body.classList.add("theme-" + param("theme", "shadow"), "align-" + param("align", "left"));

  // корінь API: на десктопі «/», у вебверсії «/w/<секрет>/» — сторінки віджетів лежать у <корінь>overlay/…
  const base = location.pathname.replace(/overlay\/[^/]*$/, "");
  let offset = 0;
  function connect(h) {
    const es = new EventSource(base + "api/events" + (flag("window", false) ? "?window=1" : ""));
    es.addEventListener("state", e => { const s = JSON.parse(e.data); offset = s.serverNow - Date.now(); h.state && h.state(s); });
    for (const n of ["chat", "chatdel", "redeem", "closewin"]) es.addEventListener(n, e => h[n] && h[n](JSON.parse(e.data)));
    es.onerror = () => h.offline && h.offline();
    return es;
  }
  const serverNow = () => Date.now() + offset;
  const sync = s => { if (s && s.serverNow) offset = s.serverNow - Date.now(); }; // поправка годинника зі стану, отриманого не з потоку
  function remaining(s) { return s.session.status === "running" ? s.session.endsAt - serverNow() : s.session.remainingMs; }
  function fmtTime(ms) { ms = Math.max(0, ms); const t = Math.ceil(ms / 1000), m = Math.floor(t / 60), s = t % 60; return String(m).padStart(2, "0") + ":" + String(s).padStart(2, "0"); }

  window.OV = { Q, param, num, flag, connect, remaining, fmtTime, serverNow, base, sync };
})();
