package ui

import (
	"net/http"
	"strconv"
	"strings"
)

func WriteStatsPage(w http.ResponseWriter, adminURL string, days int, from, to string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminURL
	body := statsHTML
	body = strings.Replace(body, "__DAYS__", strconv.Itoa(days), 1)
	body = strings.Replace(body, "__FROM__", from, 1)
	body = strings.Replace(body, "__TO__", to, 1)
	_, _ = w.Write([]byte(body))
}

var statsHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TimeSavior · 统计</title>
` + fontLinks + `
<style>
` + SharedCSS + `
.days { display:flex; gap:.35rem; flex-wrap:wrap; margin:0 0 .55rem; align-items:center; }
.days button {
  appearance:none; border:1px solid var(--line); background:transparent;
  color:var(--muted); border-radius:var(--radius); padding:.32rem .75rem;
  font:inherit; font-size:.82rem; font-family:var(--mono); cursor:pointer;
}
.days button.on, .days button:hover {
  border-color:var(--accent); color:var(--fg); background:var(--accent-dim);
}
.range-pick {
  display:flex; flex-wrap:wrap; gap:.4rem .55rem; align-items:center;
  margin:0 0 .45rem;
}
.range-pick label {
  display:inline-flex; align-items:center; gap:.3rem;
  color:var(--muted); font-size:.82rem; font-family:var(--mono);
}
.range-pick input[type="date"] {
  appearance:none; border:1px solid var(--line); background:var(--elevated);
  color:var(--fg); border-radius:var(--radius); padding:.28rem .45rem;
  font:inherit; font-size:.82rem; font-family:var(--mono);
}
.range-pick input[type="date"]:focus {
  outline:none; border-color:var(--accent); box-shadow:0 0 0 2px var(--accent-dim);
}
.range-pick button.apply {
  appearance:none; border:1px solid var(--accent-border); background:var(--accent-dim);
  color:var(--fg); border-radius:var(--radius); padding:.32rem .75rem;
  font:inherit; font-size:.82rem; font-family:var(--mono); cursor:pointer;
}
.range-pick button.apply:hover { border-color:var(--accent); }
.range {
  color:var(--muted); font-size:.78rem; font-family:var(--mono);
  margin:0 0 1.1rem;
}
.row {
  /* name col sized for 23 CJK glyphs (em of .name); bar takes leftover (narrower) */
  display:grid; grid-template-columns:1.1rem max-content minmax(2rem,1fr) 4.5rem; gap:.4rem;
  align-items:center; margin:.45rem 0;
}
@media (max-width:640px) {
  .row { grid-template-columns:1.1rem 1fr 3.8rem; }
  .row .name { width:auto; max-width:none; }
  .row .bar { grid-column:1 / -1; margin-left:1.65rem; }
}
.row .tog {
  width:1.1rem; height:1.1rem; display:flex; align-items:center; justify-content:center;
  color:var(--muted); font-size:.7rem; line-height:1; user-select:none;
}
.row.agg { cursor:pointer; border-radius:6px; padding:.12rem .2rem; margin-left:-.2rem; margin-right:-.2rem; }
.row.agg:hover { background:rgba(47,95,122,.06); }
.row.agg .tog::before { content:"▸"; transition:transform .15s ease; display:inline-block; }
.row.agg.open .tog::before { transform:rotate(90deg); }
.row:not(.agg) .tog { visibility:hidden; }
.row .name {
  font-weight:600; font-size:.95rem;
  width:23em; max-width:23em; min-width:0;
  white-space:nowrap; overflow:hidden; text-overflow:ellipsis;
}
.row.child {
  margin:.22rem 0; opacity:.92;
}
.row.child .name { font-weight:500; font-size:.86rem; color:var(--muted); }
.row.child .bar { height:.32rem; }
.row.child .tog { visibility:hidden; }
.kids { display:none; padding:0 0 .15rem .35rem; border-left:2px solid var(--accent-border); margin:0 0 .4rem 1.45rem; }
.kids.open { display:block; }
.bar {
  height:.45rem; background:var(--bg); border:1px solid var(--line); border-radius:1px; overflow:hidden;
  min-width:0;
}
.bar > i { display:block; height:100%; transition:width .55s ease; }
.g-browser_video > i { background:var(--video); }
.g-browser_ai > i { background:var(--ai); }
.g-browser_news > i { background:var(--news); }
.g-app > i, .g-system > i { background:var(--app); }
.dur {
  text-align:right; color:var(--muted); font-family:var(--mono);
  font-variant-numeric:tabular-nums; font-size:.85rem;
}
.row.child .dur { font-size:.8rem; }
.site-grid {
  display:grid; grid-template-columns:repeat(auto-fit,minmax(140px,1fr));
  gap:.55rem; margin-bottom:.85rem;
}
</style>
</head>
<body>
<div class="shell">
  <header class="topbar">
    <div>
      <p class="brand-mark">TimeSavior</p>
      <h1>使用统计</h1>
    </div>
    <div class="live" title="事件推送"><span class="dot" aria-hidden="true"></span><span id="live">连接中</span></div>
  </header>
