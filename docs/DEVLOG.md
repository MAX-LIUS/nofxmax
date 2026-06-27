
- 阶段切换：`Protection AI Workflow` 主线阶段性收口，后续主线转向执行层真实问题：Drawdown 多档委托生命周期 + Break-even 实盘委托可观测性。

- 2026-04-20：完成 `nofxmax` Drawdown / Break-even / protection 执行层集中收口，并产出交付总结 `docs/PROTECTION_EXECUTION_DELIVERY_2026-04-20.md`。
  - 启动链路：修复启动期余额拉取阻塞、`start.sh` 非幂等重启、API query reload storm。
  - 基础保护：ladder / full / fallback 在执行前做可执行性校验；小仓位不可执行 ladder 会降级，不再无限重试交易所拒单。
  - Reconciler：single-tier ladder 必须按具体价格存在，不能被 break-even/fallback/旧 stop 冒充；unexpected order 改为按角色识别；OKX cleanup 优先按 tag 精确清理。
  - Drawdown：多档规则只取当前满足的最高利润阶段，同档多规则可共存，避免高利润阶段回头补低档。
  - Break-even：独立 overlay，不再破坏 ladder/full stop-loss；stable fingerprint 不重复 reapply。
  - 验证：本轮收口后 `go test ./...` 通过。后续新任务建议转入真实持仓验收、保护摘要可视化、fixture 产物取舍。

  - 本地建立 `~/agentic-coding/` 持久体系（memory / contracts / evidence / handoffs），把 coding 执行纪律从临时会话状态升级为持久方法。
  - 仓库新增 `docs/MODEL_RESILIENCE_AND_DELIVERY_CONTINUITY_CN.md`，明确后续默认采用：流程级容灾、subagent 子任务隔离、fallback 模型续航、文件化证据、少打断多收口。
  - 目标不是“换一个模型赌稳定”，而是让任务在模型波动时也能继续推进并最终一次性交付。
  - TaskFlow 负责：任务身份、当前步骤、等待态、子任务关联、可恢复状态。
  - Agentic Coding 负责：Objective / Acceptance / Non-goals / 最小改动 / 修前修后证据 / handoff 收口。
  - 后续复杂任务统一按 `Flow 卡片 → Contract → 最小改动 → 证据验证 → 文档记忆 → 提交` 执行，不再只依赖单轮聊天上下文硬顶。
  - 这次落地不是抽象建议，而是为了直接解决当前 `nofxmax` 上长期任务“断线后重摸索”的执行顽疾。
  - Strategy Studio 保存使用 `PUT /api/strategies/:id`，保存后重新 `GET /api/strategies` 刷新编辑态；前端 `ie()` 仅做展示层默认值补齐。
  - Trader 运行时**不会读取 active strategy 作为实时配置源**；真正使用的是 trader 记录里的 `strategy_id`，由 `store.Trader().GetFullConfig()` 优先按该 ID 载入 strategy；只有 `strategy_id` 为空时才 fallback 到 active/default strategy。
  - 启动 trader 时会 `RemoveTrader` 后重新 `LoadUserTradersFromStore`，因此**重启后会拿到数据库里 strategy_id 对应的最新配置**。
  - 由此判断：若 UI 中“策略页保存后刷新正常”，但真实运行/某 trader 页面仍表现异常，下一层应优先排查 **trader 绑定的 strategy_id 不是当前编辑那份**，而不是继续怀疑 protection merge。
- 2026-04-16：补了一个 API 可观测性缺口修复：`handleGetTraderConfig` 现在会返回 `strategy_name`（此前只回 `strategy_id`，但前端查看 trader 配置弹窗会尝试展示 `strategy_name`）。这个缺口会放大“看起来没绑对策略/没保存成功”的错觉，现已补齐并通过 `go test ./api/...`。
- 2026-04-16：继续下钻前端 trader 编辑链路，已确认两条主要更新路径都**显式保留/回传 `strategy_id`**：
  - `AITradersPage.handleSaveEditTrader()` 会把 modal 返回的 `data.strategy_id` 原样带入 `api.updateTrader()`。
  - `TraderDashboardPage.saveAIControls()` 会先 `getTraderConfig()` 再把 `current.strategy_id` 连同其他字段一并 PUT 回去。
  - 这意味着：常规“编辑 trader 配置”与“dashboard 调整 AI 控制项”两条路径本身，不像是把 strategy 绑定悄悄清空的根因。
- 2026-04-16：已定位并修复 protection UI“看起来回到手动”的真实根因：数据库里存在旧版 protection value 结构（如 `{"enabled":false}`），而当前前端/后端新结构期望 `{"mode":"ai|manual|disabled","value":...}`。旧数据被反序列化后，`mode` 为空，前端 normalize 会补成默认 `manual`，导致页面回填看起来像“AI 模式丢失/回到手动”。已在 `store.ProtectionValueSource.UnmarshalJSON` 增加旧结构兼容迁移：
  - `{"enabled":true,"value":x}` → `mode=manual,value=x`
  - `{"enabled":false}` → `mode=disabled,value=0`
  - 新旧结构现可并存读取；`go test ./store ./api/...` 已通过。
