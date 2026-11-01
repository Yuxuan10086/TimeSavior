# Phase 0 路径探针说明

首次建议用探针模式采集真实请求，再微调 `rules/*.yaml`：

```powershell
.\timesavior.exe -config configs/default.yaml -probe -no-tray -no-sysproxy
# 浏览器临时代理到 127.0.0.1:18888，安装 data/ca/ca.crt 后访问三站
```

## 采集清单

对每个站点操作并观察日志 `METHOD host/path site= cat=`：

1. 打开首页 / 推荐流（应识别为 `feed`）
2. 搜索关键词（应识别为 `search`）
3. 打开视频/回答详情（应识别为 `content` / `play`）
4. 下滑推荐（高频 `feed` API）

## 初版规则来源

`rules/bilibili.yaml`、`zhihu.yaml`、`douyin.yaml`、`youtube.yaml` 已按公开 Web 路径与常见 API 前缀预填。
站点改版后以探针日志为准热更新规则文件即可，无需重启浏览器（代理进程会重载）。