` + shellNav("stats") + `
  <div class="days" id="days" role="group" aria-label="快捷范围">
    <button type="button" data-d="1">今天</button>
    <button type="button" data-d="7">近 7 天</button>
    <button type="button" data-d="14">近 14 天</button>
  </div>
  <div class="range-pick" id="rangePick">
    <label>起 <input type="date" id="fromDay" /></label>
    <label>止 <input type="date" id="toDay" /></label>
    <button type="button" class="apply" id="applyRange">查询</button>
  </div>
  <p class="range" id="range"></p>

  <section class="panel accent-edge">
    <h2>总览</h2>
    <div class="metrics" id="overview">
      <div class="metric" data-k="app"><div class="k">前台总时长</div><div class="v" id="ov-app">—</div></div>
      <div class="metric" data-k="video"><div class="k">播放总时长</div><div class="v" id="ov-video">—</div></div>
      <div class="metric" data-k="focus"><div class="k">焦点会话</div><div class="v" id="ov-focus">—</div></div>
      <div class="metric" data-k="play"><div class="k">播放脉冲</div><div class="v" id="ov-play">—</div></div>
    </div>
  </section>

  <section class="panel">
    <h2>应用统计</h2>
    <div id="apps"></div>
  </section>

  <section class="panel">
    <h2>视频播放统计</h2>
    <div class="site-grid" id="authors"></div>
    <p class="muted" id="sites-note" style="margin:.35rem 0 .75rem;font-size:.78rem"></p>
    <table class="data">
      <thead><tr><th>作者</th><th>站点</th><th>近期标题</th><th>时长</th></tr></thead>
      <tbody id="videos"></tbody>
    </table>
  </section>

  <p class="err" id="err"></p>
</div>
<script>
` + liveSSEJS + `
const initDays = __DAYS__;
const initFrom = "__FROM__";
const initTo = "__TO__";
let days = initDays || 1;
let fromDay = initFrom || "";
let toDay = initTo || "";
let useRange = !!(fromDay && toDay);
let lastSig = "";
const expanded = new Set();

