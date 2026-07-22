# 交易所委托同步 / 保护单 / 归因 审计清单

分支: `fix/exchange-order-verification`(基于 `dev`,独立 worktree `../nofxmax-exwork`)
状态: Phase 0 — 问题固化,待 review
适用交易所: Binance(BN)、OKX。其余(Bitget/Bybit/…)非当前活跃交易员,列为后续。

---

## 0. 根问题

系统以「本地下单意图」为真相源,缺一层「以交易所回读委托为准」的校验。
下单时记下我们「以为的」触发价/数量/激活价,之后几乎从不跟交易所核对真实委托明细。
Binance 是重灾区;OKX 基本健康(已读真实值),但部分共性缺陷两家都有。

复现事故: BN SKHYNIX/ETH/SOL 三笔在开仓价附近被移动止损打掉、close_reason=`unresolved_exchange_close`。
根因链: 移动止损 activation 被算法端点吞 → 从开仓价即追踪 → 小回撤触发 → 归因四层全失败。

---

## 1. 问题矩阵(已逐条代码核实)

| # | 问题 | Binance 现状 | OKX 现状 | 处理范围 |
|---|------|--------------|----------|----------|
| A | 算法委托明细回读 | ❌ `NewGetAlgoOrderService` 全库零调用 | ✅ 查真实 algo pending(activePx/callbackRatio/moveTriggerPx/sz/state) | BN 补齐到 OKX 水准 |
| B | 下单后校验交易所回显 | ❌ 只取 `resp.AlgoId`,弃 `resp.TriggerPrice`/`ActivatePrice` | ❌ 同样不回读校验 | **两家都补** |
| C | trailing activation 生效 | ❌ `/fapi/v1/algoOrder`(CONDITIONAL)吞 activation,当立即激活 | ✅ `activePx` 生效 | 仅 BN |
| D | 面板/reconciler 用真实值 | ❌ GetOpenOrders 对 trailing 无条件标 `activated`、triggerPrice 当激活价 | ✅ 按真实 state 判定(live/effective) | 仅 BN |
| E | 归因查真实委托 | ❌ L2 查普通端点 `/fapi/v1/order`,algo 派生 fill 只回 `MARKET` | ✅ 查 algo pending + client-id | 仅 BN |
| F | trailing coded client-id | ❌ `SetTrailingStopLoss` 用纯 `getBrOrderID()` | ✅ tagged("native_trailing") | 仅 BN |
| G | immediate_trailing 回调单位 | ❌ 传 ratio(0.0374)被当百分数夹成 0.1% | ⚠️ 需验 `normalizeOKXCallbackRatio` 路径 | **两家都验** |
| H | immediate_trailing 注册 mechanism | ❌ 不在 `mechanismToCode` | ❌ 同(共享 codec) | **两家都补** |

结论: BN 中 A/C/D/E/F 全中;OKX 基本健康;B/G/H 为跨交易所共性,两家都要查/补。

---

## 2. 每条问题的代码位置与证据

### A — 算法委托明细从不回读(BN)
- `grep NewGetAlgoOrderService trader/binance/*.go` → 零调用。
- BN 的 SL/TP/trailing 全走 `NewCreateAlgoOrderService`(`trader/binance/futures_orders.go:51/964/1095`)。
- `GetAlgoOrderResp`(go-binance)有 `ActualOrderId`/`TriggerPrice`,可反查真实明细,但从未使用。
- OKX 对比: `trader/okx/trader_orders.go:1399-1453` 查真实 algo pending 并读回全部字段。

### B — 下单后不校验交易所回显(两家)
- BN `SetStopLossTagged` / `setAlgoTakeProfit` / `setTrailingStopLossCore`: 只取 `resp.AlgoId`,日志打「意图值」,连 `resp.TriggerPrice`/`ActivatePrice` 都不比对。
- OKX 下单路径同样未在 place 后回读校验(仅后续 reconcile 时读真实值,非下单即时校验)。

### C — trailing activation 被算法端点吞(BN)
- `setTrailingStopLossCore`(`trader/binance/futures_orders.go:31-120`)走 `NewCreateAlgoOrderService` → POST `/fapi/v1/algoOrder`(algoType=CONDITIONAL)。
- 实证(SKHYNIX,algoId=2000001300309120):代码发 `activation=1399.7984 callback=1.2%`(日志 `futures_orders.go:118`),交易所实际存激活价 ≈1346.35(下单时市价)、回调 1.2%,当作立即激活。
- 从开仓价即追踪 → 峰值≈1346.66 → 跌 1.2% → 1330.5 ≈ 实际成交 1331.54。
- go-binance 两端点都无条件发 `activationPrice` 参数(`order_service.go:200`、`algo_order_service.go:157`),所以是 CONDITIONAL 端点对 TRAILING 的 activation 语义与经典端点不一致。
- 引入自 commit `8ad2a0d`(为让 trailing 出现在 GetOpenOrders 算法列表)。

