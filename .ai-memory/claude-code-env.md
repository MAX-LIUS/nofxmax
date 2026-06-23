# Claude Code 环境配置笔记

> **更新**: 2026-06-22
> **范围**: 本机 Claude Code CLI 配置,与业务代码无关

---

## 上下文窗口 / auto-compact(已解决 compact 失败)

### 现象
- CLI 在 ~190k 就提示 context 100%,随后 compact 失败卡死。
- 用户怀疑模型上下文不止 190k —— 判断正确。

### 实测结论(2026-06-22)
- 走本地网关 `ANTHROPIC_BASE_URL=http://127.0.0.1:4000`,模型 `claude-opus-4-8`。
- 网关是 **new-api / one-api**(响应头 `x-new-api-version`、`x-oneapi-request-id`),不是 LiteLLM,所以 `/model/info` 等端点 404。
- 直接对网关灌超大请求二分实测 primary 渠道真实窗口:
  - 565,718 tokens 通过;~575k 起报 `Input is too long (CONTENT_LENGTH_EXCEEDS_THRESHOLD)`。
  - **primary 真实上下文窗口 ≈ 570k tokens**(非标准 200k,也非 1M,是网关自定义)。

### 根因
- 网关限制不是瓶颈,它给了 ~570k。瓶颈在**客户端**:Claude Code 默认按 200k 算窗口,~190k 触发 auto-compact,白白浪费大半上下文,且 compact 摘要请求自身贴墙导致失败。

### 已做的修复
- `~/.claude/settings.json` 设 `autoCompactWindow: 500000`(下次启动 CLI 生效)。
- 选 500k 的理由:用足大窗口(~475k 才触发压缩),距 566k 硬上限留 66k+ 余量给 compact 摘要请求和输出,根治 compact 失败。
- 注意:`autoCompactWindow` schema 允许 100000–1000000。

### 待办 / 风险
- 网关有 primary/backup(用户称 backup 为 novai),backup 是**被动 failover**(primary 命中 429/5xx 或 overloaded/temporarily unavailable 关键词才自动切),无法用 header 主动指定,故 backup 窗口未测。
- 若 failover 到 backup 且其窗口 < 500k,compact 请求可能超限。真遇到再把 `autoCompactWindow` 临时下调。

### 其他
- 状态栏(statusLine)已从 token 进度条改为复刻用户 bash PS1 风格(绿 user@host : 蓝 cwd,home 收缩为 ~)。
