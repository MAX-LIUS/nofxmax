# Gate 交易所接入 — 首轮审计的 12 个缺陷与对齐结论

> **状态**:代码已修完并提交(`bed9b92`,分支 `dev`),**尚未部署**。
> **前置**:gate 交易员账户仍需先向 Gate U 本位合约钱包**转一笔 USDT**才能开户,
> 否则接口一律 `USER_NOT_FOUND: please transfer funds first to create futures account`
> —— 这不是网络/密钥问题(三个端点实测 200,0.53~0.67s),Gate 是首次转入时惰性建仓户。
> gate 的 `NewGateTrader(apiKey, secretKey)` **不收 passphrase**(API 文档字符串说必填是错的)。

用户指令原文:「全部推进修复,构建测试进行破坏性测试,确保功能可靠与其他两大交易所功能对齐,
特别是对齐本地系统的保护接口,数据接口,行情接口等等,包括平仓机制及功能接口都完全可用」。
背景:系统长期只在 okx/binance 上调教,gate 是第一次接入。

## 0. 唯一可信的事实来源

**Gate SDK v6.104.3 的官方字段注释**,不是既有代码的注释 —— 后者在 Rule 语义上恰好写反了。
`/root/go/pkg/mod/github.com/gateio/gateapi-go/v6@v6.104.3/model_*.go`。

四条决定性语义:

| 字段 | 官方语义 |
|---|---|
| `FuturesPriceTrigger.Rule` | **1 = 价格 >= 触发价**(要求触发价 > 最新价);**2 = 价格 <= 触发价**(要求触发价 < 最新价) |
| `FuturesInitialOrder.Size` | 全平 `size=0`;部分平空 `size>0`;**部分平多 `size<0`** |
| `FuturesInitialOrder.Close` | **仅**单仓模式全平时必须 true;部分平"不能设置 close,或 close=false" → **与 Size 互斥** |
| `FuturesPriceTriggeredOrder.OrderType`(只读) | `close-long-order`/`close-short-order`/`close-long-position`/`close-short-position`/`plan-close-long-position`/`plan-close-short-position` |

另外三条:
- `FuturesOrder.Text` 必须 `t-` 前缀、去前缀后 ≤28 字节、字符仅 `0-9 A-Z a-z _ - .`。
  **text 不合法 Gate 拒掉整张单**,所以任何标记失败都必须降级成裸 broker tag,而不是抛错。
- `Position.Leverage == "0"` 表示**全仓**,真实倍数在 `Position.CrossLeverageLimit`;正数表示逐仓。
- `Position.Mode` 为 `single`/`dual_long`/`dual_short`;**双向持仓模式下 size 符号不权威**,
  多头腿 size 恒为正,方向只能读 `mode`。
- Gate post-only 是 `tif="poc"`(PendingOrCancelled);`ioc` 是 taker only。
- Gate **没有**独立的保证金模式接口:全仓 = `UpdatePositionLeverage(leverage=0, cross_leverage_limit=N)`。
- Gate **没有**原生追踪止盈单类型(该 SDK 内不存在),所以 DD 必须走本地 managed。

## 1. 修掉的 12 个缺陷(按危害排序)

**① GetPositions 回传原生 symbol(最致命的一行)**。`trader_account.go` 原样返回 `pos.Contract`
= `BTC_USDT`,而同文件 `GetClosedPnL` 与 `GetOpenOrders` 都调了 `revertSymbol`。保护协调器按
symbol 字符串配对持仓与挂单,**永不相等 = 保护体系对 gate 持仓完全失明**。

**② Trigger.Rule 语义整体反了**。原代码注释把 1 写成 `<=`、2 写成 `>=`,四种
(方向 × 止损/止盈) 组合**全部下反** —— 多头止损会在价格上涨时触发。Gate 自身的
"触发价 vs 最新价"校验会拒掉其中一部分,剩下的会在错误方向成交。

**③ Size 与 Close 同时下发**。二者互斥。若交易所以 Close 为准,每一档 TP/SL 都变成全平,
TP1/TP2/TP3、dd1/dd2、部分锁利整套阶梯**塌成一张全平单**。

**④ 撤单忽略类型过滤**。原注释 "For simplicity, cancel all matching symbol orders",
`CancelStopLossOrders` 与 `CancelTakeProfitOrders` **是同一个函数**。上移保本止损会撤掉所有
止盈,重挂 DD 档会撤掉止损,撤到重挂之间仓位裸奔。这正是 Trader 接口注明"已修复"的那个 bug。

