# 交易系统开平仓逻辑与控制参数全图 (System Control Map)

> 目的：把系统的每一个控制点按"控制层"归类，而非画流程图。任何人（含未来的 Agent）
> 想新增一条规则时，先判断它属于哪一层，放进对应层，避免 Entry Gate 无限膨胀、
> 风控逻辑相互纠缠。每层标注它的**改善来源**（Information / Leverage / Execution /
> Cost / Risk），以便日后追溯"为什么有效"。
>
> 状态基准：dev 分支 + 工作区未提交改动（p85 notional cap）。以代码为准，本文档随代码更新。

---

## 0. 七个控制层（自上而下）

```
第1层  AI Decision          模型产生意图（方向、入场、SL/TP、信心、regime）
   ↓
第2层  Permission / Kill     权限总闸：是否允许 AI 开/平/止损/止盈 + safeMode
   ↓
第3层  Entry Gate           入场质量门禁（3 段，硬门阻断 + 软门扣分→size 乘子）
   ↓
第4层  Risk Budget          风险预算：把意图转成"下多大注"（sizing 管线）
   ↓
第5层  Exchange Constraint  交易所约束：最小下单额、可负担性、杠杆、保证金
   ↓
第6层  Position Protection   在场保护：阶梯止盈、保本、追踪止盈（挂交易所侧）
   ↓
第7层  Exit Logic           离场逻辑：时间止损、最大持仓、追踪回撤、AI 平仓
```

改善来源图例：
- **[INFO]** Information — 靠更好的信息/预测产生 edge
- **[LEV]**  Leverage Allocation — 靠更合理的仓位/风险预算，不依赖预测
- **[EXE]**  Execution — 靠更好的成交（滑点、maker/taker）
- **[COST]** Cost — 靠降低费用
- **[RISK]** Risk Control — 靠削尾/限制单笔风险

---

## 第1层 AI Decision（意图生成） — 改善来源 [INFO]

AI 每个扫描周期产出一组 `kernel.Decision`，字段含：`Action`（open_long/open_short/
close_long/close_short/hold/wait）、`Symbol`、`Confidence`、`Regime`、`SetupType`、
`TriggerType`、`EntryProtection`（含 RiskReward: Entry/Invalidation/FirstTarget、
Net/GrossEstimatedRR）、`ProtectionPlan`（LadderRules）、`PositionSizeUSD`、`Leverage`。

决策进入执行前先 **按优先级排序**（`sortDecisionsByPriority`）：

| 优先级 | Action | 理由 |
|---|---|---|
| 1（最高） | close_long / close_short | 先平仓，释放保证金供后续开仓 |
| 2 | open_long / open_short | 后开仓 |
| 3 | hold / wait | 最低 |
| 999 | 未知 | 末尾 |

> 这一层是唯一的 [INFO] 层。全部研究已证明：方向预测≈掷硬币（48-51%），
> 所以 **不要再往这一层堆预测因子**。edge 在第 4/6/7 层的风险预算与削尾。

---

## 第2层 Permission / Kill（权限总闸） — [RISK]

在任何执行前过滤决策。注意：**不存在"权益地板 kill-switch"**，真实的闸是权限位：

| 控制 | 作用 | 触发后 |
|---|---|---|
| `isRunning` | 交易器是否运行 | false → 整个周期中止 |
| `safeMode` | 保护安全模式 | 监控/对账继续，开平由下面权限位决定 |
| `GetAllowAIOpen()` | 允许 AI 开仓 | false → 丢弃所有 open_long/open_short |
| `GetAllowAITakeProfit()` | 允许 AI 止盈平仓 | false → 丢弃非止损的 close |
| `GetAllowAIStopClose()` | 允许 AI 止损平仓 | false → 丢弃 IsStopLoss 的 close |
| `GetAIStopMinLossPct()` | AI 止损最小亏损门槛 | 当前 pnl% > -门槛 → 丢弃该止损（防过早砍） |

> 交易所原生保护（挂单 SL/TP）与代码保护（第 6/7 层）**不受** AI 权限位影响，始终生效。

---

## 第3层 Entry Gate（入场质量门禁） — [INFO]+[RISK]

