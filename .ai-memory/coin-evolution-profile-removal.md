# 币种进化画像（Coin Evolution Profile）剥离清除

> 2026-08-04。用户指令原文：「历史画像很坑人请完全切断历史画像对系统的作用并去掉这部分代码，
> 我不需要他有越来越明确的技术倾向影响系统决定。就是进化画像这个功能对吧？剥离清除」
>
> 确认：是。用户说的「历史画像」= 策略工坊里的**进化引擎**（Evolution Engine），
> 落库表 `coin_evolution_profiles`，注入提示词的那段 `Historical Performance Profile: LONG: 适配度
> 76/100 (样本5笔) | EMA20一致胜率70% ... SHORT: 适配度 77/100 (样本13笔)`。

## 为什么它「坑人」（用户判断成立，机制清楚）

画像是**用交易员自己的历史成交**算出来的，然后又拿去影响下一次决策，是个正反馈环：
一段运气会硬化成「这个币的技术倾向」，模型转而听信画像而不是读当前结构。样本门槛低到
`SampleSize >= 3`，三笔就开始产出「适配度」并进提示词。

**两条真实作用路径**（不是只有提示词那一条，这点必须记住）：
1. **提示词注入** —— `kernel/engine_prompt.go` 两处：候选币段 `Historical Performance Profile:`、
   持仓管理段 `Historical Performance:`。由 `Context.EvolutionContexts` 承载。
2. **机械改仓位** —— `applyEvolutionAdaptations()` 在 `executeDecisionWithRecord` **之前**直接乘
   `d.PositionSizeUSD`：`reduce_size_50%`/`reduce_size_30%`、`require_confidence_85/90` 降仓，
   以及 **`allow_confidence_65` → ×1.15 放大仓位**（对历史赢家加注）。
   这条比提示词更危险：它绕过门禁评分与风险预算，直接改执行量。

剥离时线上 **5 个策略全部 `evolution.enabled=1` 且 `inject_to_prompt=1`**（BN-chart+、Claude15-chart、
GPT-hhhl+、Claude-Orig、claude-hhhl），即这功能一直在生效，不是睡眠代码。

## 删了什么

删除文件：`store/evolution_engine.go`(858) / `store/evolution_engine_test.go` /
`trader/evolution_updater.go`(285) / `api/handler_evolution.go`(170) /
`web/src/components/trader/EvolutionProfilePanel.tsx`(383) / `web/src/components/strategy/EvolutionEditor.tsx`(284)。

摘除引用：
- `store/store.go` —— `evolution` 字段、`Evolution()` 访问器、启动时的 `AutoMigrate()`
- `store/strategy.go` —— `StrategyConfig.Evolution` 字段 + `EvolutionConfig` 结构体
- `store/position.go` —— `GetClosedTradesForEvolution()`（唯一调用方是进化引擎，成了死代码）
- `kernel/engine.go` —— `Context.EvolutionContexts` 字段
- `kernel/engine_prompt.go` —— 两处注入点
- `trader/auto_trader_loop.go` —— 三处：`applyEvolutionAdaptations` 调用、
  `go updateEvolutionProfile(...)`、7b 段的画像装载
- `api/server.go` —— `/evolution/{profiles,reset,rebuild/:id}` 三条路由
- 前端 —— `data.ts` 两个方法、`types/trading.ts` 三个接口、`types/strategy.ts` 的
  `EvolutionConfig`、`StrategyStudioPage` 的「进化引擎」分区、`InsightPanel` 的「进化洞察」页签、
  `TraderDashboardPage` 的 `CoinProfileSummary`(图下 🧬 条) 与 `EvolutionProfilePanel`

顺带：`InsightPanel` 的 `traderId` prop 与 `formatTimeAgo()` 只被进化页签使用，一并删除并改了调用方。

## 数据库：表**保留**，不删数据

`coin_evolution_profiles` 原样留在生产库，只是不再有任何读写。生产数据不可删是既定约束，
且用户要的是「去掉这部分代码」，不含删数据。副作用：库里每个 strategy 的 config JSON 仍带
`evolution` 对象，无人重写这些行。

## 两条回归测试（都做过阴性对照）

- `store/evolution_removed_test.go::TestLegacyEvolutionKeyIgnored` —— 带 `evolution` 块的旧 config
  必须仍能反序列化（Go 默认忽略未知字段，全仓无 `DisallowUnknownFields`），且重新序列化**不得**
  再吐出该块。若将来有人在这条路径上加严格解码，先炸的是这个测试而不是全部实盘交易员。
- `kernel/evolution_prompt_removed_test.go::TestPromptCarriesNoHistoricalProfile` —— 渲染真实
  prompt，断言不含 `Historical Performance` / `适配度` / `EMA20一致胜率`（中英双语各跑一遍）。

**⚠️ 这条测试第一版是空壳，教训记下来**：两个注入点都在 `for` 循环体内，循环对
`ctx.MarketDataMap` 里没有该 symbol 的币 `continue`，且**候选币循环还会跳过已是持仓的 symbol**。
我第一版 fixture 候选币与持仓用了同一个 SKHYNIXUSDT 且没给 MarketDataMap → 两个循环体一次都没执行，
测试**假绿**。是靠「把注入代码临时加回去、看测试是否变红」的阴性对照抓到的（第一次对照两个点都没红）。
修正后 fixture 用不同 symbol + 双方都给 MarketDataMap，并在测试里**先断言两个 symbol 确实出现在
prompt 里**再断言禁词不存在，防止后人再退回空壳。逐点阴性对照已复验：候选点、持仓点分别改回去都能变红。

**通用教训：删除类测试（断言「不含 X」）天然容易假绿，必须配阴性对照，否则等于没测。**

## 验证

`go build ./...` 通过；`go test ./...` 全绿（store/trader/kernel/api 及各交易所子包）；
`npx tsc --noEmit` 零错误；`npm run build` 成功且无 circular dependency 告警。
