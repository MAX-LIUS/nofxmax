# 入场门禁「只信 AI 自报、不做验证」缺口审计（2026-07-31）

> 起因：用户问「系统只接受 AI 的分析而不验证那些，跟 HH/HL 检测缺陷一起提到的，是否都堵上了」。
> **答案：没有。只堵了方向/结构这一处（HH/HL 闸门）。**

## ⚠️ 回测结论（2026-07-31 当天完成）：**不要按真实止损重算 RR**

审计发现的「RR 用自报止损、实际执行有 1.5ATR 地板」是**真的**（见下文）。
但我随后做的回测显示，**按真实止损重算 RR 会主动损害绩效，不能修**。

样本 n=8682（2026-05-05~07-29，四个交易员，要求 24h 完整后续 K 线，
剔除 2 笔价格错配脏数据 SPCXUSDT 0.7194→156.34 / XAUUSDT 68.9→4154）。

**真实止损口径（挂 1.5ATR 止损 + 目标止盈 + 超时），效应随持仓时长单调加强：**

| 时长 | 真实RR≥1.5 放行组 | 拦截组 | 差 | p |
|---|---|---|---|---|
| 6h | -0.168% / 胜率42.0% | +0.075% / 51.8% | -0.243 | 0.0000 |
| 12h | -0.480% / 37.4% | +0.440% / 54.6% | -0.920 | 0.0000 |
| 24h | -0.662% / 33.2% | +0.585% / 54.1% | **-1.247** | 0.0000 |

（真实持仓中位 4.66h、P75 11.81h、P90 18h，6h 会截掉长尾，故必须测到 24h。）

**机制（三重确认，可定论）**：
1. `rho(真实RR, 目标距离ATR) = +0.9988` —— 几乎是恒等式。95.8% 的样本自报风险
   低于 1.5ATR 地板，`real_risk` 被钉成常数 `1.5×ATR`，于是
   **真实RR 退化成「目标距离 ÷ 1.5ATR」，不再是盈亏比，而是「目标有多远」**。
2. 目标距离单独就能复现全部负效应：[0,1)ATR 胜率 67.8% → [5,∞)ATR 26.6% 单调下降。
3. **自然实验**：floor 不生效的子样本（自报风险已≥1.5ATR，n=367）效应**消失**
   （差 +0.099，p=0.3525）；floor 生效的子样本 -1.305，p=0.0000。

## 陷阱记录：目标距离看着像信号，实为循环论证

第 6 步曾得出「目标距离<1ATR」样本外增量 **+1.1401，p=0.0000**，四个交易员、
三个月全正，样本外还强于样本内 —— 漂亮得可疑，一查就是**循环论证**：
目标距离在模拟里**同时决定止盈位**，近目标容易被打到记为小赢，远目标先吃 1.5ATR 止损。

| 口径 | 中位差 | 截尾期望差 | p |
|---|---|---|---|
| 带止盈 | +1.39（好看） | +0.1369 | 0.0088 |
| **拆掉止盈** | — | **-0.0198** | **0.8113** |
| 完全中性（无止损止盈） | +0.2452 | +0.0444 | 0.6472 |

放行组胜率 67.8% 但**均赢 +1.282% / 均输 -3.326%**，期望值 -0.1996
**比拦截组 -0.0518 更差**。**中位数在不对称赔付下会说谎；胜率高不等于期望正。**

## 更根本的发现：RR 本身就是弱判据，与怎么算无关

| RR 定义 | vs 中性收益 rho | vs 真实止损收益 rho |
|---|---|---|
| 自报RR（现行门禁在用） | +0.0010 | -0.0121 |
| 真实RR（提议改成的） | -0.0086 | -0.0585 |
| 自报止损ATR倍数 | -0.0106 | -0.0488 |

三种定义**全部接近零**。现行 `自报RR>=1.5` 门禁本身也没有预测力
（中性口径中位差 -0.1732 p=0.0680、截尾期望差 p=0.3797，方向还是反的）。

**所以真正的结论不是「RR 算错了要修」，而是「RR 这个判据本身价值很低」。**
原审计说的「风险低估 3~4 倍」在**风险度量**意义上成立，但把它接进门禁做拦截
**没有收益、甚至有害**。可做的是**审计形态记录真实RR与自报RR之差**（不拦截），
用于观察 AI 是否系统性低报风险，以及给仓位大小提供输入 —— 但不要当入场门禁。

