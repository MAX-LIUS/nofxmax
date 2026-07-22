# Binance 原生 Trailing Stop 修复记忆

> **更新**: 2026-07-22
> **状态**: 核心修复已完成，待部署 / 已部署验证
> **背景**: BN 交易员原生 trailing stop 在开仓价即触发，导致 `unresolved_exchange_close` 与利润侵蚀。

---

## 1. 真正根因（已证实，非 Binance bug）

go-binance **v2.8.9** 库 bug：TRAILING_STOP_MARKET 的激活价参数用了**错误的键名 `activationPrice`**，
而 Binance algo 端点期望的是 **`activatePrice`**。Binance 静默忽略未知参数，把激活价默认成
下单时的市价 → 开仓即武装，任何逆向 tick 立即触发。

- **修复**：升级到 go-binance **v2.8.10**（commit "Rename activationPrice to activatePrice"；
  `ActivationPrice()` 保留为兼容别名，内部委托 `ActivatePrice()`）。适配层显式调 `.ActivatePrice()` 绑定正确键。
- **实盘验证**：发送 activatePrice = entry*1.02，交易所登记为精确 +2.000%（0.000% 偏差）。

## 2. 端点选择（已证实）

- 经典端点 `/fapi/v1/order` 现在**拒绝** TRAILING_STOP_MARKET，返回 **-4120**
  （"Order type not supported... use Algo Order API"）。2026-07-22 实盘确认。
- 必须走 algo 端点 `/fapi/v1/algoOrder`（algoType=CONDITIONAL）。
- go-binance v2.8.10 API：`NewCreateAlgoOrderService()` → `resp.AlgoId`(int64) / `resp.ActivatePrice` / `resp.CallbackRate`；
  取消用 `NewCancelAlgoOrderService().AlgoID(int64)`。

## 3. 读回校验（保留为回归护栏）

下单后读回交易所登记的 activation/callback，偏差 >0.3% 或缺失 → 取消该 algo 单并返回 error
→ 调用方降级到本地托管回撤监控（local monitor）并在面板告警。v2.8.10 后正常通过；作为未来
参数/API 变更的护栏保留。

## 4. 已知遗留（用户决定：记录、代码保持现状）

### Issue 1 — algo 单最小名义额（graceful degradation，保持现状）
algo 单有最小数量约束（~18-20 USDT 名义额），低于会报 **-4136**。低于阈值时优雅降级到
本地监控。**保持现状**：小仓位走 local monitor 是可接受的安全行为。

### Issue 2 — list 端点不返回 activatePrice（显示问题，仅记录）
`ListOpenAlgoOrders` 返回的 `[]GetAlgoOrderResp` **缺 `ActivatePrice`**（只有 `TriggerPrice`）；
单条查询 `GetAlgoOrder` 才有 `ActivatePrice`。`GetOpenOrders` 对 algo TRAILING 单用 triggerPrice
近似填充 `ActivationPrice` 并标 `ActivationStatus="activated"`。**保持现状**：不影响下单正确性，
仅列表展示近似。

## 5. 已修复的连带风险 — CloseLong/CloseShort 陈旧缓存裸仓（issue 3，已解决）

**事故**：CloseLong 读了 15s `GetPositions()` 缓存（9.7s 陈旧），在 DOGEUSDT 实际持仓时
报 "no long position"，险些留裸仓。
**修复**：CloseLong/CloseShort 的 quantity==0 分支改用无缓存的 `execPositionAmt(symbol, side)`
（直接调 `NewGetPositionRiskService().Symbol()`，symbol 级 GetPositionRisk），返回绝对值。
测试 mock（`trader/binance/futures_test.go`）已改为尊重 symbol 查询过滤，匹配真实 API 行为。

## 6. 关键文件

- `go.mod` — go-binance v2.8.9 → **v2.8.10**（根因修复）
- `trader/binance/futures_orders.go` — `setTrailingStopLossCore` 重写为 algo 端点 + `.ActivatePrice()`；
  CloseLong/CloseShort 改用 `execPositionAmt`
- `trader/binance/futures_positions.go` — `execPositionAmt`（无缓存、symbol 级）
- `trader/auto_trader_risk.go` — `supportsNativeTrailingStop` binance: `SupportsAlgoOrders && CanAmendProtection`
- `trader/binance/futures_test.go` — mock 尊重 symbol 过滤

## 7. 临时探针工具（用后清理）

`cmd/bntrailprobe`（E2E 适配层探针）、`cmd/bnclose`（强制平仓）、`cmd/bnalgolist`（列/取消 algo 单）、
`cmd/bnfilters`（过滤器）。`/opt/webstack/nofx/` 下同名二进制需清理。