**⑤ 止损/止盈分类只看 Rule**。Rule 2 对多头是止损、对空头是**止盈**;原分类器把每个空头
止损报成止盈,reconciler 永远看到 `missingSL` 并**无限重挂**。修法:`classifyTriggerOrder`
依 (方向, rule) **联合**判定;方向优先取只读 `order_type`(全平单 `Size==0` 无符号可读,
只有它可靠),取不到就承认失败返回 `ok=false`,让调用方保留而不是猜。

**⑥ PositionSide 全程为空**。保护栈 18 处方向过滤写作
`if order.PositionSide != "" && !strings.EqualFold(...)` —— 空值**不是保守失败,而是直接关掉
过滤**,任何单都能配上任何方向的任何仓位。这类"空值即放行"的写法是本次最需要警惕的模式。

**⑦ leverage 以 int 回传**。通用层 6 处断言 `.(float64)`
(`auto_trader_decision.go:197,398`、`auto_trader_replace.go:93`、`auto_trader_risk.go:4575`、
`auto_trader_loop.go:751`、`position_snapshot.go:54`),int **全部静默失败**读成 0。

**⑧ 全仓杠杆未从 cross_leverage_limit 解析**。原样上抛 `leverage=0`,所有全仓仓位显示 0x,
而通用层以 `lev > 0` 为门槛 → 杠杆相关仓位计算与风控**静默失效**。同时补出 `mgnMode`。

**⑨ SetMarginMode 是空壳** `return nil`,`is_cross_margin` 完全无效。且通用层是
**先** `SetMarginMode` **再** `OpenLong`(后者内部调 `SetLeverage`),所以必须记录意图、
由 `SetLeverage` 统一编码,否则 leverage 调用总会写正数 = 逐仓,覆盖掉全仓设置。

**⑩ 平仓张数向零截断**。全平尤其危险:2001 张 × 0.001 = 2.001,再除回去是
**2000.9999999999998**,`int64()` 截断成 2000,**留 1 张残仓,而此时保护单已经撤了 = 残仓
无保护**。注意很多张数确实能精确往返(2999 就可以),这正是这类 bug 能躲过随手测试的原因。
全平改为直接取交易所整数张数(构造上精确),部分平改四舍五入,开仓侧保留截断(只会小于
风控额度)。

**⑪ quanto_multiplier 未校验**。除零得 `+Inf`,`int64(+Inf)` = `-9223372036854775808`,
**是负数**,于是被 `size <= 0 -> size = 1` 兜成**静默的 1 张单并报告成功**。
(我最初以为是"下出巨量单",实测后修正为这个更隐蔽的结果。)

**⑫ 触发价硬编码 `%.8f`**。粗于 8 位小数的合约被 Gate 直接拒单 —— 保护单**根本不存在**。
改为按 `contract.OrderPriceRound` 取整并按 tick 推导小数位。

## 2. 可选能力的静默降级陷阱

通用层对 Trader 接口之外的扩展方法全部通过**匿名接口断言**:

```go
if capable, ok := trader.(interface {
    SetStopLossTagged(symbol string, positionSide string, quantity, stopPrice float64, reasonTag string) (string, error)
}); ok {
    orderID, err := capable.SetStopLossTagged(...)
}
```

签名不匹配时 `ok=false`,**无错误、无日志,静默回退到非标记路径**。

gate 原实现的四个 tagged 保护方法签名是 `...) error`(缺 orderID 返回值),导致:
- `protection_execution.go:885,921,1069` 三处断言**静默失败**;
- `auto_trader_risk.go:3786` DD 档断言**静默失败**;
- 所有 gate 保护单的 `text` 字段永远是裸 broker tag `t-nofx`,**从未走到带机制标识的
  reason codec 路径**。

这比编译错误更隐蔽,因为非标记路径确实存在且能工作。

修法:四个方法改签名 `(string, error)`,并增加编译期接口守卫:

```go
var _ interface {
    SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error
    SetStopLossTagged(...) (string, error)
    SetTakeProfit(...) error
    SetTakeProfitTagged(...) (string, error)
} = (*GateTrader)(nil)
```

此模式在 `trader.go` 顶层**已有** `var _ types.Trader = (*GateTrader)(nil)`,
但 optional 方法无处可检,得手工补。

