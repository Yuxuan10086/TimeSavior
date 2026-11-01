package ui

import (
	"net/http"
)

// WriteLLMLogsPage serves the recent LLM call dialog list.
func WriteLLMLogsPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(llmLogsHTML))
}

var llmLogsHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TimeSavior · 大模型日志</title>
` + fontLinks + `
<style>
` + SharedCSS + `
.empty {
  color:var(--muted); font-size:.92rem; margin:0;
}
.dialog {
  border-top:1px solid var(--line); padding:1rem 0 .15rem;
}
.dialog:first-child { border-top:none; padding-top:.15rem; }
.dialog-head {
  display:flex; flex-wrap:wrap; gap:.35rem .75rem; align-items:baseline;
  margin-bottom:.45rem;
}
.dialog-head .when {
  font-family:var(--mono); font-size:.78rem; color:var(--muted);
}
.dialog-head .site { font-weight:600; }
.kind-tag {
  font-family:var(--mono); font-size:.7rem; font-weight:600;
  letter-spacing:.03em; color:var(--accent);
  border:1px solid var(--accent-border); background:var(--accent-dim);
  border-radius:4px; padding:.1rem .4rem;
}
.verdict {
  font-family:var(--mono); font-size:.72rem; font-weight:600;
  letter-spacing:.04em; text-transform:uppercase;
}
.verdict.allow { color:var(--ok); }
.verdict.block { color:var(--bad); }
.cost {
  font-family:var(--mono); font-size:.78rem; color:var(--muted);
  margin-left:auto;
}
.reason { margin:0 0 .55rem; font-size:.95rem; }
.videos {
  margin:0 0 .65rem; padding:0; list-style:none;
  font-size:.84rem; color:var(--muted);
}
.videos li {
  padding:.2rem 0; border-bottom:1px dashed rgba(226,219,207,.7);
}
.videos li:last-child { border-bottom:none; }
.videos .t { color:var(--fg); }
.fold {
  margin:.35rem 0 0; border:none; padding:0;
}
.fold summary {
  cursor:pointer; color:var(--muted); font-size:.78rem; font-family:var(--mono);
  letter-spacing:.03em; list-style:none;
}
.fold summary::-webkit-details-marker { display:none; }
.fold[open] summary { margin-bottom:.4rem; color:var(--fg); }
.fold pre {
  margin:0; white-space:pre-wrap; word-break:break-word;
  font-family:var(--mono); font-size:.75rem; line-height:1.5;
  color:var(--muted); background:var(--elevated);
  border:1px solid var(--line); border-radius:8px; padding:.65rem .75rem;
  max-height:28rem; overflow:auto;
}
.fold pre.expanded { max-height:none; }
.fold .expand-btn {
  display:inline-block; margin-top:.35rem; padding:0; border:none;
  background:none; cursor:pointer; color:var(--accent);
  font-family:var(--mono); font-size:.72rem; letter-spacing:.03em;
}
.keep {
  color:var(--muted); font-size:.78rem; font-family:var(--mono);
  margin:0 0 1rem;
}
</style>
</head>
<body>
<div class="shell">
  <header class="topbar">
    <div>
      <p class="brand-mark">TimeSavior</p>
      <h1>大模型日志</h1>
    </div>
    <div class="live" title="事件推送"><span class="dot" aria-hidden="true"></span><span id="live">连接中</span></div>
  </header>
` + shellNav("llm-logs") + `
  <p class="keep" id="keep"></p>
  <section class="panel accent-edge">
    <h2>最近调用</h2>
    <div id="list"></div>
    <div class="empty" id="empty" hidden>暂无对话</div>
  </section>
  <p class="err" id="err"></p>
