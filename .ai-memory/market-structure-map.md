# Market Structure Map 升级 (进行中)

专家框架: 所有结构统一为 `价格区间+来源+行为+新鲜度+强度+方向角色+失效条件`。
核心原则: 结构是"提升信心的证据",不是全部硬门禁(否则开仓更少)。

## 系统现状盘点 (2026-07-18)
管线: DetectStructuralLevels(swing/vol_cluster/fib) → MergeIntoZones → ApplyFlipLogic
→ ApplyTimeframeBoost → AssignQualityGrade → EnrichZoneMultiTF → FilterTopZonesForAI(每方向top3)
已有: 区间化 zone(Low/High/Mid)、Confidence 0-100、QualityGrade A/B/C、VolumeScore/RecencyScore
/TouchCount/MultiTFCount、Flipped/FlipCount、EvaluateForTrading(SL/TP用途)、Fib回撤+扩展、盘中VWAP、depth imbalance。
关键: kernel/engine_analysis.go 有10+结构硬校验 + trader/entry_gate.go structural_fit 阶段=硬门 → 与"证据非门禁"冲突,Phase3 再改。

## 缺口 vs Phase1(10模块)
缺: 前日/前周H/L、显式range状态机、BOS/CHOCH+回踩、供需区(order block)、真VP(POC/VAH/VAL/HVN/LVN)、
Anchored VWAP、K线FVG、Equal H/L流动性池、反应强度+OI变化。
状态机仅Flipped布尔(缺fresh→tested→reacted→retested→weakened→broken→flipped→invalid)。
角色仅support/resistance(缺反转/延续/接受/加速边界/流动性目标5类)。

## 流水线(单操作者多角色): Builder→Tester→Validator→Reviewer→Adversary,每模块 go build+go test 绿灯

## 进度
- [x] 模块1 Volume Profile: market/volume_profile.go + _test.go (8测试过). POC/VAH/VAL(70%)/HVN/LVN,
  50 bins,按overlap分配成交量. 接入 data.go(primary+每TF) + types.go(Data/TFSeries.VolumeProfile) +
  kernel/formatter_structural.go formatVolumeProfile(). 纯证据,不碰gate.
- [x] 模块2 Anchored VWAP: market/anchored_vwap.go + _test.go. 从最近swing high/low锚定,±1σ带.
  接入 data.go + types.go(AnchoredVWAPs) + formatAnchoredVWAPs().
- [x] 模块3 前日/前周 H/L: market/period_levels.go + _test.go(5测试过). 按UTC日/ISO周分桶聚合,
  含今日developing extremes. 时间戳秒/毫秒自动侦测(normalizeToMillis). 接入 data.go(widestKlines跨度最宽序列
  算周期位,前周需最长历史) + types.go(PeriodLevels) + formatPeriodLevels().
- [x] 模块4 FVG + Equal H/L 流动性池: market/fvg_liquidity.go + _test.go(6测试过).
  FVG=3根K线缺口(bullish/bearish),含fill_ratio回补检测+size_atr显著性过滤(>0.15xATR).
  LiquidityPool=等高/等低swing聚类(tol=0.2xATR, >=2触及). 接入 data.go(primary+每TF) +
  types.go(FairValueGaps/LiquidityPools) + formatFVGAndLiquidity()(仅显示未回补FVG+最近3个池).
  primaryATR14 从 timeframeData[primaryTimeframe].ATR14 取.

## Phase 1 完成 (4新检测器全部纯证据接入, 未碰gate)
仍缺(专家Phase1剩余,但部分已被现有zone覆盖): BOS/CHOCH+回踩区, 供需区(order block).
range顶/底/中轴已由zone近似; 多TF swing/测试次数已有.
- [ ] Phase2: zone状态机 + 5角色 + 反应强度/OI
- [ ] Phase3: 门禁哲学重构(硬门→信心加分,仅失效位保留硬门) — 风险最高单独做
- [ ] 后期: 图表显示、止损、选币过滤强化

## 注意
- go 在 /usr/local/go/bin (PATH需export). `go build ./...` 会超时,按包 build ./market/ ./kernel/.
- 全程纯增量,未改任何 gate/校验逻辑.