- 2026-04-16：继续补 UI 解释层，避免用户把 `enabled`、`global mode`、`TP/SL value mode` 混为一谈。在 `web/src/components/strategy/ProtectionEditor.tsx` 为 Full / Ladder 增加状态摘要与提示：当出现“整体模式 = AI，但执行开关仍关闭”时，明确提示“页面保留 AI 配置，但运行时不会实际挂保护单，直到启用执行开关”。前端测试 `npm test` 已通过。
- 2026-06-24：三交易员（claude/GPT/Claude-R）排查 + 修复 + 记忆固化，一次性提交并部署。
  - **持仓时间不显示（GPT/OKX）根因**：`/positions` 接口数据来自交易所实时接口，`auto_trader_decision.go` 的 `entry_time` 只取 DB 的 OPEN 记录；OKX 同步漂移时 DB 查不到→`entryTimeMs=0`→前端不显示。修复：DB 查不到时回退用交易所已返回的 `createdTime`（OKX/Bybit 有，Binance 无则保持 0）。
  - **peak 不准根因**：peak（`peakPnLCache`，key=symbol_side）纯内存、无持久化，容器重启即清零并从当前盈亏重新累计，导致已触发 ladder 的持仓 peak 显示很小。修复：新增 `peak_pnl_states` 表 + `SavePeakPnL/DeletePeakPnL/LoadPeakPnLForTrader`；`UpdatePeakPnL` 高点上移时落库、`ClearPeakPnLCache` 平仓删库；启动 `loadPeakPnLFromStore` 恢复，且只恢复当前仍 OPEN 的仓位（大小写不敏感）并清理 stale。
  - **批量误平根因**：4 次 `sync_absent_from_exchange` 全集中在 03:24 同一时刻，疑似 OKX `GetPositions` 瞬时空响应。`MarkOpenPositionsAbsentFromExchangeClosed` 无防空保护。修复：缺失仓位 ≥2 且=全部本地 OPEN 时判瞬时故障、跳过整批并告警；单个消失仍正常平。加 2 个回归测试。
  - **备用模型结论**：本次启动以来 endpoint fallback 0 次，三 trader 计费均只用主模型，主路无超时/重试。用户感觉“备用用得多”应发生在 04:13 重启前（日志已丢）。已替换 NovaI fallback key（写入 DB，明文 JSON 字段）。
  - **UI**：净盈亏（红绿正负色）移到持仓卡片右上、与盈亏百分比并排。
  - **CANCELED 疑问解答**：native_trailing 反复撤挂是设计正常（跟价移动）；full_tp/full_sl 撤挂主因是仓位数量变化后按剩余量重挂，非 bug。
  - **记忆固化**：新增 `.ai-memory/INDEX.md`（恢复主入口）+ `.ai-memory/communication-preferences.md`（中文沟通约定）；`CLAUDE.md` 顶部加"记忆与恢复协议"区块，使 compact/clear 后可从单一入口恢复原始记忆与固定开发管理方法（FUXI_WORKFLOW）。
  - **验证**：`go build ./...` ✅、`go test ./store/ ./trader/` ✅（含新守卫测试）、前端 `tsc` ✅。前端 `Ladder planned levels` 单测失败为 baseline 既有、非本次引入。
- 2026-06-24（续）：回吐控制阈值回测研究 + GivebackGuard L2 状态持久化。
  - **问题**：用户问"开仓不足 3% 的盈利/成本回吐是否应同样控制，控制与不控制实际表现如何"。
  - **方法**：扩展 `cmd/gbsim`，新增 `-l1study` flag + `l1StudyGrid()`，单独扫 `L1MinPeakPct ∈ {0.5,1.0,1.5,2.0,3.0,5.0}`（其余 L1 旋钮锁定 live 值 gb40 cl50），两族：L1-only 与 L1+L2(live winner)。修了 `describe()` 的 `%.0f→%.1f`（之前 0.5/1.5 被截断）。
  - **三套独立数据一致结论**：阈值降到 3% 以下，PnL 单调崩塌——
    - robust-EMA 12mo/8币 1075笔：baseline 1379；mp3=1193、mp2=619、mp1.5=-5、mp1.0=-164（转负）。
    - DB-real claude 182笔(+L2)：baseline 47.9；mp5=77.1、mp3=73.5、mp2=66.0、mp1=56.6、mp0.5=53.8（单调降）。
    - breakout 12mo/8币 2670笔：baseline 6760；mp3=6826（略超 baseline）、mp2=5281、mp1=3078（腰斩）。
  - **机制**：阈值越低 trim 次数越爆炸（breakout: mp3=1475→mp0.5=3247），在小浮盈处反复砍仓=churn，把还没走出来的赢单提前杀死；回撤虽降但 PnL 代价不成比例（mp0.5 降 37% DD 却砍 48% PnL）；胜率升是假象。
  - **结论**：3% 是真实经济分界，不对 sub-3% 回吐做额外控制，维持现状。
  - **顺带做（用户已认可）GivebackGuard L2 持久化**：此前 `gbPortfolioPeakUnreal/gbL2FiredAtPeak/gbL1FiredAtPeak` 纯内存，重启清零→可能首个反转就误触发 L2 或丢 latch 误砍赢单。新增 `giveback_guard_states` 表（每 trader 一行：组合浮盈高水位 + L2 latch + L1 各仓位 ratchet JSON）+ `SaveGivebackGuardState/LoadGivebackGuardState`；`runGivebackGuard` 每轮（默认 60s）落库一次；启动 `loadGivebackGuardStateFromStore` 恢复并按当前 OPEN 仓位裁剪 L1。加 3 个 round-trip 测试。
  - **验证**：`go build ./...` ✅、`go test ./store/ ./trader/` ✅（含新 `GivebackGuardState` 测试）。
