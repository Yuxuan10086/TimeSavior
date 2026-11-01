package ui

// DESIGN SPECIFICATION
// ====================
// 1. Purpose Statement: TimeSavior 是本机生产力管控工具。用户通过状态看板了解当前限制、
//    通过使用统计审视时长、通过临时解锁完成挑战。界面须像正规产品设置/控制台，而非调试页。
// 2. Aesthetic Direction: Soft editorial · refined productivity（浅色精致生产力 · 冷色）
// 3. Color Palette:
//    --bg #eef1f4 · --surface #f7f9fb · --elevated #ffffff · --line #d3dae3
//    --fg #10151c · --muted #3d4654 · --accent #2f5f7a（青石蓝）
//    --ok #1a6b58 · --warn #9a5a12 · --bad #b42318
//    bucket: video #b85a4c · ai #2a6f78 · news #4f6b52 · app/system #3a6b82
// 4. Typography: Fraunces（品牌/大标题）+ Source Sans 3（界面）+ JetBrains Mono（数据）
//    + PingFang SC / Microsoft YaHei（中文）
//    禁止 Inter / Roboto / Arial / system-ui；禁止 purple/violet/indigo 渐变；禁止 emoji 图标。
// 5. Layout Strategy: 主内容 max-width 容器水平居中（margin:0 auto）；顶栏与导航同宽对齐；
//    整体像产品设置页，不做左偏工业轨。背景用冷纸色 + 淡青石蓝光晕营造氛围。

const fontLinks = `
<link rel="preconnect" href="https://fonts.googleapis.com"/>
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin/>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,600;9..144,700&family=JetBrains+Mono:wght@400;500;600&family=Source+Sans+3:wght@400;500;600;700&display=swap" rel="stylesheet"/>`

