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
| `unified-protection-system.md` | 统一保护系统全量记忆（churn/回吐护栏/幻影 trailing v1.14/无激活价危险单+re-arm断路器 v1.15/place-at-open 统一保护:开仓挂全档+运行时补丢档+越过 activePx 用 activePx=0 立即重挂(v1.16.0 近价锚定被 OKX tick 取整坑→v1.16.1 热修)+profit-aware 判别/v1.16.2 多档匹配冲突根治:全委托单统一用 orderID 认单/v1.16.3 全档 churn 根治:全档 arm 补写 nativeTrailingArmTime,修 v1.16.2 引入的 dd1 无限重挂堆到 OKX 55 单上限/v1.16.4 面板红+周期重挂根治:同持仓存多条同 ruleFP armed 记录(部分平仓各留一条,身份校验只比 entry 不比 qty),map 随机序取到死单 ID → 改取 UpdatedAt 最大;300s cooldown 保留/v1.16.5 collapse 误撤兄弟档:全档迁移撤光该方向所有 trailing 单(含 4 秒前刚挂的 partial)→ 跳过被其他 armed 记录认领的单/v1.16.6 同 ruleFP 旧 armed 记录标 superseded;并记录两条"不能做"的边界:一刀切清 stale 记录会拉低防降档下限(alloc 只在内存,靠 DB armed 记录跨重启恢复)、"OKX 有僵尸单"已被 cmd/zombieaudit 证伪(orphan=0)/**v1.16.7 该 bug 类第 4 次实例:Binance/Bitget 部分平仓档 arm 分支 + OKX 未打 tag 兜底分支拿到 orderID 却不落库不写 armTime,而 300s cooldown 检查嵌在"已有 armed 记录"分支内 → 没记录=cooldown 永不生效 → 每 20s 重挂,HYPEUSDT 挂单 360 张/小时无上限增长;三处一起补齐+表驱动测试覆盖三交易所** /**v1.16.8 构造交易所对等测试项目(trader/venue_trailing_parity_test.go,4测试×okx/binance,均反向验证)挖出 HEAD 既存缺陷=该 bug 类第5次实例:findPartialTrailingReplacementCandidate 无阈值贪心匹配(b5a7aca 时同侧只挂1张,place-at-open 后 dd1 与局部档并存前提失效)→OKX 局部档 arm 时把 dd1 全平单当"自己漂移了"无条件撤掉;修法=复用 claimedTrailingOrderIDsForPosition 排除已认领单,不加新阈值;并把 300s cooldown 从"已有 armed 记录"分支内上提为无条件限频(armedFingerprints 不含 managed_drawdown→原分支永不可达);同时订正两处我自己的错误结论:①Binance open-algo 不报 callbackRate/activatePrice,适配器拿 triggerPrice 顶替且硬编码 activated→我审计报的活动价是移动止损位,用户读数 60.598 才对 ②审计工具漏调 SetExecutionPreferences(线上默认 preferUSDC=true)→查 SOLUSDT 而真实市场是 SOLUSDC→返回空集不报错→谎报"仓位裸奔"假警报,已修 zombieaudit/orphanlist;HYPEUSDT 仓位已由 break_even_stop 平掉并顺带清空 174 张孤儿单,原计划的不可逆批量撤单未执行也不再需要** /**v1.16.9 该 bug 类第 6 次实例(2026-07-27,用户从面板发现"两档配置却显示三档 0.6/1.2/1.8"):applyNativeProtectionTargetsAfterOpen(protection_execution.go)全文零次 ATR 调用,把 ATR 倍数当百分数直接挂单——另外三条路径(运行时 risk.go:204/展示 decision.go:1223/对账器 reconciler.go:476)都换算过,0efe033 修了展示、26104e6 修了对账器,a56f741 引入的 place-at-open 从没修;误差无固定方向(SOL 1.8ATR 实为 0.911%→太宽 2 倍;KAITO 1.2ATR 实为 3.44%→太窄 1/3);**第二个独立成因**:开仓传的 entry 是 marketData.CurrentPrice=下单前快照价(SOL 76.36 vs 实际 76.28),而 entry 既是 ruleFP 首字段又是 ATR→% 的分母 → 单修换算仍分裂成两族;附带第三后果:未换算档因 getCumulativeCloseRatioByRule 按 MinProfitPct 匹配不上、回退取最高档号累加 → 30% 的档挂成整仓;修法=挂单前调 resolveDrawdownRulesATR + 新增 syncRequestEntryPriceToExchange(用交易所实际持仓 entry 校正,偏离>2% 保留原值,纯 best-effort 不阻塞开仓);测试 trader/postopen_drawdown_atr_test.go 3×2venue 全反向验证;**并订正"OKX 看起来没问题"**:OKX 同样中招(KAITO/SPCX/WLD 都是原始+已换算成对 armed),只因 OKX 上报 callbackRate、面板显示已换算值把原始那条掩住,Binance 不回 callbackRate 才暴露三条并列 → 教训:venue 上报差异会掩盖同一缺陷,"另一交易所看着正常"不是缺陷不存在的证据;遗留待确认:7 个持仓存量多余挂单未清理(高风险)、9 条 6 月僵尸记录(trader 4801de05_..._1781597131 已不在 traders 表)、v1.16.8+v1.16.9 均未提交** /**v1.16.10 该 bug 类第 7 次实例(2026-07-27,清理 SOL 原始倍数单后 dd1 再没补挂):换算后的 dd1 指纹与被撤的原始倍数单不同 → 无 stored orderID → 落到兜底匹配"本方向第一张 trailing 单就是我"(place-at-open 之前的假设,那时同侧只可能一张),抓走 dd2 的 1.32 局部单;Binance 对每张 trailing 单硬报 activated → applyNativeTrailingDrawdown:2400 静默 return true,什么都没挂,活仓无 dd1 全平保护而日志+reconciler 都声称已覆盖;修法=findExistingFullTrailingOrder 与 hasMatchingNativeTrailingOrderForRule 两处兜底循环都用 claimedTrailingOrderIDsForPosition 跳过兄弟档已认领的 orderID(同 v1.16.8 招式,不加新阈值),兜底本身保留给真正无人认领的孤儿单;测试 TestFullTierFallbackSkipsSiblingClaimedOrder(线上真实数据+3 子例含回归护栏);上线验证 pid 2274258:SOL dd1 已补挂 2000001312879648(4.41)、ETH 部分成交后 dd1 按新仓位 0.081 正确重挂;并记一条排查陷阱:刚 arm 完的档位会连打 300s 的 🟠 arm cooldown,那是 v1.16.6 结构性兜底(cooldown 检查故意排在 armedFingerprints 分支之前),不是匹配失败** /**v1.16.11 该 bug 类第 8 次实例(2026-07-27,ATR 策略下 30% 部分止盈档按整仓挂单:WLD 应 114 挂了 380、CL 应 0.42 挂了 1.4,回调更紧会先触发→整仓平掉 runner 全没):根子在 resolveDrawdownRulesWithModes 每档从 result:=base(原始 config)起算、只在 mode=ai 时才采纳调用方值 → 全 manual 策略把调用方已 ATR 解析的规则整体还原成裸倍数 → 档位分配表把裸 ATR 倍数 3.0 存进语义为百分比的 MinProfitPct(日志 peak_trigger=3.00% 真值 4.2012%,updateDrawdownTierStates 也会提前转 tracking)→ getCumulativeCloseRatioByRule 精确匹配失败 → 兜底「取 min≤本规则的最高档」认领 dd1 累加其 100 并 clamp → close=30 算出 close=100;SOL/ETH 侥幸正常只因其解析后部分档 min 低于裸 3.0、兜底一条都没匹配上,"部分档 min 反而高于 dd1"就是本 bug 的外部指纹;修法=initDrawdownTiersForPosition 增 entryPrice 并在 mode 合并之后再跑 resolveDrawdownRulesATR(分配表恒为百分比)+ 身份匹配改用跨解析不变的 (StageName,CloseRatioPct) + 删掉会往上认领的数值兜底(匹配不到就返回本档比例,少保护一档远好过整仓被平,真实阶梯累加语义保留并有测试钉住);测试 trader/drawdown_tier_alloc_atr_test.go 3 例(claude-ct30 线上配置),反向验证报错即线上数字 ENTIRE position (380 units) instead of 114** 等) | 活跃 线上 v1.16.11(2026-07-27 05:53 UTC pid 2282277 md5 478e8ed8,回滚备份 nofx.bak_v11610_20260727_055310,提交 4399b72);上线验证:分配表日志已恒为百分比且与 arm 指纹一致(KAITO 8.60%↔8.5997、SPCX 3.29%↔3.2933、WLD 4.20% 原报 3.00%、CL 2.71%/2.43%),WLD 局部档已按 114 重挂(原 380)、CL 按 0.4 重挂(原 1.4),dd1 全平档仍 380/1.4;两张错量单 3778506109091213312/3778585660408365056 已撤;**v1.16.12 已编码待部署 md5 6d90f3c6**(该 bug 类第 9 次实例:computeDrawdownTierAllocations 的 allocatedPct 预算裁剪假设各档互斥,而 dd1 触发点 4.20% 低于部分档 5.60% 排序在前吃满 100 → 部分档 ratioPct=0 被 continue **整条丢出分配表**,线上日志"1 tiers"就是证据;后果:累加匹配不到/高水位不跟踪/managed 兜底永不执行该档/面板少一档;修法五处配套 = 全平档拿整仓且不占预算 + 累加时排除并存全平档(**关键陷阱**:档位加回来会让身份匹配成功,若仍累加全平档的 100 则 #8 从匹配路径复活,反向验证实测 cumulative=100% 1.40 vs 0.42) + supersede 循环禁止部分档注销全平档 + findRuleForTier 先按 StageName 身份匹配(TierIndex 指向内部排序切片,与调用方未排序切片错配) + resolveDrawdownRulesWithModes 空名改语义推断(原盖成 T2,arm 路径叫 partial_profit_lock,同一档两个名字);测试 4 例全反向验证,trader 全包 34.6s 绿;**教训:兜底 ⚠️ 日志即使结果正确也必须追因 —— 这次正确值完全来自 v1.16.11 兜底的保守方向,不是系统正常工作**);**v1.16.13 已提交待部署 md5 b92d92da 提交 b18ff54**(按用户"不行就 managed 一直陪跑,双保险,别搞接管"定性重构:原主循环 `if nativeTrailingHandled { continue }` 是**账户级接管**,managed 当轮全停摆;最严重后果=`accountReArmBreaker` tripped 分支谎报 allCovered 并把档位"交给"`applyExchangeFailedLocalMonitor`,而后者对 close>=100 的档**不下单也不注册执行器**,只写保护状态 → 交易所无单+managed 无执行器+面板 armed+回吐熔断器因 positionHasArmedProtection 放过回撤盈利仓,且 resetReArmFail 只在验证到覆盖时清零而覆盖需要一张永不下单的单 → **该仓位终身零保护**;改法 5 处+1 处回吐守卫 = 去掉账户级 continue(managed 始终评估,执行权只由每档 exchangeSideCoversDrawdownTier 判定,查有效性且读不到状态时 fail open)+ 门禁抑制时回滚 evaluateDrawdownTiers 已写的 executed(该函数先改状态再返回,不回滚则档位被永久过滤且 hasAllTiersCompleted 误判全退出)+ tripped 档覆盖判定直接 false + tripped 分支不再谎报覆盖也不重复补挂(对部分档会抖动)+ 无 allocs 老路径改查交易所单有效性不再信 armed DB 记录 + positionHasArmedProtection 对 *_exchange_failed_armed 要求存在档位分配;测试 E8/E9/E10 三端钉住,TestReArmBreaker_TripsAfter3Fails 按新语义更新;**教训:"双保险"vs"接管"在代码里的区别是闸门粒度 —— 账户级/仓位级 continue 一定是接管,只有下沉到"这一档此刻交易所侧是否真有效"并 fail open 才是陪跑**) |
| `binance-native-trailing.md` | BN 原生 trailing 修复（go-binance v2.8.9→2.8.10 参数键名 bug、algo 端点、close 缓存裸仓修复、遗留 issue 1/2） | 活跃 |
| `exit-timing-stop-research.md` | 退出侧研究(已逐K定论,未部署)：弱势/动量判据无增益(证伪)；快照仿真的2×ATR收紧止损被逐K回测证伪(claude上-45U最差)；**四trader逐K验证:无固定N×ATR能全正,系统现有swing结构close-confirm止损已是稳健最优,勿替换**；顺带发现Claude-R 15m止损偏紧可单独跟进 | 已定论 |

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