- 2026-06-24（续2）：举一反三排查"重启即丢"的内存态，修复入场冷却风控漏洞。
  - **方法**：通盘过了 `AutoTrader` 所有内存字段，按"重启丢不丢 / 丢了有无实际影响"分类。
  - **已自愈无需动**：`drawdownAIRules`（开仓决策懒加载）、`drawdownTierAllocs`（按剩余仓量回推执行态）、`protectionState/breakEvenState/drawdownState`（dynamic protection 已恢复）、`positionFirstSeenTime`（已加交易所 createdTime 回退）、`nativeTrailingArmTime/immediateTrailingIDs`（reconciler 重建）、单周期临时态。
  - **发现真实漏洞**：入场冷却 `cooldownManager` 纯内存，且只在"活仓→平仓"转变时设置；重启后 `positionFirstSeenTime` 为空、该转变永不再触发，刚止损的币种冷却被清零→可立即重入，绕过连亏冷却（最高 4x）风控。
  - **修复**：新增 `restoreEntryCooldownsFromStore`，启动时从 DB 最近平仓记录重算 `until = exitTime + duration×连亏倍数`，仍在未来的重新武装；数据全来自 DB（`GetRecentTrades/GetConsecutiveLossCount/ExitTime`），无需新表。加 `RestoreCooldown` 原语 + 2 单测。每币种只看最新一笔，最新盈利即视为冷却已过期。
  - **确认非真实风控不动**：`stopUntil`（从不赋未来值）、`dailyPnL`（非 grid 路径只重置为 0）是惰性死字段。
  - **验证**：`go build ./...` ✅、`go test ./store/ ./trader/` ✅（含新 `RestoreCooldown` 测试）。
- 2026-06-24（续3）：保护系统对利润影响的系统回测——回吐控制负面影响 + DD/BE/TP 拆解 + 趋势自适应平仓 + %vs ATR。
  - **新增 gbsim 三模式**（commit 已落）：`-liveconfig`（线上配置 PnL 代价拆解）、`-adaptive`（ADX 门控趋势自适应平仓，trend×chop 双向）、`-ablation`（DD/BE/TP/SL 逐层敲除）、`-unitcompare`（%固定 vs ATR）。backtest 包新增 `ablation.go/unit_compare.go/analysis_entry.go`。
  - **回吐控制(GivebackGuard)负面影响**：机械趋势 regime 损利明显——robust-EMA baseline≈1800、flat 守卫≈1096（损 ~39%）；breakout 损 7%。真实 AI 交易员(claude 185笔 48→74、GPT 203笔 +8.7%)上守卫反而净增利。负面影响与"标的趋势延续强度"正相关。
  - **趋势自适应平仓**：方向验证为"让趋势跑"——强趋势(ADX≥thr)少砍(cT25)、震荡多砍(cC70)最优。robust-EMA 同 run 内 flat 1096→自适应 best 1252(+14%)、MaxDD 仅 +3%。但真实 claude 上 flat(74) > 自适应 best(64)。结论：自适应只在机械趋势有用,线上 AI 不开。
  - **DD/BE/TP 拆解**(真实 claude 187笔, guard off): FULL=15.32; -DD ΔPnL≈0(几乎惰性); -BE +7.58; -TP +14.04; SL-only=46.72(+31)。即在真实 AI 单上,TP 阶梯/BE 对利润是净抑制,DD 近乎无效;SL 是唯一关键护栏。GPT 同向:-TP +3.73、SL-only PnL 翻倍。
  - **%固定 vs ATR**(真实 claude 187笔): %=15.32 / ATR-tight=41.43(DD 还从 62→42) / ATR-wide=47.50。ATR 全面碾压百分比。GPT 上两者接近(ATR-mid/wide 略好)。结论:ATR 模式在真实 AI 单上利润显著更优,尤其 claude。
  - **验证**：`go build ./...` ✅、`go test ./trader/backtest/` ✅。所有结论为回测证据,未改任何线上参数(需用户确认)。