### D — 面板/reconciler 用假设值而非真实值(BN)
- `GetOpenOrders`(`trader/binance/futures_orders.go:910-913`):对任何 trailing 单无条件写 `ActivationStatus="activated"`,拿 `triggerPrice` 当激活价。注释明说是「假设币安一定按激活价激活了」。
- 后果: reconciler 每轮报 `verified=true`,掩盖 activation 被吞。
- `GetAlgoOrderResp`(open-algo 列表)无 `activatePrice`/`callbackRate` 字段,即使读列表也拿不到真实激活价/回调。
- OKX 对比: 按真实 `state`(live/effective)判定 `activationStatus`。

### E — 归因查错端点(BN)
- L2 `lookupOrderType`(`trader/binance/close_attribution.go:32-51`)查 `NewGetOrderService()` → 普通端点 `/fapi/v1/order`。
- 成交 `trade.OrderID` = `at.OrderID`(`trader/binance/futures_account.go:227`)= 算法单触发后新派生的普通市价单 id。查普通端点得 `OrigType=MARKET`,真实「TRAILING_STOP_MARKET + 触发价」元数据在算法命名空间,只能 `GetAlgoOrder(algoId)` 查。
- 四层归因全空 → `unresolved_exchange_close`(`trader/binance/order_sync.go:242`)。
- 实测: BN 近期 SL/TP 类(ladder_tp/break_even/structural_sl,42 笔)靠 coded client-id 归因成功;unresolved=7 基本是 trailing。

### F — trailing 无 coded client-id(BN)
- `applyNativeTrailingDrawdown` 的 BN 分支(`trader/auto_trader_risk.go:1968/2167`)调 `SetTrailingStopLoss`(无 tag)→ 纯 `getBrOrderID()`。
- OKX 分支(`:2035/2210`)调 `SetTrailingStopLossTaggedWithID(...,"native_trailing")` → coded client-id。

### G — immediate_trailing 回调单位错乱(两家验)
- `placeImmediateTrailing`(`trader/protection_execution.go:353-404`)传 `callbackRatio` = 小数比率(如 0.0374)。
- BN `setTrailingStopLossCore` clamp 按百分数 `[0.1, 5]` 处理 → 0.0374 < 0.1 被夹成 0.1%。
- 实证: `activation=1346.0946 callback=0.1% reason="immediate_trailing"`(该 3.74% 变 0.1%)。
- OKX: `normalizeOKXCallbackRatio`(`trader/okx/trader_orders.go:476`)语义需核实是否同样受影响。

### H — immediate_trailing 未注册 mechanism(两家)
- `mechanismToCode`(`store/reason_codec.go:31-50`)无 `immediate_trailing`。
- `EncodeReasonClientID` 返回 "" → 回退纯 id → 触发后归因失败。共享 codec,两家都受影响。

---

## 3. 分阶段修复计划

每阶段独立 commit / 独立 PR,便于 review + 回滚。共享文件(`auto_trader_risk.go` 等)改动收敛到最小、单独 hunk。

### Phase 0 — 问题固化(本文档,无代码)
产出本审计清单,作为两条线边界声明 + 验收基准。

### Phase 1 (P0) — BN trailing 止血(独立 PR)
- C+F: trailing 改经典 `CreateOrderService` + `TRAILING_STOP_MARKET` + coded client-id
- G: immediate_trailing 回调单位对齐(ratio→percent),同时验 OKX 路径
- 配套: `GetOpenOrders` 从经典 openOrders 读回 trailing,保面板可见
- 验证: 单元测试(activation 生效 + 单位换算);testnet/小仓实盘验证激活价按峰值

### Phase 2 (P1) — 权威回读层(独立 PR,治本)
- B: 两家下单后 `GetAlgoOrder`/algo-pending 回读校验 trigger/activate/callback/qty,不符告警+重下
- A+D: BN 面板/reconciler 改用真实回读值,对齐 OKX
- E: BN 归因 L2 拿到裸 MARKET 时回退查 `GetAlgoOrder` 补 origType/触发价

### Phase 3 (P2) — 归因补全 + 回填
- H: 注册 `MechImmediateTrailing`(共享 codec,两家生效)
- 历史 unresolved 用 `cmd/closeeventbackfill` 按真实 algo 明细回填

### Phase 4 (P3) — 语义决策(待拍板)
- immediate_trailing 开仓价触发是否保留

---

## 4. 隔离 / 协作契约

- 本线 worktree: `../nofxmax-exwork`,分支 `fix/exchange-order-verification`,基于 `dev`。
- 结构位线: 主工作区 `/root/projects/nofxmax`,`dev` 上带未提交改动,涉及 `market/*`、`kernel/*`、`trader/entry_gate.go`、`store/strategy.go`。
- 本线文件产权: `trader/binance/*`、`trader/okx/*`、`trader/close_attribution.go`、`store/reason_codec.go`、`store/attribution.go`;共享文件 `trader/auto_trader_risk.go`、`trader/protection_reconciler.go`、`trader/protection_execution.go` 改动最小化。
- 重叠面: 近乎为零。唯一交集 `store/strategy.go`(本线尽量不碰)。
- 节奏: 每阶段 rebase `dev`;共享文件改动放阶段末尾单独 hunk。