另外两处可选断言(order text 解码、tier 标注)会在本地字符串函数上失败,
属于"实现未到位、通用层先容错"的状况,不涉及交易所边界风险,放在后续迭代。

## 3. 补齐的 5 个可选能力

原能力表 `trader/protection_capabilities.go` 中 gate 行 **4 个 false**:

| 能力 | 原状态 | 现状态 | 达成路径 |
|---|---|---|---|
| `CanCancelByClientID` | false | **true** | 新增 `trader_parity.go:CancelOrder`,先查询委托列表+正则匹配 clientOrderID、再取 order.Id 撤单,不匹配返回成功(守幂等) |
| `CanDistinguishStopTP` | **false(实际 true)** | **true** | 原代码已有 `CancelStopLossOrders`/`CancelTakeProfitOrders`(虽然实现错了是同一个函数),所以这个字段当时就该填 true。现已修复实现。|
| `SupportsLimitOrders` | false | **true** | 新增 `PlaceLimitOrder(symbol, side, quantity, price, postOnly)`;postOnly→`tif="poc"`,false→`gtc`;拒绝 sub-contract 量,因 Gate 以**张数**为单位 |
| `SupportsOrderBook` | false | **true** | 新增 `GetOrderBook(symbol, depth)`,按 `quanto_multiplier` 换算回 base 单位,格式 `[][]float64{{price,qty}}` 与 okx/binance 统一 |
| `SupportsPartialClose` | false(单独字段) | → `CanPartialClose: true` | 原字段已 true;`PlaceLimitOrder` 与 `closePosition` 内已有张数计算逻辑,支撑部分平 |

另外两个有条件 true:
- `CanPartialClose: true` ← 原来就有,只是现在真能用了(因为平仓路径修完了)。
- `SupportsPostOnly: true` ← 新增的 `PlaceLimitOrder` 已处理 `tif="poc"`,限价委托生效。

两个 **false 且必须保持 false** 的能力:
- `SupportsNativePartialTrailing: false`
- `SupportsNativeFullTrailing: false`

理由:Gate SDK v6.104.3 内无任何 trailing 单类型,而通用层追踪判定写在
`auto_trader_risk.go:3734~3780` 的 `if pt.CanSetTrailingDrawdownTP() { switch exName {
case "binance", "bitget", "okx": ... }}`,gate **不在 switch** 内 → 一定走本地 managed,
这正是用户先前指令"不行就 managed 一直陪跑,双保险"的实际状态,不需要也不该改。

两个未实现且尚未声明的能力(留待后续或实测):
- 订单 ID 撤单:gate 有 `CancelFuturesOrder(settle, contract, orderId)`,
  但 generic 层没有 `CancelOrderByID` optional 接口。
- 委托 text 解析:generic 层有 `textDecoder` 断言,但对 gate broker tag `t-nofx` 做统一解码
  需要先在 reason_codec 注册 `t-` 前缀(当前只注册了 okx `x-` 与 binance `b-`)。

新增两个 tagged close 方法供 auto_trader_risk.go 内部锁利/DD 路径调用:

```go
func (t *GateTrader) CloseLongTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
func (t *GateTrader) CloseShortTagged(symbol string, quantity float64, reasonTag string) (map[string]interface{}, error)
```

内部共用 `closePosition(symbol, side, quantity, reasonTag)`,reasonTag 非空时编码进 `text`。

## 4. 破坏性测试的两个发现

**mutation testing 原理**:逐个重引入每个 bug(精确字符串替换),期待覆盖该 bug 的测试由
PASS → FAIL。任何"mutant 存活"(测试仍 PASS)说明测试有盲区。

构建的 Python harness `/root/.claude/jobs/gatefix/tmp/mutate.py`,17 个 mutant,结果
**caught 17 / missed 0 / harness-error 0**。

两个有趣的案例:

**案例 A — 数值选得不够刁钻**。最初为"2999 张全平会因截断留残仓"写的测试,mutant 存活。
手工应用 mutation 发现:2999 在 multiplier=0.001 下**确实精确往返**
(`2999*0.001 = 2.999`,`2.999/0.001 = 2999.0`)。这正是该类 bug 能长期潜伏的原因 ——
大多数数值都能往返,只有极少数落在浮点表示缝隙里。Python 手搜找到 **2001 确实损失**:
`2.001/0.001 = 2000.9999999999998`,截断成 2000。测试改用 2001。

