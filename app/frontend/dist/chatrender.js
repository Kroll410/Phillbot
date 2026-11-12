/* Рендер повідомлення чату Twitch. Спільний для головного вікна і оверлею OBS (/overlay/chatrender.js).
   Текст вставляється лише як textContent — без innerHTML із сирого тексту (захист від XSS). */
(function () {
  "use strict";
  const PALETTE = ["#FF4040","#5A8CFF","#2FBF4A","#E0503A","#FF7F50","#9ACD32","#FF6A2A","#3FBF7F","#DAA520","#E07A2A","#5FB3B5","#1E90FF","#FF69B4","#A060FF","#00E07F"];

  function hexToRgb(h) {
    const m = /^#?([0-9a-f]{6})$/i.exec(h || "");
    if (!m) return null;
    const n = parseInt(m[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  }
  // темні кольори ніків погано видно на темному фоні — підтягуємо до світлішого
  function readable(hex) {
    const c = hexToRgb(hex);
    if (!c) return hex;
    const l = 0.299 * c[0] + 0.587 * c[1] + 0.114 * c[2];
    if (l >= 95) return hex;
    const k = (95 - l) / 255 + 0.35;
    return "rgb(" + c.map(v => Math.round(v + (255 - v) * k)).join(",") + ")";
  }
  function nameColor(login, color) {
    if (!color) {
      let h = 0;
      for (const ch of login || "") h = (h * 31 + ch.charCodeAt(0)) >>> 0;
      color = PALETTE[h % PALETTE.length];
    }
    return readable(color);
  }
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  const safeUrl = u => /^https:\/\//.test(u || "") ? u : "";

  function render(msg, opts) {
    opts = opts || {};
    const row = el("div", "cmsg" + (msg.order ? " order" : "") + (msg.reward ? " reward" : "") + (msg.mention ? " mention" : "") + (msg.highlight ? " hl" : ""));
    row.dataset.id = msg.id;
    row.dataset.user = msg.userId || "";
    if (opts.time) {
      const d = new Date(msg.ts);
      row.appendChild(el("span", "ctime", d.toLocaleTimeString("uk-UA", { hour: "2-digit", minute: "2-digit" })));
    }
    if (opts.badges !== false) {
      for (const b of msg.badges || []) {
        const u = safeUrl(b.url);
        if (!u) continue;
        const img = el("img", "cbadge");
        img.src = u; img.alt = ""; img.title = b.title || b.setId;
        row.appendChild(img);
      }
    }
    const name = el("span", "cname", msg.name || msg.login);
    name.style.color = nameColor(msg.login, msg.color);
    row.appendChild(name);
    row.appendChild(el("span", "csep", ": "));
    const body = el("span", "cbody");
    for (const f of msg.fragments || []) {
      if (f.type === "emote" && f.emoteId) {
        const img = el("img", "cemote");
        img.src = "https://static-cdn.jtvnw.net/emoticons/v2/" + encodeURIComponent(f.emoteId) + "/default/dark/2.0";
        img.alt = f.text; img.title = f.text;
        body.appendChild(img);
      } else if (f.type === "mention") {
        body.appendChild(el("span", "cmention", f.text));
      } else {
        body.appendChild(document.createTextNode(f.text));
      }
    }
    row.appendChild(body);
    if (msg.order && opts.orderTag !== false) row.appendChild(el("span", "ctag", "★ замовлення"));
    return row;
  }

  window.ChatRender = { render, nameColor };
})();