</div>
<script>
` + liveSSEJS + `
(function () {
  const $ = (id) => document.getElementById(id);
  let lastSig = "";

  function esc(s) {
    return String(s||"").replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  }
  function siteLabel(s) {
    return ({youtube:"YouTube", bilibili:"B站", douyin:"抖音", zhihu:"知乎", title_short:"标题短名"}[s] || s || "—");
  }
  function kindLabel(k) {
    return ({watch_judge:"观看判定", title_short:"标题短名"}[k] || k || "观看判定");
  }
  function fmtWhen(ts) {
    if (!ts) return "—";
    const d = new Date(ts);
    if (isNaN(d.getTime())) return esc(ts);
    return d.toLocaleString("zh-CN", { hour12:false });
  }
  function fmtYen(v) {
    const n = Number(v);
    if (!isFinite(n) || n <= 0) return "";
    if (n < 0.0001) return "¥" + n.toExponential(2);
    return "¥" + n.toFixed(Math.min(6, Math.max(2, (n+"").split(".")[1]?.length||2)));
  }
  function parseVideos(raw) {
    try {
      const v = typeof raw === "string" ? JSON.parse(raw || "[]") : (raw || []);
      return Array.isArray(v) ? v : [];
    } catch (_) { return []; }
  }
  function videosHTML(raw) {
    const list = parseVideos(raw).slice(0, 8);
    if (!list.length) return "";
    return "<ul class='videos'>" + list.map((v) => {
      const title = esc(v.title || "—");
      const rawAuthor = String(v.author||"").replace(/[\s·\-–—_|]*(白名单中?|whitelist)\s*$/i, "").trim();
      const author = rawAuthor ? " · " + esc(rawAuthor) : "";
      const dur = (v.duration_sec != null) ? " · " + Math.round(Number(v.duration_sec)||0) + "s" : "";
      return "<li><span class='t'>" + title + "</span>" + author + dur + "</li>";
    }).join("") + "</ul>";
  }
  function itemHTML(d) {
    const kind = d.kind || "watch_judge";
    const isShort = kind === "title_short";
    const allow = d.allow === true;
    const block = d.allow === false;
    let verdict = "";
    let vcls = "verdict";
    if (!isShort) {
      verdict = "—";
      if (allow) { verdict = "放行"; vcls += " allow"; }
      else if (block) { verdict = "拦截"; vcls += " block"; }
    }
    const cost = fmtYen(d.cost_cny);
    const reason = esc(d.reason || (d.error ? ("出错：" + d.error) : "—"));
    const inputRaw = d.request_raw || d.prompt_summary || "";
    const outputRaw = d.response_raw || "";
    let folds = "";
    if (inputRaw) folds += textFold("输入原文", inputRaw);
    if (outputRaw) folds += textFold("输出原文", outputRaw);
    return "<article class='dialog'>" +
      "<div class='dialog-head'>" +
        "<span class='when'>" + fmtWhen(d.ts) + "</span>" +
        "<span class='kind-tag'>" + esc(kindLabel(kind)) + "</span>" +
        (isShort ? "" : "<span class='site'>" + esc(siteLabel(d.site)) + "</span>") +
        (verdict ? "<span class='" + vcls + "'>" + verdict + "</span>" : "") +
        (cost ? "<span class='cost'>" + cost + "</span>" : "") +
      "</div>" +
      "<p class='reason'>" + reason + "</p>" +
      (isShort ? "" : videosHTML(d.videos_json)) +
      folds +
    "</article>";
  }
  function textFold(label, raw) {
    const long = String(raw).length > 1200;
    const btn = long
      ? "<button type='button' class='expand-btn' data-expand='1'>展开全部</button>"
      : "";
    return "<details class='fold' open><summary>" + esc(label) + "</summary>" +
      "<pre>" + esc(raw) + "</pre>" + btn + "</details>";
  }
  function signature(payload) {
    const items = payload.items || [];
    return JSON.stringify(items.map(x => [x.id, x.ts, x.kind, x.allow, x.reason, x.cost_cny]));
  }
  async function load() {
    const err = $("err");
    try {
      const r = await fetch("/__timesavior/llm-logs?limit=10", {cache:"no-store"});
      if (!r.ok) throw new Error("HTTP " + r.status);
      const d = await r.json();
      err.textContent = "";
      const keep = Number(d.keep_days) || 0;
      $("keep").textContent = keep > 0 ? ("保留 " + keep + " 天") : "";
      const sig = signature(d);
      if (sig === lastSig) return;
      lastSig = sig;
      const items = d.items || [];
      const list = $("list");
      const empty = $("empty");
      if (!items.length) {
        list.innerHTML = "";
        empty.hidden = false;
      } else {
        empty.hidden = true;
        list.innerHTML = items.map(itemHTML).join("");
      }
    } catch (e) {
      err.textContent = "刷新失败：" + e.message;
    }
  }
  document.getElementById("list").addEventListener("click", (ev) => {
    const btn = ev.target && ev.target.closest ? ev.target.closest("[data-expand]") : null;
    if (!btn) return;
    const pre = btn.previousElementSibling;
    if (pre && pre.tagName === "PRE") {
      pre.classList.add("expanded");
      btn.remove();
    }
  });
  tsConnectLive({
    llm_log: () => load(),
    status: () => {}
  });
  load();
})();
</script>
</body>
</html>
`