- 2026-06-24（续4）：完整参数寻优 + 平仓比例/档位 + 关闭不利项 + 样本外验证。
  - **新增 gbsim**：`-optimize`（分阶段 ATR 寻优:SL/TP/BE/DD 的距离×平仓比例×档位数×关闭不利层,按抗回撤分 PnL−λ·MaxDD 排序;每阶段锁定上阶段赢家,逐维边际可见）、`-holdout`（时间序列 train/test 切分,train 上寻优、test 上验证,抓过拟合)。backtest 新增 `optimize.go/holdout.go`。
  - **真实 claude 188笔寻优**：%baseline 4.78 → 优化 63.70(13x),MaxDD 62→47。结构=紧 SL(2.0 ATR)+单档 TP@65%+轻 BE+边际 DD。λ=0.15/0.3/0.5 结果一致(非刀尖)。
  - **真实 GPT 203笔寻优**：%baseline 9.44 → 16.56(1.75x)。结构=SL3.0、**TP 全关**、早 BE@65%、DD 关。
  - **机械 EMA 1077笔**:SL5.0 only、全关保护 → 12033 但胜率 12%、MaxDD 4077(纯趋势签名,证明方向=现行 TP/BE 过激,但其"零保护"答案是 regime 特例、回撤不可接受)。
  - **样本外验证(关键)**:claude train131→test57:优化参数 OOS PnL 39.2/DD 30.4 vs 现行% 31.3/43.6 → +25% 利润、−30% 回撤,**真实可泛化非过拟合**。GPT test 窗口对所有配置都亏,但优化仍亏最少(−12.9 vs −16.3)。
  - **跨交易员一致结构**:紧 SL(2.0–2.5 ATR)+ TP 阶梯关/最简 + 轻早 BE + DD 关或边际。现行"2 档 % TP3/6 + BE2/4"被判为过度工程化、提前切赢单。
  - **验证**:`go build ./...` ✅、`go test ./trader/backtest/` ✅。仍未改线上参数(待用户确认)。
- 2026-06-24（续5）：全市场普适参数寻优 + 样本外验证(关键反转结论)。
  - **目标**:不分交易员(市场统一,只币种不同),从 OKX 拉 23 币种×18 个月×combined(EMA+breakout)信号≈1.6 万 entry,找普适保护参数,严防振荡市利润折损。
  - **新增 gbsim**:`-signal combined`(EMA∪breakout 去重,跨趋势+震荡)、`-rank`(预设稳健候选排名,无拟合即可泛化)、`-holdout` 支持 robust(按 entry_time 排序后时间切分)。backtest 新增 `candidate_rank.go`,`robust.go`/`optimize.go` 加宽 ATR 网格。
  - **关键反转 — 贪婪寻优会过拟合**:全市场样本上 `-optimize` 选出紧 SL=1.5,样本外(train70%→test30%)直接 **−18384**,而 %baseline/ATR-wide 同窗口稳定 **+6200**。λ 加到 1.0 仍选 SL=1.5、仍崩。证明:激进逐维寻优在单一训练窗口必过拟合,任何 λ 救不了。
  - **方法论转向**:普适参数只能靠"预设稳健候选 + 样本外排名",不能靠 in-sample 峰值。
  - **候选样本外排名结论**:
    - 机械样本 OOS(趋势偏置):%live 按抗回撤分胜出,ATR-wide PnL 更高但回撤过大。
    - 真实 claude OOS:ATR-wide 全面≥%live,但仅 ~24% 优势(非寻优谎称的 13x)。
    - 真实 claude 全样本:ATR-wide 2tp 45 vs %live 21(~2x)。
    - 真实 GPT OOS(全亏窗口):ATR 各档亏得都比 %live 少。
  - **稳健结论**:真实 AI 单上 ATR 模式始终≥%live、从不更差;保留完整 TP 阶梯(2tp)是振荡市利润防护的关键(chop-defensive 早 TP 反而 PnL 代价过大)。激进"紧 SL+砍 TP"是过拟合陷阱。
  - **给 claude 的普适方案**:`atr-wide 2tp`(SL=4.5ATR,TP1=2.0@35%,TP2=7.0@25%,BE=2.0/0.3@50%,DD=arm7/gb40%@45%),跨全部样本最稳、真实单上 ~2x %live、从不更差。
  - **验证**:`go build ./...` ✅、`go test ./trader/backtest/` ✅。仍未改线上参数(待用户确认具体落地)。

## 续6 — claude 实盘切换到 `atr-wide 2tp`(2026-06-24)
- **目标**:把回测稳健胜出的 `atr-wide 2tp` 落到 claude 实盘,与 AI 分析完美结合,完善策略管理。
- **落地对象**:策略 `6fd686fe-df0f-4d30-98cf-5f93e0a89a0c`(trader `4801de05_..._claude_...`,is_running=1)。其余两个交易员(GPT/Claude-R)用不同策略,不受影响。
- **改动(仅 `$.protection` + `$.atr_protection` 两段,其余字段 diff 校验完全一致)**:
  - `atr_protection`:`enabled=true`、`multiple_mode=fixed`、各维 mode=fixed、`stop_loss_atr=4.5`、`take_profit_1_atr=2.0`、`take_profit_2_atr=7.0`、`break_even_1_atr=2.0`、`drawdown_min_profit_atr=7.0`、`min_eff_pct=0.5`、`max_eff_pct=25`、`timeframe=1h`、`atr_period=14`。
  - `ladder_tp_sl.rules`:3 档→2 档(TP1 2.0%@35% + SL 4.5%@100%;TP2 7.0%@25%),与回测平仓比例一致。
  - `break_even_stop`:2 档→1 档(trigger 2.0、offset 0.3、close 50%)。
  - `drawdown_take_profit.rules`:保留单条 runner_exit(min_profit 7.0、max_dd 40%、close 45%)。
  - `giveback_guard` L1+L2 **保持启用**(前结论:真实 AI 单上净正向)。