`evaluateEntryGate`，3 段串行。硬门（`Enforced=true`）失败即阻断并置 `SizeMultiplier=0`；
软门（`Enforced=false`）失败仅按 `Penalty` 扣分，汇总成 score → size 乘子。

**评分→size 乘子**（`scoreToSizeMultiplier`）：score≥75 → 1.0；60-75 → 线性 0.5~1.0；<60 → 0.3。

### Stage 1 Market State（市场状态）
| 检查 | 类型 | 条件 |
|---|---|---|
| trigger_type 有效性 | 硬 | 必须是 6 类确认触发之一 |
| regime 允许列表 | 硬 | regime 在 AllowedRegimes 内 |
| funding_rate | 硬 | \|funding\| ≤ MaxFundingRateAbs |
| ATR 波动 | 硬 | atr14% ≤ MaxATR14Pct |
| trend 对齐 | 硬 | 方向不得逆 regime（逆势硬阻断，见下方注） |
| slow_bull_stack_short | 硬(opt-in) | 1h price>EMA50>EMA100 时阻 open_short；下跌regime自动失效 |
| ema20 方向一致 | 硬 | 多头 price≥EMA20×0.995，空头≤×1.005（range_edge小偏离豁免） |
| trend_phase 延伸/耗竭 | 软 | 延伸/耗竭阶段扣分缩仓（20-30） |
| RSI 极端 | 硬 | RSI7<20 不空、>80 不多 |
| coin momentum gate | 硬 | 停滞/耗竭/衰减/逆动量 各自阻断 |
| blocked_regime | 硬 | AI 报 chop/news_risk/no_trade |
| same_dir_loss_cooldown | 硬 | 同向上一单亏损且<20min |
| chasing_worse_price | 软 | 60min 内同向更差价再入，扣25 |
| correlated_adverse_throttle | 硬(opt-in,默认关) | 自身近期平仓亏损聚集（仅live窗口验证过，多年proxy未确认） |

> 注（trend 对齐）：逆势入场硬阻断。历史上曾试"逆势甜区软门"，2026-06-04 严格回测
> （真实SL/TP+费，5.5月）证明逆势两个方向都亏（-0.38%/-0.82%），已回退为硬阻断。

### Stage 2 Structural Fit（结构契合）
| 检查 | 类型 | 条件 |
|---|---|---|
| regime_structure_mismatch | 硬 | setup_type 必须在该 regime 的 AllowedSetups 内 |
| squeeze_regime 低信心/低RR | 硬 | squeeze/crowded 需 ≥SqueezeMinConfidence 且 ≥SqueezeMinRR |
| regime 交叉验证 | 硬 | AI regime vs 系统 regime；方向顺系统趋势可豁免 |
| protection 对齐 | 硬/软 | 计划被结构策略拒→硬；TP在首目标前→软(信息) |
| range_middle_without_edge | 软 | range 中部非 edge setup，扣15 |
| fake_retest_trap | 硬 | entry 距锚点<0.15% 且多支撑/空阻力=假回测 |

### Stage 3 Confidence & Risk（信心与风险）
| 检查 | 类型 | 条件 |
|---|---|---|
| confidence_below_min | 硬 | Confidence ≥ MinConfidence |
| rr_below_min | 硬 | 有效RR+0.02 ≥ MinRR（运行时按执行约束重算） |
| net_rr_below_min | 软 | 费后净RR<min，扣20 |
| SL距离 ATR 下限 | 硬 | slDist/ATR ≥ MinSLDistanceATRMul(默认1.2) |
| SL vol buffer | 软 | <base+buffer→执行时自动加宽 |
| SL距离% 上限 | 硬 | slPct ≤ MaxSLDistancePct（山寨×2，netRR≥2再×1.5；<0禁用） |
| SL% 地板 | 硬 | slPct ≥ 0.15%（防被tick噪声扫） |
| reward 距离 ATR 下限 | 硬 | rewardDist/ATR ≥ MinRewardATRMul(默认1.8) |
| short 非下跌需高信心 | 硬 | 空头且非trend_down → ≥ShortNonDowntrendMinConfidence(默认85) |
| unsupported_setup_type | 软 | 非[trend_pullback/range_edge/breakout_retest] |
| breakout_retest_blocked | 硬(opt-in) | 每个时段都净负，硬阻 |
| net_rr_above_max | 硬 | netRR > MaxNetRR(默认2.8) 过度承诺目标很少达成 |
| ai_cot_hesitation | 软 | 思维链≥2处犹豫/自我修正，扣20 |