function fmtDur(sec) {
  sec = Number(sec) || 0;
  if (sec < 60) return Math.round(sec) + "s";
  if (sec < 3600) return (sec/60).toFixed(1) + " 分钟";
  return (sec/3600).toFixed(1) + " 小时";
}
function siteLabel(s) {
  return ({youtube:"YouTube", bilibili:"B站", douyin:"抖音"}[s] || s);
}
function esc(s) {
  return String(s||"").replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
function setText(id, text) {
  const el = document.getElementById(id);
  if (el && el.textContent !== String(text)) el.textContent = text;
}
function signature(d) {
  return JSON.stringify({
    a: d.app_total_sec, v: d.video_total_sec, f: d.focus_sessions, p: d.play_pulses,
    apps: (d.apps||[]).map(x => [x.bucket, x.duration_sec, x.share_pct,
      (x.children||[]).map(c => [c.bucket, c.short_name, c.duration_sec])]),
    by: d.video_by_site,
    vids: (d.videos||[]).map(x => [x.author, x.site, x.title, x.duration_sec, x.watch_exempt])
  });
}
function cleanAuthor(s) {
  return String(s||"").replace(/[\s·\-–—_|]*(白名单中?|whitelist)\s*$/i, "").trim();
}
function exemptBadge(on) {
  return on ? '<span class="tag" title="观看豁免">豁免</span>' : '';
}
function statsURL() {
  if (useRange && fromDay && toDay) {
    return "/__timesavior/stats?from=" + encodeURIComponent(fromDay) + "&to=" + encodeURIComponent(toDay);
  }
  return "/__timesavior/stats?days=" + days;
}
function syncURL() {
  if (useRange && fromDay && toDay) {
    history.replaceState(null, "", "/stats?from=" + encodeURIComponent(fromDay) + "&to=" + encodeURIComponent(toDay));
  } else {
    history.replaceState(null, "", "/stats?days=" + days);
  }
}
function syncPresetButtons() {
  [...document.getElementById("days").children].forEach(x => {
    const on = !useRange && Number(x.dataset.d) === days;
    x.classList.toggle("on", on);
  });
}
function syncDateInputs() {
  const f = document.getElementById("fromDay");
  const t = document.getElementById("toDay");
  if (f && fromDay) f.value = fromDay;
  if (t && toDay) t.value = toDay;
}

function renderAppRow(a) {
  const kids = a.children || [];
  const can = kids.length > 0;
  const open = can && expanded.has(a.bucket);
  const g = esc(a.group||"app");
  let html = '<div class="row'+(can?' agg':'')+(open?' open':'')+'" data-bucket="'+esc(a.bucket)+'"'+(can?' role="button" tabindex="0" aria-expanded="'+(open?'true':'false')+'"':'')+'>'+
    '<div class="tog" aria-hidden="true"></div>'+
    '<div class="name">'+esc(a.bucket)+'</div>'+
    '<div class="bar g-'+g+'"><i style="width:'+(a.share_pct||0).toFixed(1)+'%"></i></div>'+
    '<div class="dur">'+fmtDur(a.duration_sec)+'</div></div>';
  if (can) {
    html += '<div class="kids'+(open?' open':'')+'" data-parent="'+esc(a.bucket)+'">'+
      kids.map(c => {
        const label = c.short_name || c.bucket;
        const tip = c.full_title || c.last_title || c.bucket || "";
        return '<div class="row child"><div class="tog"></div><div class="name" title="'+esc(tip)+'">'+esc(label)+'</div>'+
        '<div class="bar g-'+esc(c.group||a.group||"app")+'"><i style="width:'+(c.share_pct||0).toFixed(1)+'%"></i></div>'+
        '<div class="dur">'+fmtDur(c.duration_sec)+'</div></div>';
      }).join("")+
    '</div>';
  }
  return html;
}

async function load() {
  const err = document.getElementById("err");
  try {
    const r = await fetch(statsURL(), {cache:"no-store"});
    if (!r.ok) throw new Error("HTTP " + r.status);
    const d = await r.json();
    err.textContent = "";
    if (d.from_day) fromDay = d.from_day;
    if (d.to_day) toDay = d.to_day;
    syncDateInputs();
    patch(d);
    const live = document.getElementById("live");
    if (live) live.textContent = "已更新 " + (d.generated_at || new Date().toLocaleTimeString());
  } catch (e) {
    err.textContent = "刷新失败：" + e.message;
  }
}

function patch(d) {
  const sig = signature(d);
  if (sig === lastSig) return;
  lastSig = sig;

  const from = d.from_day || "";
  const to = d.to_day || "";
  setText("range", from && to ? (from === to ? from : from + " ~ " + to) : "");

  setText("ov-app", fmtDur(d.app_total_sec));
  setText("ov-video", fmtDur(d.video_total_sec));
  setText("ov-focus", d.focus_sessions||0);
  setText("ov-play", d.play_pulses||0);

  const apps = d.apps || [];
  const appsEl = document.getElementById("apps");
  const appsHTML = apps.length ? apps.map(renderAppRow).join("") : '<p class="muted">暂无焦点数据</p>';
  if (appsEl.dataset.sig !== sig) {
    appsEl.innerHTML = appsHTML;
    appsEl.dataset.sig = sig;
  }

  // Top summary cards: aggregate by site; main table below stays by author.
  const bySite = d.video_by_site || {};
  let siteKeys = Object.keys(bySite);
  siteKeys.sort((a,b) => (bySite[b]||0) - (bySite[a]||0));
  const topSites = siteKeys.slice(0, 6);
  const authorsEl = document.getElementById("authors");
  const authorsHTML = topSites.length ? topSites.map(name =>
    '<div class="metric"><div class="k">'+esc(siteLabel(name))+'</div><div class="v">'+fmtDur(bySite[name])+'</div></div>'
  ).join("") : '<div class="metric"><div class="k">站点</div><div class="v muted">暂无</div></div>';
  if (authorsEl.dataset.html !== authorsHTML) {
    authorsEl.innerHTML = authorsHTML;
    authorsEl.dataset.html = authorsHTML;
  }

  const note = document.getElementById("sites-note");
  if (note) note.textContent = "";

  const vids = d.videos || [];
  const vidsEl = document.getElementById("videos");
  const vidsHTML = vids.length ? vids.map(v => {
    const title = v.title || "";
    const titleCell = title ? esc(title) : '<span class="muted">—</span>';
    const author = cleanAuthor(v.author) || "未识别";
    return '<tr><td><strong>'+esc(author)+'</strong>'+exemptBadge(v.watch_exempt)+
      '</td><td>'+esc(siteLabel(v.site))+'</td><td>'+titleCell+
      '</td><td class="mono">'+fmtDur(v.duration_sec)+'</td></tr>';
  }).join("") : '<tr><td colspan="4" class="muted">暂无播放流量</td></tr>';
  if (vidsEl.dataset.html !== vidsHTML) {
    vidsEl.innerHTML = vidsHTML;
    vidsEl.dataset.html = vidsHTML;
  }
}

document.getElementById("apps").addEventListener("click", e => {
  const row = e.target.closest(".row.agg");
  if (!row || !document.getElementById("apps").contains(row)) return;
  const key = row.dataset.bucket;
  if (!key) return;
  if (expanded.has(key)) expanded.delete(key); else expanded.add(key);
  row.classList.toggle("open", expanded.has(key));
  row.setAttribute("aria-expanded", expanded.has(key) ? "true" : "false");
  const kids = row.nextElementSibling;
  if (kids && kids.classList.contains("kids")) {
    kids.classList.toggle("open", expanded.has(key));
  }
});
document.getElementById("apps").addEventListener("keydown", e => {
  if (e.key !== "Enter" && e.key !== " ") return;
  const row = e.target.closest(".row.agg");
  if (!row) return;
  e.preventDefault();
  row.click();
});

document.getElementById("days").addEventListener("click", e => {
  const b = e.target.closest("button");
  if (!b) return;
  days = Number(b.dataset.d) || 1;
  useRange = false;
  fromDay = "";
  toDay = "";
  const f = document.getElementById("fromDay");
  const t = document.getElementById("toDay");
  if (f) f.value = "";
  if (t) t.value = "";
  syncPresetButtons();
  syncURL();
  lastSig = "";
  load();
});

document.getElementById("applyRange").addEventListener("click", () => {
  const f = document.getElementById("fromDay").value;
  const t = document.getElementById("toDay").value;
  if (!f && !t) return;
  fromDay = f || t;
  toDay = t || f;
  if (fromDay > toDay) { const x = fromDay; fromDay = toDay; toDay = x; }
  useRange = true;
  syncPresetButtons();
  syncDateInputs();
  syncURL();
  lastSig = "";
  load();
});

syncPresetButtons();
syncDateInputs();
load();
tsConnectLive({
  stats: () => load(),
  status: () => {}
});
</script>
</body>
</html>
`
