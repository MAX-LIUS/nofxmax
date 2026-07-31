# 入场门禁「只信 AI 自报、不做验证」缺口审计（2026-07-31）

> 起因：用户问「系统只接受 AI 的分析而不验证那些，跟 HH/HL 检测缺陷一起提到的，是否都堵上了」。
> **答案：没有。只堵了方向/结构这一处（HH/HL 闸门），下面这个缺口更大且未修。**

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