- **与 AI 的结合方式(结构性"完美匹配")**:AI 仍独立决定方向/入场/杠杆;ATR overlay 只把保护距离按各币种当下波动率自动缩放(effPct=M×ATR/entry),AI 给什么仓位就配什么宽度的保护。fixed 模式下 4.5/2.0/7.0 这组经回测验证的倍数对所有币种统一生效。
- **策略管理校验**:确认 `trader_protection_configs` 表为遗留死表(`Get/SaveUnifiedProtectionConfig` 无任何 live 调用方,仅 migration 定义),StrategyConfig 经 `resolveATRProtection` 是唯一真源,本次改动权威。
- **部署**:DB 写入前已备份(`/tmp/claude_strategy_backup/`),写后 readback diff 校验一致 + `wal_checkpoint(TRUNCATE)`;`docker restart nofx-trading` 重载(三交易员内存态已持久化,boot 时 giveback/cooldown/peak 全部正确恢复:claude portfolioPeak=12.32 l2Latch=12.32)。
- **实盘验证(boot 04:56 后)**:16 个持仓全部触发 `🎯 ATR-protection applied`,按各币 ATR 缩放(BTC ATR=612 vs WLD ATR=0.014 各自适配);reconciler 全部 `state=protected verified=true missingSL/TP=false`;SOLUSDT 仅一次重构期瞬态告警(3档→2档清理旧 TP),04:59 自愈。无 panic/解析错误。


## 续7 — sz=0 平仓修复 + 权益回撤熔断 (L3)，2026-06-24

### 1. sz error (51000) 平仓 bug 修复
- **根因**:部分平仓不足 1 手(如 TRUMPUSDT 残余 0.04 币 / ctVal 0.1 = 0.4 张,lotSz≥1),`formatSize(0.4)→"0"`,OKX 拒单 sCode 51000;DD 分批砍仓循环重试 176 次。
- **修复**:`trader/okx/trader.go` 新增 `resolveCloseSize(wantContracts, fullContracts, inst)`:① 不足 1 手的部分平仓顶到 1 手(封顶剩余全量);② 整仓本身<1 手判 `POSITION_DUST`(Skip,告诉上层停止重试);③ 格式化后 sz 非正一律 Skip。接入 `closeShortWithTag`/`closeLongWithTag`。
- `trader/auto_trader_risk.go` 新增 `closeOrderSkipped()`,识别 `NO_POSITION/SKIPPED/POSITION_DUST`,尘埃仓打真实告警、返回 nil 使 tier 循环正常停止(终结重试风暴),不再误报"✅平仓成功 orderId:<nil>"。
- 测试:`trader/okx/close_size_test.go` 7 用例全 PASS。

### 2. 权益回撤熔断 L3(整体权益从峰值回撤分批砍仓)
- **需求**:浮赢回吐(维度a,现有 L2 已覆盖)+ 整体权益回撤(维度b,新增 L3),任一触发即分批砍仓;参数进策略 UI 可调;预设由真实数据回测标定。
- **实盘回撤实测(近3天)**:claude 权益峰 299.61→谷 247.55 = **-17.4%**(浮盈 +24.68→-21.01);GPT 70.96→59.01 = **-16.8%**;Claude-R 53.46→39.41 = **-26.3%**。证实用户关切。
- **L3 设计**:追踪账户总权益(本金+已实现+浮盈)高水位;回撤触及分档阈值→按该档比例砍**每一个**持仓(含亏损仓,因整体回撤=趋势变/账户失血)。逐档深档优先(快速暴跌只触发最深一档),每档一 episode 触发一次,权益创新高后全部重新武装。区别于 L2(只削赢家锁利润)。
- **gbsim 真实回测交叉验证**(`-l3` 模式,真实 OKX 历史,三交易员独立数据):
  - claude in-sample 最优 `8→33 18→66`(+10.24 PnL)在 GPT 上**转负**(-0.92)→过拟合,弃用。
  - 跨三交易员稳健的预设:**`6%→30% / 12%→50% / 20%→75%`**:DD cut 在三者全为正(claude +0.27, GPT +3.31, ClaudeR +1.55),PnL 在 2/3 提升(claude +5.29, GPT +3.52),ClaudeR -0.64(DD 削减抵偿)。触发次数合理(60天 6–30 次)。
  - hair-trigger `4→25 8→40 14→60` 削 DD 最多但 claude 60天触发 61 次、ClaudeR DD 转负→过敏,弃用。
