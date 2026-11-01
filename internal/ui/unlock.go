package ui

import (
	"net/http"
)

// WriteUnlockPage serves the feed-triggered unlock status / cancel UI.
func WriteUnlockPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(unlockHTML))
}

var unlockHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TimeSavior · 临时解锁</title>
` + fontLinks + `
<style>
` + SharedCSS + `
.msg { min-height:1.2em; margin:0 0 .75rem; color:var(--warn); font-size:.92rem; }
.msg:empty { display:none; }
.rule-line {
  color:var(--muted); font-size:.86rem; font-family:var(--mono);
  margin:0 0 1rem; line-height:1.55;
}
.rule-line b { color:var(--fg); font-weight:600; font-variant-numeric:tabular-nums; }
.until {
  font-family:var(--mono); font-size:1.15rem; font-weight:600;
  margin:.35rem 0 0;
}
.status-line {
  font-family:var(--mono); font-size:.95rem; margin:0 0 .75rem; color:var(--fg);
}
.quota-line {
  display:flex; flex-wrap:wrap; gap:.45rem; margin:0 0 .55rem;
  font-family:var(--mono); font-size:.86rem; color:var(--muted);
}
.quota-line .chip {
  border-color:var(--accent-border); background:var(--accent-dim); color:var(--fg);
  font-size:.86rem; padding:.35rem .85rem;
}
.quota-line .chip b { font-variant-numeric:tabular-nums; color:var(--accent); }
.actions { display:flex; flex-wrap:wrap; gap:.55rem; margin-top:1rem; align-items:center; }
.hidden { display:none !important; }
</style>
</head>
<body>
<div class="shell">
  <header class="topbar">
    <div>
      <p class="brand-mark">TimeSavior</p>
      <h1>临时解锁</h1>
    </div>
    <div class="live" title="事件推送"><span class="dot" aria-hidden="true"></span><span id="live">连接中</span></div>
  </header>
` + shellNav("unlock") + `
  <section class="panel accent-edge">
    <h2>状态</h2>
    <div class="quota-line" aria-label="额度">
      <span class="chip">今日 <b><span id="remain">未用</span></b></span>
      <span class="chip">窗口 <b><span id="window">—</span></b></span>
    </div>
    <p class="rule-line" id="ruleLine">
      <b><span id="winStart">21:30</span></b>–次日 <b><span id="winEnd">04</span>:00</b>
      内首次打开管控站推荐页，自动解锁 <b><span id="mins">30</span></b> 分钟；未打开不顺延。
    </p>
    <p class="msg" id="msg"></p>
    <p class="status-line" id="statusLine">加载中…</p>
    <p class="until hidden" id="untilLabel"></p>
    <div class="actions">
      <button class="btn btn-danger hidden" id="cancelBtn" type="button">取消解封</button>
    </div>
  </section>
</div>
<script>
` + liveSSEJS + `
(function () {
  const $ = (id) => document.getElementById(id);
  let canceling = false;

  function fmtTime(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (isNaN(d)) return iso;
    const p = (n) => String(n).padStart(2,"0");
    return p(d.getHours())+":"+p(d.getMinutes())+":"+p(d.getSeconds());
  }

  function apply(st, errFallback) {
    const mins = st.unlock_minutes ?? 30;
    const winStart = st.window_start || "21:30";
    const winEnd = st.window_end_hour ?? 4;
    $("mins").textContent = mins;
    $("winStart").textContent = winStart;
    $("winEnd").textContent = String(winEnd).padStart(2,"0");

    const started = !!(st.temp_unlock_started || (st.daily_used ?? 0) > 0);
    $("remain").textContent = started ? "已用" : "未用";
    $("window").textContent = st.feed_unlock_window ? "进行中" : "未开始";

    if (errFallback) {
      $("msg").textContent = errFallback;
    } else if (st.enabled === false) {
      $("msg").textContent = "临时解锁已禁用";
    } else if (st.message && /已用完|已禁用|不在可用|无法/.test(st.message)) {
      $("msg").textContent = st.message;
    } else {
      $("msg").textContent = "";
    }

    const active = !!st.temp_unlock_active;
    $("cancelBtn").classList.toggle("hidden", !active);
    if (active) {
      $("statusLine").textContent = "临时解锁中";
      $("untilLabel").classList.remove("hidden");
      $("untilLabel").textContent = st.temp_unlock_until
        ? ("至 " + fmtTime(st.temp_unlock_until))
        : "已解锁";
    } else if (started) {
      $("statusLine").textContent = "今日额度已用完，次日 04:00 后重新计";
      $("untilLabel").classList.add("hidden");
    } else if (st.feed_unlock_window) {
      $("statusLine").textContent = "窗口内：打开任意管控站推荐页即可开始计时";
      $("untilLabel").classList.add("hidden");
    } else {
      $("statusLine").textContent = "窗口外：不会开始计时";
      $("untilLabel").classList.add("hidden");
    }
  }

  async function api(path, body) {
    const opt = body == null
      ? { method: "GET" }
      : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
    const r = await fetch(path, opt);
    return r.json();
  }

  async function refresh() {
    try {
      const st = await api("/__timesavior/unlock/state");
      apply(st);
    } catch (e) {
      apply({}, "无法连接本地服务");
    }
  }

  async function cancel() {
    if (canceling) return;
    canceling = true;
    $("cancelBtn").disabled = true;
    try {
      const st = await api("/__timesavior/unlock/cancel", {});
      apply(st);
    } catch (e) {
      apply({}, "取消失败");
    } finally {
      canceling = false;
      $("cancelBtn").disabled = false;
    }
  }

  $("cancelBtn").onclick = cancel;
  refresh();
  tsConnectLive({
    unlock: (st) => { if (st) apply(st); },
    status: () => {}
  });
})();
</script>
</body>
</html>
`
