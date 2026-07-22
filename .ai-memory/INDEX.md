# AI 记忆主索引（恢复入口）

> **作用**: 这是会话记忆的唯一入口。无论上下文被 compact 还是 clear，新会话只要从这里开始，就能恢复全部"原始记忆"和"固定开发管理方法"。
> **更新**: 2026-06-24
> **维护约定**: 任何新增/变更的长期记忆，都必须在本文件登记一行，否则视为未固化。

---

## 0. 恢复协议（compact / clear 后必做）

新会话或上下文被压缩/清空后，**按顺序**读以下文件即可恢复工作状态：

1. 本文件 `.ai-memory/INDEX.md` —— 全局地图
2. `.ai-memory/communication-preferences.md` —— 沟通约定（**始终中文**）
3. `docs/FUXI_WORKFLOW_CN.md` —— 固定开发管理方法（优先级/门禁/风险边界，必须服从）
4. 与当前任务相关的专题记忆（见第 2 节）
5. 最近进展：`git log --oneline -15` + `docs/DEVLOG.md` 尾部

> 原则：**不靠口头记忆，规则必须落在仓库文件里**。本索引就是那张地图。

---

## 1. 长期记忆文件（.ai-memory/）

| 文件 | 内容 | 状态 |
|---|---|---|
| `INDEX.md` | 本主索引 / 恢复入口 | 活跃 |
| `communication-preferences.md` | 语言（中文）、风格、高风险操作需确认 | 活跃 |
| `claude-code-env.md` | 本机 CLI 配置：上下文窗口 ~570k、autoCompactWindow=500k、网关 failover | 活跃 |
| `unified-protection-system.md` | 统一保护系统全量记忆（churn/回吐护栏/GPT custom_prompt bug 等） | 活跃 v1.12 |
| `binance-native-trailing.md` | BN 原生 trailing 修复（go-binance v2.8.9→2.8.10 参数键名 bug、algo 端点、close 缓存裸仓修复、遗留 issue 1/2） | 活跃 |

---

## 2. 固定开发管理方法（docs/，必须服从）

| 文件 | 作用 |
|---|---|
| `docs/FUXI_WORKFLOW_CN.md` | **总工作流**：优先级原则、执行原则、风险控制、自治推进模式 |
| `docs/DURABLE_EXECUTION_WORKFLOW_CN.md` | 长任务续航（TaskFlow × Agentic Coding） |
| `docs/Git工作流规范.md` | 分支/提交规范（dev 主线、feat/hotfix、commit 格式） |
| `docs/DECISIONS.md` | 关键决策记录 |
| `docs/DEVLOG.md` | 开发日志（最近进展看尾部） |
| `docs/TODO.md` / `docs/RISKS.md` | 待办 / 风险登记 |
| `docs/PROJECT_MEMORY_ARCHIVE_CN.md` | 接管阶段记忆归档总表 |

治理文档族（按需查）：`PROJECT_OVERVIEW_CN` / `ARCHITECTURE_CN` / `MODULE_INDEX_CN` / `DATA_MODEL_RELATIONS_CN` / `SYSTEM_TRUST_BOUNDARY_CN`。

---

## 3. 当前在途工作（滚动更新，已完成即移除）

> 2026-06-24 会话：三交易员排查 + 修复

- **已完成代码（未部署）**：① entry_time 兜底(OKX createdTime) ② peak 持久化+重启恢复(新表 `peak_pnl_states`) ③ 防空响应批量误平守卫(`MarkOpenPositionsAbsentFromExchangeClosed`) ④ UI 净盈亏移右侧+红绿 ⑤ NovaI fallback key 已替换进 DB。
- **验证**：`go build` ✅、`go test ./store/ ./trader/` ✅（含 2 个新守卫测试）、前端 `tsc` ✅。前端 1 个失败单测(`Ladder planned levels`)是 baseline 既有，非本次引入。
- **待办**：等用户定部署方式后重启 `nofx-trading` 生效。注意容器 `/app/nofx`(12:12)是未知暂存二进制，安全做法=从源码重建。
- **第 3 项守卫策略**：缺失 ≥2 且=全部本地 OPEN 时判瞬时故障跳过；单个消失仍正常平。