---

## 第4层 Risk Budget（风险预算 / sizing 管线） — [LEV]+[RISK]

把"下多大注"定下来。**这是全项目证据最硬的一层**：大注不带 edge（top10%仓位中位
ppn 仅 0.0003），却贡献 45% 尾部亏损（看错×下重交互）。管线顺序（long/short 对称）：

| 顺序 | 控制 | 公式 / 条件 | 来源 | 状态 |
|---|---|---|---|---|
| 4.0 | Gate score 乘子 | size ×= SizeMultiplier（第3层软门汇总） | [INFO] | 生效 |
| 4.1 | Evolution 适配 | 进化策略微调 size/confidence | [INFO] | 视配置 |
| 4.2 | Vol-sizing | size ×= clamp(VolTargetPct/atr14%, min, max) | [RISK] | opt-in |
| 4.3 | 仓位价值比 | size ≤ equity × ratio（BTC/ETH默认5x，山寨1x；`enforcePositionValueRatio`） | [LEV] | 生效 |
| 4.4 | 可负担性 | size ≤ availBalance/marginFactor ×0.98 | [LEV] | 生效 |

> **单笔价值上限已由 4.3 `enforcePositionValueRatio` 负责**：`size ≤ equity × ratio`，
> 按币种分档（BTC/ETH 默认5x、山寨1x）。倍数由 owner 主动设定，**故意调高以允许单笔高价值
> 是有意设计**，不得用另一个更低的独立 cap 去对抗它。
>
> 决策记录：曾评估过在 4.4 之后再加一个独立 notional cap（回测 CVaR -8.37→-5.37），但它与
> 4.3 功能重复、且会压制 owner 故意抬高的倍数上限，**已撤除**。详见 DecisionLog DEC-0109。
> 结论：单笔风险预算只由 4.3 一处控制，调节手段是改 `*MaxPositionValueRatio` 倍数，不是加新 cap。
>
> **vol-target（4.2）留研究项不投产**：限制触发版仍削掉近期反弹修复，净值恶化，未过 kill criteria。

---

## 第5层 Exchange Constraint（交易所约束） — [EXE]

| 控制 | 条件 |
|---|---|
| 最小下单额 | size ≥ MinPositionSize（默认12 USDT），否则拒单 |
| 入场价偏离 | 计划价 vs 现价 ≤ MaxEntryDeviationPct（默认1.5%） |
| 杠杆 cap | Leverage ≤ BTCETHMaxLeverage / AltcoinMaxLeverage |
| 最大保证金 | 保证金占用 ≤ MaxMarginUsage（如0.9） |
| Maker 入场 | opt-in：post-only 挂近触价赚 maker 费，未成可回落 market（[COST]） |
| 容量替换 | 满仓时高信心新单可切最弱旧仓（ReplaceWeakest*） |

成交：quantity = actualPositionSize / currentPrice → 尝试 maker → 回落 market → 记录订单。

---

## 第6层 Position Protection（在场保护，挂交易所侧） — [RISK]

开仓后 `applyPostOpenProtection` 挂保护；`managePositionRisk` 循环维护。失败则紧急平仓。

| 机制 | 配置 | 触发 |
|---|---|---|
| Drawdown 阶梯止盈 | DrawdownTakeProfitConfig.Rules[] | 每 tier：pnl≥MinProfitPct 后 armed，回撤≥MaxDrawdownPct 平 CloseRatioPct；支持 ATR 单位 |
| 保本止损 | BreakEvenStopConfig.Rules[] | pnl 达 TriggerValue（profit_pct 或 r_multiple，支持 ATR）→ 移 SL 到成本±OffsetPct |
| Tier 分配 | 开仓时一次性锁定 DrawdownTierAllocation | 高水位/superseded 状态跟踪 |
| Runner 语义 | RunnerKeep/StopMode/TargetMode | 保留一部分跑趋势，BE 对 runner 可豁免 |
| Excursion 追踪 | UpdateExcursion（每 poll，无条件） | 记录 MFE/MAE 及开仓 ATR 倍数，供回测/反查 |

---

## 第7层 Exit Logic（离场逻辑，代码侧强制） — [RISK]