**案例 B — 测试以错误的理由通过了**。`open-missing-multiplier-guard` mutant 存活,
手工应用后发现测试仍过,原因:**我的 badContractServer mock 不完整,它打断了 SetLeverage
调用(contract 查不到),Open 根本没走到除法那一步**。重写 mock 为完整健康响应,
加强断言为"零订单到达 wire",验证了 mutation 确实使测试红。

附带修正:我自己写的 `open-missing-multiplier-guard` 注释错了,原本写"除零产生巨量订单",
实测发现 Go 的 `int64(math.Inf(1)) = -9223372036854775808`(负数),会被
`if size <= 0 { size = 1 }` 兜成**静默的 1 张单**,更隐蔽。

## 5. 测试覆盖范围

6 个测试文件,共 60 个测试用例,0 fail / 0 skip:

- `trader/gate/protection_test.go` (137 行) — 纯函数:
  `triggerRuleFor`、`triggerOrderSide`、`classifyTriggerOrder`、`sanitizeGateText`
- `trader/gate/protection_wire_test.go` (236 行) — `captureServer` 抓实际请求体,
  覆盖 SL/TP tagged 四种 + size 符号 + 触发 rule + 价格格式化
- `trader/gate/account_wire_test.go` (408 行) — `positionServer` 校验 symbol 回转、
  杠杆/margin 解析、时间戳转换、双向持仓方向;`badContractServer` 测 multiplier 守卫
- `trader/gate/cancel_wire_test.go` (164 行) — `cancelServer` 记录撤单 id,
  覆盖 kindFilter (stop/tp/nil)
- `trader/gate/open_orders_test.go` (242 行) — 分类器、PositionSide/ProtectionRole 填充、
  **18 个机制 × 10 档(0~9)共 180 种 reason tag 全部不超 Gate 28 字节限制**
- `trader/gate/parity_test.go` (343 行) — 编译期接口镜像 + wire 测试(CancelOrder 双命名空间、
  PlaceLimitOrder tif、GetOrderBook 单位换算、status 映射)

mutation harness 17 个 mutant 全命中;`gofmt -l` 干净;`go vet` 干净;`go build ./...` 干净。

## 6. 仍需注意的事项

**部署前提 1 — Gate 仓户需要首充**。任何币种的 Gate U 本位合约账户首次使用前必须转入
至少一笔 USDT,否则 API 一律 `USER_NOT_FOUND`,这是 Gate 的惰性建户机制,
不是网络/密钥/IP 问题(三个候选端点全部 200,0.5~0.7s 响应)。

**部署前提 2 — 用户尚未下达部署指令**。前序指令是"提交后**暂不部署**",
本次指令只说"全部推进修复",并无新的"部署"明示,且系统为生产交易系统(高风险操作门类),
应等明确确认。

**已知 leakage(已在前序会话建议旋转密钥)**:两行 gate secret 明文分别留在
`data/nofx_20260726_095503.log:119764` 与 `data/nofx_20260726_143418.log:79355`。

**DD trailing 实际走 managed**,与 okx/binance 路径不同但功能对齐("不行就 managed 一直陪跑,
双保险"),三家行为一致(本地根据 tick 推移挂撤)。

**GetMarketPrice 对所有三家都返回 last price**,不是 mark price —— 保护协调器实际用的
mark price 来自 `GetPositions` 内的 `pos["markPrice"]` 字段,gate 现已正确填充
(修前留空 → 0.0)。`market_data_capabilities.go` 的 `QuoteMarkPrice: true` 对所有交易所都是
名不副实的,已在 gate 条目注释记录。

**`CancelOrder` 对"已消失委托"返回成功**(幂等性),与 binance 一致,okx 是三家中的
例外(会报 `NO_POSITION` 状态)。通用层已对 okx 单独处理,gate 无需改。

**时间戳单位**:gate `Position.OpenTime`/`UpdateTime` 是**秒**(SDK 注释确认),
通用层期待毫秒,已 ×1000。gate `FuturesPriceTriggeredOrder` 的 `create_time`/`finish_time`
也是秒(与 position 一致),但当前通用层未读 trigger 委托时间,暂未换算。

commit `bed9b92` 包含 13 个文件:6 个测试、4 个核心文件、reason_codec、parity、
capabilities,已推送至 `dev` 分支。

---

**本记忆固化于** commit `bed9b92`(2026-07-29),修复 gate 保护/数据/平仓全通路 12 个缺陷,
补齐 5 个可选能力,通过 60 个单元测试与 17-mutant 破坏性测试,**尚未部署**。