- **代码**:
  - sim:`trader/backtest/portfolio_sim.go` 加 `GuardParams.L3Enabled/StartCapital/L3Tiers` + `applyEquityBreaker()`;`equity_breaker_sweep.go`(L3Grid + Sweep);`cmd/gbsim` `-l3 -capital` 旗标。单测 `equity_breaker_test.go` 4 用例 PASS。
  - live:`store/strategy.go` `GivebackGuardConfig.L3Enabled/L3Tiers`;`store/migration_unified_protection.go` 加 `equity_peak/l3_fired_at_peak/l3_tier_fired_json` 列(`columnExists` 幂等,SQLite 无 ADD COLUMN IF NOT EXISTS);`trader/giveback_guard.go` `gbApplyL3()`;ratchet 持久化/恢复扩展;round-trip 测试 PASS。
  - UI:`web/src/types/strategy.ts` 加 `GivebackGuardConfig/GivebackL3TierConfig`;`ProtectionEditor.tsx` 新增 L3 编辑区(开关/dry-run/分档增删改),预设 6/12/20→30/50/75。tsc + vitest 通过。
- **状态**:全部本地构建 + 测试通过。**未部署实盘**(实盘下单/配置变更需用户确认后再上线)。

## 续8 — L3 逆势优先 + 提前熔断 + 反向回抽过滤，2026-06-24

### 实盘回撤根因(真实平仓数据)
近5天 claude 权益峰 299.61→谷 228.94(**-23.6%**)。按方向拆分亏损平仓:LONG -97.05(33笔) vs SHORT -21.22(11笔)——满仓多头撞下跌。具体:① 单一 FILUSDT 反复逆势抄底,06-24 止损 -16.5、06-25 止损 -23.91、13:58 又开 259.6 多单(第三轮),光这一个币吃掉 full_sl 总亏(-60)的三分之二;② 单仓名义 ~310 > 账户权益 ~250(集中度>100%);③ SL 偏宽(到 -7.7% 才止)。L1/L2 只保护浮盈回吐(峰值才 12U),对"满仓单边被趋势带走"无能为力——正是 L3 要补的洞。

### L3 设计升级(用户两点要求)
1. **砍逆势优先、留顺势**:逆/顺势按 **pnl 速率方向**判定(非盈亏正负)。速率<-eps=逆势→按档位足额砍;速率≥-eps=顺势→只砍 档位×L3TrendKeepMult(0=全留作第二梯队)。盈利但掉头的仓=逆势照砍;亏损但回升的仓=顺势保留。
2. **提前熔断 + 反向回抽过滤**:权益速率(%/K线)连续 L3EarlyConfirmBars 根为负、且回撤过 L3EarlyFloorPct 才触发提前砍,单根插针不触发(防 V 形回抽砍在地板);L3FireCooldownBars 控制两次触发间隔。

### gbsim 持仓级 trace 验证(真实,claude 30天)
- 无回抽过滤:9 次触发,PnL +3.06,MaxDD 77.08(DDcut 3.53)——其中 #6/#7 在 DD 仅 3% 时被单根插针触发,只砍一点点(过敏)。
- 加回抽过滤(confirm=2 cd=3):**7 次触发全部 [TIER]**(无过敏提前触发),PnL +2.87(持平),**MaxDD 72.43(DDcut 8.18,更好)**。trace 确认逆势/顺势判定正确(ETH 浮盈+1.82% 但速率-0.45→判逆势砍;SPCX 浮盈+25%速率+1.9→判顺势只砍9%);FIL 在 -2% 就被分批砍清,远早于实盘 -16.5/-23.91 的 full_sl。
- 跨三交易员交叉验证(均30天,加回抽过滤):claude PnL -6.04→+2.87 / DD 80.61→72.43;GPT -4.22→-2.67 / 24.64→24.07;ClaudeR -11.32→-10.22 / 27.83→24.31。**三者 PnL 与 DD 均改善,无一受损**。

### 生产预设
`k30 v3/f2 w6 c2 cd3`:tiers 6/12/20→30/50/75、顺势保留(砍×0.3)、速率窗口6、提前熔断权益跌3%/K线、回撤下限2%、插针过滤连续2根、冷却3根。

### 代码
- sim:`portfolio_sim.go` 加 simPos 速率采样(recordPnl/pnlVelocity)、GuardParams 速率+回抽过滤字段、guardState 负速率streak/冷却计数、applyEquityBreaker 逆势分类+提前触发+回抽过滤;trace(L3FireEvent/L3FirePos/RunPortfolioSimTrace);`equity_breaker_sweep.go` L3WinnerConfig + 变体网格;`cmd/gbsim` `-l3`/`-l3trace`/`-capital`。单测 9 个全 PASS。
- live:`store/strategy.go` GivebackGuardConfig 加 7 个速率/回抽字段;`trader/giveback_guard.go` gbApplyL3 重写为速率分类版(per-symbol 速率环 gbPnlHist、equity 速率streak、冷却),mirror sim;AutoTrader 加对应状态字段。
- UI:`ProtectionEditor.tsx` L3 区加 6 个速率/回抽参数输入 + 预设。tsc + vitest 通过。
- 全量 `go build ./...` + `go test`(store/trader/backtest/okx/api)全绿。