`managePositionRisk` 每仓按序执行（前一个触发即 continue，跳过后续）：

| 顺序 | 机制 | 触发条件 | 豁免 | 证据 |
|---|---|---|---|---|
| 7.0 | Excursion 记录 | 无条件每 poll | — | 过程数据留痕 |
| 7.1 | Time-stop | held≥TimeStopHours **且** pnl≤TimeStopLossPct(负) | 赢家（亏损条件必须满足） | 切"逆势慢失血20-37h" |
| 7.2 | Max-hold | held≥MaxHoldHours **且** pnl<ProfitExemptPct | 盈利runner | 清扫横盘/dust占仓位 |
| 7.3 | Trailing-TP | peak≥TrailingActivatePct 后，回撤≥TrailingGivebackPct(占peak%) 且 pnl≥TrailingMinLockPct | 未 armed | 让赢家跑、锁利润 |
| 7.4 | Drawdown 规则 | 见第6层 | — | AI/config 阶梯 |
| 7.5 | Break-even | 见第6层 | runner 可抑制 | — |
| — | AI 平仓 | 第1层决策，受第2层权限门 | — | [INFO] |

> 注：Time-stop（7.1，需亏损）与 Max-hold（7.2，含横盘/保本）是两个独立机制，都可能触发。
> **研究结论**：纯粹按持仓时长的"时间止损"对逆势亏损无效（hold vs pnl 相关仅0.049，
> 亏损开仓即定型）——所以 7.1 保留"亏损条件"是必要的，不能退化成纯时间闸。

---

## 附录 A：控制参数全表（RiskControlConfig 关键字段）

| 参数 | 层 | 默认 | 含义 |
|---|---|---|---|
| MaxPositions | 5 | — | 同时持仓上限 |
| BTCETHMaxLeverage / AltcoinMaxLeverage | 5 | — | 杠杆上限 |
| BTCETHMaxPositionValueRatio | 4.3 | 5.0 | BTC/ETH 单仓价值≤equity×此 |
| AltcoinMaxPositionValueRatio | 4.3 | 1.0 | 山寨单仓价值≤equity×此 |
| MaxMarginUsage | 5 | — | 最大保证金占用 |
| MinPositionSize | 5 | 12 | 最小下单额 USDT |
| MinRiskRewardRatio | 3 | — | 最小 RR |
| MinConfidence | 3 | — | 最小开仓信心 |
| EntryCooldownMinutes | 2/3 | 90 | 亏损后同币冷却 |
| MaxEntryDeviationPct | 5 | 1.5 | 计划vs执行价最大偏离% |
| TimeStopHours / TimeStopLossPct | 7.1 | 24 / -1.5 | 时间止损（须亏损） |
| MaxHoldHours / MaxHoldProfitExemptPct | 7.2 | 18 / 2.0 | 最大持仓（赢家豁免） |
| TimeStopBars / MaxHoldBars | 7 | — | bar 计数覆盖 *Hours（follow_primary时） |
| TrailingTakeProfitEnabled | 7.3 | false | 追踪止盈开关 |
| TrailingActivatePct / GivebackPct / MinLockPct | 7.3 | 3.0 / 35 / 0.5 | 追踪止盈三参 |
| VolSizingEnabled / VolTargetPct / Min/MaxMult | 4.2 | false / 1.5 / 0.4 / 1.5 | 波动目标仓位 |
| MakerEntry* | 5 | — | maker 入场 [COST] |
| ReplaceWeakest* | 5 | false | 满仓强信号替换最弱仓 |

## 附录 B：新增规则该放哪层（决策指引）

- 新的"看得更准/预测"类想法 → 第1层，但先质疑：方向已证明≈掷硬币，多半无效。
- 新的"该不该开这一单"过滤 → 第3层 Entry Gate（注意别让它无限膨胀）。
- 新的"下多大注" → 第4层 Risk Budget（[LEV]，当前最有 edge 的层）。
- 新的"成交/费用"优化 → 第5层（[EXE]/[COST]）。
- 新的"在场如何保护/离场" → 第6/7层（[RISK]）。

每加一条，先在 Decision Log 立项（Hypothesis + Primary Metric + Success Criteria），
跑完必须有 Random Control。见 ResearchPrinciples.md。