// SharedCSS is the product shell stylesheet (no fmt verbs — safe in raw HTML).
const SharedCSS = `
:root {
  --bg:#eef1f4; --surface:#f7f9fb; --elevated:#ffffff; --line:#d3dae3;
  --fg:#10151c; --muted:#3d4654; --accent:#2f5f7a; --accent-dim:rgba(47,95,122,.11);
  --accent-border:rgba(47,95,122,.28);
  --ok:#1a6b58; --warn:#9a5a12; --bad:#b42318;
  --video:#b85a4c; --ai:#2a6f78; --news:#4f6b52; --app:#3a6b82;
  --radius:10px;
  --font:"Source Sans 3","PingFang SC","Microsoft YaHei",sans-serif;
  --display:"Fraunces","Source Han Serif SC","Songti SC",serif;
  --mono:"JetBrains Mono",Consolas,monospace;
  --shell:1280px; /* all admin tabs share this max-width */
}
*,*::before,*::after { box-sizing:border-box; }
html,body { margin:0; min-height:100%; }
body {
  color:var(--fg); font-family:var(--font); font-size:15px; line-height:1.55;
  background:
    radial-gradient(920px 420px at 50% -8%, rgba(47,95,122,.10) 0%, transparent 55%),
    radial-gradient(700px 380px at 100% 100%, rgba(42,111,120,.06) 0%, transparent 45%),
    linear-gradient(180deg, #f4f6f8 0%, var(--bg) 40%, #e8ecf1 100%);
}
a { color:var(--accent); text-decoration:none; }
a:hover { text-decoration:underline; }
code, .mono { font-family:var(--mono); font-size:.88em; }
.shell {
  max-width:var(--shell); margin:0 auto; padding:1.75rem 1.35rem 3.5rem;
}
@media (min-width:800px) {
  .shell { padding-left:1.75rem; padding-right:1.75rem; }
}
.topbar {
  display:flex; flex-wrap:wrap; gap:.75rem 1.25rem;
  align-items:flex-start; justify-content:space-between;
  margin-bottom:1.15rem; text-align:left;
}
.brand-mark {
  font-family:var(--display); font-weight:700; font-size:1.05rem;
  letter-spacing:-.02em; color:var(--accent); margin:0 0 .4rem;
}
.topbar h1 {
  font-family:var(--display); font-size:clamp(1.65rem, 3.2vw, 2.1rem); font-weight:700;
  letter-spacing:-.03em; margin:0; line-height:1.15; color:var(--fg);
}
.sub { color:var(--muted); margin:.4rem 0 0; font-size:.94rem; }
.live {
  display:inline-flex; align-items:center; gap:.5rem;
  font-family:var(--mono); font-size:.72rem; color:var(--ok);
  margin-top:.2rem; flex-shrink:0;
}
.live.off { color:var(--warn); }
.live.off .dot { background:var(--warn); animation:none; }
.live .dot {
  width:.45rem; height:.45rem; border-radius:50%; background:var(--ok);
  box-shadow:0 0 0 0 rgba(26,107,88,.45); animation:pulse 1.8s infinite;
}
@keyframes pulse {
  0% { box-shadow:0 0 0 0 rgba(26,107,88,.45); }
  70% { box-shadow:0 0 0 7px rgba(26,107,88,0); }
  100% { box-shadow:0 0 0 0 rgba(26,107,88,0); }
}
.nav {
  display:flex; flex-wrap:wrap; gap:0; margin:0 0 1.35rem;
  border-bottom:1px solid var(--line); justify-content:flex-start;
}
.nav a {
  position:relative; color:var(--muted); text-decoration:none;
  font-size:.9rem; font-weight:600; padding:.65rem .15rem .7rem; margin-right:1.35rem;
}
.nav a:hover { color:var(--fg); text-decoration:none; }
.nav a.on { color:var(--fg); }
.nav a.on::after {
  content:""; position:absolute; left:0; right:0; bottom:-1px;
  height:2px; background:var(--accent); border-radius:2px 2px 0 0;
}
.panel {
  background:var(--surface); border:1px solid var(--line); border-radius:var(--radius);
  padding:1.2rem 1.25rem; margin-bottom:.9rem;
  box-shadow:0 1px 0 rgba(16,21,28,.04);
}
.panel.accent-edge { border-top:3px solid var(--accent); }
.panel h2 {
  font-size:.92rem; font-weight:700; font-family:var(--font);
  letter-spacing:.02em; text-transform:none; color:var(--fg);
  margin:0 0 .9rem;
}
.hint { color:var(--muted); font-size:.88rem; margin:0 0 .85rem; }
.metrics {
  display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:.65rem;
}
@media (min-width:640px) {
  .metrics { grid-template-columns:repeat(4,minmax(0,1fr)); }
}
.metric {
  background:var(--elevated); border:1px solid var(--line); border-radius:calc(var(--radius) - 2px);
  padding:.85rem .9rem; min-width:0;
}
.metric .k {
  color:var(--muted); font-size:.74rem; font-family:var(--mono);
  letter-spacing:.04em; text-transform:uppercase; margin-bottom:.3rem; font-weight:600;
}
.metric .v {
  font-family:var(--mono); font-size:1.05rem; font-weight:650;
  word-break:break-word; color:var(--fg);
}
.split {
  display:grid; grid-template-columns:1fr; gap:.9rem;
}
@media (min-width:700px) {
  .split { grid-template-columns:minmax(0,1fr) minmax(0,1fr); }
}
table.data { width:100%; border-collapse:collapse; font-size:.9rem; }
table.data th, table.data td {
  text-align:left; padding:.55rem .35rem; border-bottom:1px solid var(--line); vertical-align:top;
}
table.data th {
  color:var(--muted); font-weight:650; font-size:.74rem; font-family:var(--mono);
  letter-spacing:.05em; text-transform:uppercase;
}
table.data td { color:var(--fg); }
.tag {
  display:inline-block; padding:.12rem .45rem; border-radius:999px;
  font-size:.7rem; font-family:var(--mono); letter-spacing:.03em;
  background:var(--accent-dim); color:var(--accent); border:1px solid var(--accent-border);
  margin-left:.25rem; vertical-align:middle;
}
.ok { color:var(--ok); }
.warn, .bad { color:var(--warn); }
.danger { color:var(--bad); }
.muted { color:var(--muted); }
.foot {
  color:var(--muted); font-size:.8rem; font-family:var(--mono);
  margin-top:1.5rem; padding-top:1rem; border-top:1px solid var(--line);
}
.err { color:var(--bad); font-size:.85rem; min-height:1.2em; }
.icon {
  display:inline-block; width:1em; height:1em; vertical-align:-.12em;
  fill:none; stroke:currentColor; stroke-width:1.75; stroke-linecap:round; stroke-linejoin:round;
}
.btn {
  appearance:none; border:1px solid var(--line); background:var(--elevated); color:var(--fg);
  font:inherit; font-weight:600; border-radius:8px; padding:.7rem 1rem; cursor:pointer;
}
.btn:hover { border-color:var(--accent); }
.btn-primary {
  background:var(--accent); border-color:var(--accent); color:#f3f8fb;
}
.btn-danger {
  background:transparent; border-color:rgba(180,35,24,.35); color:var(--bad);
}
.btn-danger:hover { background:rgba(180,35,24,.06); border-color:var(--bad); }
.btn-primary:disabled, .btn:disabled { opacity:.5; cursor:not-allowed; }
.chip-row { display:flex; flex-wrap:wrap; gap:.4rem; }
.chip {
  background:var(--elevated); border:1px solid var(--line); border-radius:999px;
  padding:.22rem .7rem; font-size:.8rem; font-family:var(--mono); color:var(--muted);
}
.chip b { color:var(--fg); font-weight:650; }
.advanced {
  margin-top:1.25rem; border-top:1px dashed var(--line); padding-top:.85rem;
}
.advanced summary {
  cursor:pointer; color:var(--muted); font-size:.82rem; font-family:var(--mono);
  letter-spacing:.03em; text-transform:uppercase; list-style:none; font-weight:600;
}
.advanced summary::-webkit-details-marker { display:none; }
.advanced[open] summary { margin-bottom:.65rem; color:var(--fg); }
.advanced .tech {
  color:var(--muted); font-size:.8rem; font-family:var(--mono); line-height:1.65;
}
`