### 状态
代码完成 + 全测试通过。**尚未部署实盘**。建议:先 dry_run=true 上线观察触发点,再切实盘。

### 上线部署(2026-06-26 00:03 UTC)
- 三个交易员策略均启用 L3(claude 6fd686fe / GPT 4658ad10 / Claude-R 85b160fb),用 `json_patch` 合并 L3 字段,保留原 L1/L2;readback 校验一致;`wal_checkpoint(TRUNCATE)` 落盘。配置前先备份到 `/tmp/l3_deploy_backup/`。
- 生产预设(实盘,dry_run=false):tiers 6/12/20→30/50/75、顺势保留×0.3、速率窗口6、提前熔断3%/K线、回撤下限2%、插针过滤连续2根、冷却3根。
- **1h bar-clock 修正(关键)**:live 监控每 15–20s 跑一次,但 L3 速率/streak/冷却逻辑按 1h bar 推进(gbL3BarMs=3600000),与回测的 1h bar 单位一致;tier 回撤阈值检查仍每 tick 跑(快速暴跌即时响应)。避免参数被 60× 误读。
- `docker compose build nofx` + `up -d`:容器 healthy。boot 日志确认新二进制运行(restore 行含 `equityPeak=/l3Latch=` 新字段),三交易员 GivebackGuard 状态正确恢复,equityPeak=0(首次 L3 部署,从现在向前建峰),无 panic / 无配置解析错误。监控 15s/15s/20s 正常起跑。

## 续9 — L3 滚动基线模式重构 + 7漏洞修复 + 全trader实盘，2026-06-26

### 背景:推翻续7/续8 的多档+锁设计
用户三点批评驱动重构:(1) 样本太小(仅30天3trader);(2) 逻辑漏洞没穷举;(3) keep_mult=0.3 砍顺势无数据支撑。
真实回测打脸旧预设:`k30 v3/f2 w6` 在 claude 30天是**最差档之一**(51次砍仅削DD 3.53、PnL-8.66)。且 keep_mult 跨trader无稳健最优值(claude偏好keep0、claude-R偏好flat,方向相反)——本身就是过拟合信号。

### 滚动基线模式(用户方案,取代多档+锁)
单条滚动参考权益 `rollRef`:每当权益较 rollRef 回撤 `L3RollDropPct%` 就砍仓并把 rollRef 重置为当前权益。无档、无 latch、无 re-arm 高水位门(后者正是续8"回血未创新高"空洞的根源)。两种砍仓策略:
- `L3RollCounterOnly=true`:逆势仓(速率<-eps)全砍、顺势仓砍 `CutPct×TrendKeepMult`(留 runner 防V反)。
- `false`:全仓砍 `CutPct%`(按当前剩余比例,指数去杠杆)。

### 穷举12种意外情形,7个真漏洞已修
① whipsaw 反复砍 → cooldown + `L3RollReArmPct` 重新武装(触发数从几百降到40-58)。
② gap 欠保护 → `L3RollScaleCap` 跌幅缩放(深跌砍更多,带上限)。
④ 新仓无速率历史被误判顺势 → `classifyCounter` 用 PnL 符号兜底。
⑦ 同tick L1/L2/L3 双砍 → L3 先跑,触发则抑制 L1/L2。
⑤ 重启失忆 → `roll_ref`/`roll_arm_floor` 落库 + 启动恢复(实战已验证重启后恢复成 236.84 而非归零)。
⑥ 子手数砍单被拒 → 复用已修的 `resolveCloseSize`。
另揪出**真bug**:`addExit(frac)` 把 frac 当原始仓位的绝对比例(砍3次清零),改成砍**当前剩余**的比例(指数去杠杆,永远留runner)。这才符合用户"第二梯队继续跑"本意。
评估后忽略:⑧空仓空转(无害)、⑨盈利回吐也砍(保利特性)、⑪单仓硬SL抢跑(非L3职责,V反不可预测)、⑩基线随新高抬升(优点,根治续8空洞)。

### 大样本回测(接通 robust 长周期多币种)
`-l3 -robust` 接通 6-12月、8币种、趋势+突破双信号(数千 entries 样本外)。三行情画像:
- 崩盘(claude真实30天):修bug+治理后 `ct+k30/k50` 升顶,PnL+32~34、DD削38、仅触发40-58次。
- 小回撤(claude-R真实30天):`ct+k30/k50` 也排前列,**跨trader一致**(过拟合解除)。
- 牛市(robust):所有砍仓 PnLcost 全为正(都在少赚)。
**核心洞察:L3 是保险,牛市是保费、崩盘是赔付。选参数必须用崩盘样本,不能用牛市样本。** robust 绝对数字不可信(固定1000notional/不受本金约束),仅用于相对排名;真正可信的是 claude 真实崩盘序列。
方法论教训:一度误用 stale 二进制报数,已纠正;所有结论用新二进制(含指数去杠杆+治理)实测。

