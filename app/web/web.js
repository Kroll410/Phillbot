/* PhillBot у браузері: замінює міст Wails (window.go / window.runtime) на HTTP і SSE.
   app.js не знає, де працює: він кличе window.go.main.App.<Метод>(…) і слухає window.runtime.EventsOn(…).
   Методи, що в браузері мають сенс лише локально (буфер обміну, відкрити посилання), виконуються тут. */
(function () {
  "use strict";
  const handlers = {};
  let es = null, obsURL = "";

  function listen(name) {
    if (!es) return;
    es.addEventListener(name, e => {
      let data = null;
      try { data = JSON.parse(e.data); } catch (_) { return; }
      if (name === "state" && data && data.obs) obsURL = data.obs.url || "";
      (handlers[name] || []).forEach(cb => { try { cb(data); } catch (err) { console.error(err); } });
    });
  }
  function connect() {
    es = new EventSource("api/events");
    Object.keys(handlers).forEach(listen);
    if (!handlers.state) { handlers.state = []; listen("state"); }
  }

  async function invoke(method, args) {
    const r = await fetch("api/call/" + encodeURIComponent(method), {
      method: "POST", credentials: "same-origin",
      headers: {"Content-Type": "application/json", "X-PhillBot": "1"},
      body: JSON.stringify(args),
    });
    const text = await r.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch (_) {}
    if (!r.ok) throw new Error((data && data.error) || text || r.statusText);
    return data;
  }

  const local = {
    CopyText: async s => { try { await navigator.clipboard.writeText(String(s)); return true; } catch (_) { return false; } },
    OpenURL: async u => { if (/^https:\/\//.test(u)) window.open(u, "_blank", "noopener"); },
    OpenOverlay: async p => { if (obsURL) window.open(obsURL + p, "_blank", "noopener"); },
    OpenFolder: async () => {},
    RevealFile: async () => {},
  };

  window.go = {main: {App: new Proxy({}, {get: (_, name) => local[name] || ((...args) => invoke(String(name), args))})}};
  window.runtime = {
    EventsOn(name, cb) {
      const fresh = !handlers[name];
      (handlers[name] = handlers[name] || []).push(cb);
      if (fresh) listen(name);
    },
    BrowserOpenURL: u => local.OpenURL(u),
  };
  document.documentElement.classList.add("web");
  connect();
})();