分析脚本：`rr_bt1.py`~`rr_bt9.py`（`/root/.claude/jobs/5cbb3cf4/tmp/`）。

## 结论先行：RR 检验用的止损，比交易所实际挂出的止损近 3~4 倍

`trader/entry_gate.go` 检查 3b `rr_below_min` 用 `d.EntryProtection.RiskReward.Invalidation`
—— **AI 自报的失效位** —— 算 RR 并与 `min_rr` 比较。但实际执行的结构止损有
`protection.ladder_tp_sl.structural_sl.floor_atr_mul = 1.5`（claude-ct30/GPT-ct50 实配，
`enabled=true`）作硬下限，`atr_protection_resolver.go:664/684/714` 三处都会把倍数抬到 floor。

**地面真相对照**（`protection_plan_snapshots.tiers_json` 里落库的 `triggerPrice`，n=293，
不是推算）对比 AI 自报失效位距离（7 月决策记录，n=6423）：

| trader | AI 自报失效位距离(中位) | 交易所实际止损距离(中位) | 倍数 |
|---|---|---|---|
| claude | 0.770% | **2.901%** | 3.8× |
| GPT | 0.886% | **3.525%** | 4.0× |
| Claude-R | 0.563% | 1.636% | 2.9× |
| BN | 0.767% | 3.317% | 4.3× |

自报止损换算成 ATR14(1h) 倍数：中位 **0.786 ATR**，**95.5% 低于 1.5 ATR 的 floor**
（p10=0.395 / p25=0.530 / p75=1.054 / p90=1.273）。也就是说 floor 几乎**永远**在外推止损。

**对 RR 门禁的后果**（7 月 6423 笔开仓提议）：

| 判据 | 按 AI 自报止损 | 按 floor=1.5ATR 推后的止损 |
|---|---|---|
| min_rr=1.5 通过率 | 93.7% | **24.5%** |
| min_rr=2.0 通过率 | 48.4% | **10.5%** |

即：**当前 RR 门禁放行的单子里，约七成按真实止损算根本不满足 min_rr。**
风险被低估约 3~4 倍，RR 被高估约同等幅度。

## 这不是新发现，是被记下来却没做的事
`.ai-memory/production-deployment.md` 2026-07-20 那条结尾已写：
「NOTE (future, more surgical): recompute RR against ENFORCED stop (1.5×ATR floor)
instead of AI nominal invalidation — roots out the 'tight declared SL inflates RR'
(SPCX class).」——**一年前就定位了，一直没修。**
`min_sl_distance_atr_mul=0.8` 只是拦掉自报止损 <0.8ATR 的（拦掉 51%），
它**不解决**「用自报止损算 RR」这件事：0.9ATR 能过 0.8 门禁，但真实止损仍是 1.5ATR。

## 48 项检查的数据来源分布（粗分类，正则判定，仅供定位）
- **仅读 AI 自报 14 项**：`rr_below_min`、`net_rr_below_min`、`net_rr_above_max`、
  `fake_retest_trap`、`range_middle_without_edge_setup`、`unsupported_setup_type`、
  `breakout_retest_blocked`、`squeeze_regime_low_rr`、`squeeze_regime_low_confidence`、
  `short_confidence_below_regime_min`、`protection_policy_rejected`、`chasing_worse_price`、
  `same_direction_loss_cooldown`、`trigger_type_missing`
- **仅从行情实算 5 项**：`funding_rate_too_high`、`volatility_too_high`、
  `sl_distance_below_vol_buffer`、`sl_distance_pct_too_wide`、`coin_momentum_stale`
- **混合 17 项**（含本次新增的 `structural_alignment_missing` /
  `structural_blocking_level_too_close`，它们是这一阶段唯一从 K 线实算摆动结构的）
- 其他 12 项为常量/配置校验

## 建议的修法（未实施，需用户决定）
把 3b 的 RR 改用**实际会执行的止损**复算：
`effective_risk = max(|entry - ai_invalidation|, floor_atr_mul × ATR)`，
再算 `RR = |first_target - entry| / effective_risk`。
影响面很大（min_rr=1.5 下通过率 93.7% → 24.5%），**必须先回测再改**，
不能直接上线——否则等于一次性把开仓量砍到四分之一。
可选缓解：先以 audit 形态记录「真实 RR」与「自报 RR」的差，积累实盘证据。