### 代码
- `trader/backtest/portfolio_sim.go`:GuardParams 加 `L3Mode/L3RollDropPct/L3RollCutPct/L3RollCounterOnly/L3RollScaleCap/L3RollReArmPct`;guardState 加 `rollRef/rollInit/rollArmFloor`;`applyRollingBreaker` + `classifyCounter`;sim 循环 L3 先跑抑制 L1/L2(⑦)。
- `trader/backtest/equity_breaker_sweep.go`:`L3RollingGrid`(drop×cut×keep×治理变体)、`SweepEquityBreakerRobust`、`mkRoll`。
- `cmd/gbsim/main.go`:`-l3 -robust` 通路、rolling 变体渲染。
- `store/strategy.go`:GivebackGuardConfig 加 6 个 rolling 字段。
- `store/migration_unified_protection.go`:幂等 ALTER `roll_ref`/`roll_arm_floor`;GivebackGuardState + Save/Load 扩展。
- `trader/giveback_guard.go`:`gbApplyL3Rolling`(1h bar-clock、velocity采样、缩放、cooldown、rearm、新仓兜底)、`gbApplyL3` 加 rolling 分发。
- `trader/auto_trader.go`:AutoTrader 加 `gbRollRef/gbRollArmFloor`、persist/load 扩展。
- `web/`:types + ProtectionEditor 加「模式」选择器(滚动基线/分档阶梯)+ rolling 编辑面板。
- 测试:`rolling_breaker_test.go` 6个新测试(阈值/指数去杠杆/缩放/whipsaw-rearm/新仓兜底/逆势全砍顺势保留)、store round-trip 加 RollRef/RollArmFloor。全绿(Go全过、tsc过、131前端测试过;2个失败为改动前既有的无关ladder测试)。

### 生产预设(实盘,dry_run=false)
`rolling -5%/cut100% ct+k0.3`:每跌5%、逆势全砍+顺势砍30%×keepMult、冷却3根1h K线、回血3%重新武装、跌幅缩放关、速率窗口6。

### 上线部署(2026-06-26 03:55 UTC,全trader实盘)
- 流程:先 dry_run=true 部署验证 rollRef seed 落库(236.84/57.08/41.99)→ 用户确认「直接上线」→ dry_run=false → 重启 nofx。
- 三策略(claude 6fd686fe / GPT 4658ad10 / Claude-R 85b160fb)json_set 写入 rolling 预设,readback 校验一致,wal_checkpoint 落盘。配置备份 `/app/data/strategies_giveback_backup_20260626_031638.bak`。
- **实战验证漏洞⑤修复**:重启后 rollRef 从持久化恢复成 236.84/57.08/41.99(非归零),滚动基线扛过重启。
- 容器 healthy、无 panic、无配置错误、protection reconciler 正常 tick。尚未触发(权益未跌穿5%,正常待命)。
- 回滚:改 dry_run 或切回 tier 模式均一条命令;备份文件在上述路径。
- 待办:盯首次 `[GivebackGuard L3-ROLL]` 触发日志,核对砍仓/分类与回测一致性。

### 续9b — 冷却盲区修复(cooldown→0,re-arm 独立防 whipsaw),2026-06-26 11:10 UTC
用户指出冷却的致命盲区:逆势全砍后留下的顺势 runner 在插针/级联崩盘里会反转成逆势,但冷却 3 根 1h K线锁手 3 小时 → 无法及时再砍 → 大亏。
**线上实证**:claude-R 上线后首次触发 `drop=15.6%`(应在 5% 就触发),因 `gbBarsSinceL3Fire` 从 0 起、冷却锁住前 3 根 bar,权益一路跌到 -15.6% 才砍。这正是用户描述的"冷却放任回撤跑"。
**根因辨析**:冷却=时间闸门(锁 N 根 bar,不管行情),re-arm=恢复闸门(只在权益回血 ReArmPct% 后放松)。崩盘持续下跌时权益永远回不到 arm floor,rollRef 钉住,下次再跌 5% 立即触发并把反转的 runner 重新分类全砍——**re-arm 根本不挡级联砍仓,冷却才挡**。
**回测验证**:含今日崩盘的快照上,cd=0/3/6 在 ra=3 下结果完全相同(PnL 33.67/DD削5.42/66次),且纯无冷却配置霸占前12名(PnL+55/DD削19)。冷却从不帮忙、只在崩盘锁手。
**修复**:三策略 `l3_fire_cooldown_bars` 3→0(保留 `l3_roll_rearm_pct=3`),重启加载,rollRef 状态扛过重启。UI 默认同步改 0。新增单测 `TestRolling_ReArmDoesNotBlockCascade`(证明 re-arm 不挡级联)+ 保留 `TestRolling_ReArmBlocksWhipsaw`(证明 re-arm 仍防 whipsaw),全绿。
**线上保护实证(7h,4轮触发)**:逆势 COUNTER 全砍 25 次、顺势 trend 轻砍 8 次,分类/比例/指数去杠杆/re-arm 全部按设计执行,保护真实生效。

