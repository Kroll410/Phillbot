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
    if (r.status === 401) { location.reload(); throw new Error("потрібно увійти"); } // сесія скінчилась — сторінка входу
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
    // вихід у вебверсії — кінець сесії в браузері; канал і далі працює (таймери, ставки)
    Logout: async () => {
      await fetch("auth/logout", {method: "POST", headers: {"X-PhillBot": "1"}, credentials: "same-origin"}).catch(() => {});
      location.href = "/";
    },
    Login: async () => { location.href = "/auth/login"; },
    // лише для десктопу: Client ID, окреме вікно чату (у вебверсії їх не показуємо)
    CancelLogin: async () => {}, ChangeClientID: async () => {}, OpenChatWindow: async () => {}, CloseChatWindow: async () => {},
    PickWheelImage: async () => { throw new Error("Своя картинка в центрі колеса поки є лише в програмі для Windows"); },
    SaveWheelImage: async () => { throw new Error("Своя картинка в центрі колеса поки є лише в програмі для Windows"); },
    // Marbles: файл завантажується браузером (у грі його вибирають з «Завантажень»), гра — через steam://
    ExportMarbles: async () => {
      const r = await fetch("api/marbles", {credentials: "same-origin"});
      if (r.status === 401) { location.reload(); throw new Error("потрібно увійти"); }
      if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error || m; } catch (_) {} throw new Error(m); }
      const url = URL.createObjectURL(await r.blob());
      const a = document.createElement("a");
      a.href = url; a.download = "subday_marbles.csv";
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
      return {lines: Number(r.headers.get("X-Lines")) || 0, copied: false, path: "subday_marbles.csv"};
    },
    LaunchMarbles: async () => {
      const a = document.createElement("a");
      a.href = "steam://rungameid/1170970";
      document.body.appendChild(a); a.click(); a.remove();
      return true;
    },
    // експорт за весь час — звичайне завантаження файлу браузером
    ExportAll: async kind => {
      const a = document.createElement("a");
      a.href = "api/export/" + encodeURIComponent(kind); a.download = "";
      document.body.appendChild(a); a.click(); a.remove();
      return "";
    },
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

  // загальні налаштування: «Дані каналу» (видалити все — затисканням, червоним) і документи
  window.webHold = async (action, btn) => {
    if (action !== "deletechannel") return;
    const r = await fetch("api/account/delete", {method: "POST", headers: {"X-PhillBot": "1"}, credentials: "same-origin"}).catch(() => null);
    if (r && r.ok) { location.href = "/"; return; }
    const hint = document.getElementById("delChannelHint");
    if (hint) { hint.textContent = "Не вдалося видалити дані — спробуйте ще раз."; hint.classList.add("err"); }
  };
  const addDataBox = () => {
    const box = document.querySelector("#generalModal .box.settings");
    const ink = document.querySelector("#logoutBtn .liquid"); // та сама «червона фарба», що в кнопці «Вийти»
    if (!box || !ink) { setTimeout(addDataBox, 300); return; }
    if (document.getElementById("webDataBox")) return;
    const d = document.createElement("div");
    d.id = "webDataBox";
    d.innerHTML = `<h3>Дані каналу</h3>
      <div class="hint" id="delChannelHint">Видалити всю історію, налаштування й вхід цього каналу з PhillBot. Скасувати не можна — спершу можна зробити «Усе .zip».</div>
      <div class="row"><button class="winbtn holdend" data-hold="web:deletechannel" title="Затисніть і тримайте">${ink.outerHTML}<span class="wl">Видалити дані каналу</span></button></div>
      <div class="hint legallinks"><a href="/privacy" target="_blank" rel="noopener">Політика конфіденційності</a> · <a href="/terms" target="_blank" rel="noopener">Умови користування</a></div>`;
    const actions = box.querySelector(".row.actions");
    box.insertBefore(d, actions);
  };
  addEventListener("load", addDataBox);

  // адміністратору — посилання на сторінку «Доступ» у шапці
  fetch("admin/api/users", {credentials: "same-origin"}).then(r => {
    if (!r.ok) return;
    const add = () => {
      const box = document.getElementById("userBox");
      if (!box) { setTimeout(add, 300); return; }
      if (document.getElementById("adminLink")) return;
      const a = document.createElement("a");
      a.id = "adminLink"; a.href = "/admin"; a.textContent = "Доступ"; a.className = "adminlink";
      box.parentNode.insertBefore(a, box);
    };
    add();
  }).catch(() => {});
})();