// liveSSEJS is a tiny EventSource client with reconnect (shared by admin pages).
const liveSSEJS = `
function tsConnectLive(handlers) {
  let es = null;
  let retry = 0;
  let timer = null;
  const liveEl = () => document.getElementById("live");
  function setLive(text, ok) {
    const el = liveEl();
    if (!el) return;
    el.textContent = text;
    el.parentElement && el.parentElement.classList.toggle("off", !ok);
  }
  function connect() {
    if (es) { try { es.close(); } catch (_) {} }
    es = new EventSource("/__timesavior/events");
    es.onopen = () => {
      retry = 0;
      setLive("实时连接", true);
    };
    es.onerror = () => {
      setLive("重连中…", false);
      try { es.close(); } catch (_) {}
      es = null;
      const wait = Math.min(10000, 800 * Math.pow(1.6, retry++));
      clearTimeout(timer);
      timer = setTimeout(connect, wait);
    };
    const wrap = (kind, fn) => {
      es.addEventListener(kind, (ev) => {
        let data = {};
        try { data = JSON.parse(ev.data || "{}"); } catch (_) {}
        try { fn(data, kind); } catch (e) { console.warn(e); }
      });
    };
    wrap("ping", () => {});
    if (handlers) {
      Object.keys(handlers).forEach((k) => wrap(k, handlers[k]));
    }
  }
  connect();
  return { reconnect: connect };
}
`

func shellNav(active string) string {
	item := func(key, href, label string) string {
		cls := ""
		if key == active {
			cls = ` class="on"`
		}
		return `<a` + cls + ` href="` + href + `">` + label + `</a>`
	}
	return `<nav class="nav" aria-label="主导航">` +
		item("dashboard", "/", "状态看板") +
		item("stats", "/stats", "使用统计") +
		item("unlock", "/unlock", "临时解锁") +
		item("llm-logs", "/llm-logs", "大模型日志") +
		`</nav>`
}
