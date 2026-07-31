# Production Deployment - Critical Information

## 2026-07-31 Deploy: HH/HL 结构闸门上线 + 窗口缺陷修复 (pid 345250)
备份 `/opt/webstack/nofx/nofx.bak-20260731-030515` (md5 `abca7a86df493492d5082b67209980a4`),
配置备份 `/tmp/cfg_backup_20260731-030515/`。新二进制 md5 `934a97bd357b2fccde92e14dbfa879e9`。
`MainPID=345250`, `NRestarts=0`, `active`, 4 traders loaded, health 200 @9ms。

**只对 claude-ct30 + GPT-ct50 开启**（`structural_alignment=true, lb=3, n=2, pct=0.0`，
强制模式非审计）。Claude15 **必须排除**——结构效应在 15m 上**反向**；BN 未开。

上线即刻验证（11:22:10 CST）：
```
🚫 Entry gate blocked UNIUSDT open_short: [structural_fit] ...
   | also failed: structural_alignment_missing {tf=1h dir=0 want=-1 bars=21 lb=3 n=2}
```
11:22:37 第二条**只有** `✗ structural_alignment_missing` 一项失败，是最干净的证据。

### ⚠️ 但 `bars=21` 暴露了一个真缺陷，当天即修
闸门读的决策上下文 K 线是**按 `primary_count` 为 AI prompt 裁剪过的**。
GPT-ct50 `primary_count=22` → 闸门只看到 21 根。按该窗口重跑回测：
**放行率 7.7%、增量 +0.160、p=0.1522 —— 闸门在 GPT-ct50 上等于无效**，
且 92.3% 的拦截是「凑不齐摆动点」而非「结构不对」。claude-ct30（29 根）反而正常
（+0.309, p=0.0002）。详见 `entry-structure-gate-backtest.md`。

修复：`structuralBars()` 在上下文 < 30 根时自取 120 根（60s 缓存）。
**取数失败弃权放行，绝不当拦截**（执行路径）；**只取 primary timeframe**，
不学 shadow gate 的跨周期兜底（15m 效应反向）。

回滚：`systemctl stop nofx` → `cp nofx.bak-20260731-030515 nofx` → 恢复
`/tmp/cfg_backup_20260731-030515/` 的策略配置 → `systemctl start nofx`。
若只想关闸门而不回滚二进制，把两个策略的 `structural_alignment` 置 false 即可
（配置项独立，`json_set` 直改 DB 有先例）。

**未修的更大缺口**：`rr_below_min` 用 AI 自报失效位算 RR，而实际执行的止损有
`floor_atr_mul=1.5` 硬地板，实测宽 2.9~4.3 倍。按真实止损重算，min_rr=1.5 通过率
93.7% → 24.5%。影响面过大，**必须先回测**，见 `entry-gate-ai-selfreport-audit.md`。

## 2026-07-29 Deploy: 超额敞口收敛 v1.17.3 (pid 84853, commit ba2a2e9)
备份 `/opt/webstack/nofx/nofx.bak-20260729-175747` (md5 921f4a0b04e7bd96d824949719fe5430),
新二进制 md5 `17f3abfb83cd8ff7935b6d03a62d2721`。构建 `go build -o /tmp/nofx_v1173 .`。
`MainPID=84853` == `pgrep -x nofx`,`NRestarts=0`,`ActiveState=active`。

修的是**被账本屏蔽的委托泄漏**(详见 `unified-protection-system.md` v1.17.3 段)。
部署前实测危害:ETHUSDT 空头 4 张活 trailing、**其中 3 张都是全平**,而协调器一直报
`staleTrail=0` —— 泄漏的 armed 记录让 `classifyProtectionOrder` 标它们 `expectedDynamicOwner`,
成了泄漏委托的**保护伞**。四张单同属 `dd1`,只是 ATR 倍数被重解析过
(ATR(1h)=16.176366 anchor=1890.56 → 1 ATR=0.85564%):
1.0268%=1.20ATR / **0.7701%=0.90ATR** / 1.5401%=1.80ATR(30%) / 1.2835%=1.50ATR(当前设计)。
**谁先触发谁平掉整仓 → ETH 会在回撤 0.7701% 被整仓平掉而设计是 1.2835%,提前 40%。**

上线即刻效果(第一个 reconcile 周期 01:58:15):
- `🧾 Over-claimed trailing exposure on ETHUSDT short: 4 live claimed trailing orders for
  2 configured tiers` ×2,retired `cb=0.007701` 与 `cb=0.010268` 两条;
- 21 秒后 `✓ Canceled algo order by id for ETHUSDT: 3784966212015259648 / 3784785480328314880`
  —— **端到端闭环**:退认领 → 交回既有 stale 清理路径 → 交易所真撤掉;
- ETH `claimedTrail=4 dynamicOwner=4` → `2/2`,保留的正是 dd1 1.2835% + partial 1.5401%/30%;
- 账本 12 armed → 10 armed + 2 superseded;5 个仓位全部 `armed=2 oids=2 ratios=[30,100]`;
- ERROR 0,panic 0,WARN 6 = 我这 2 行有意告警 + 4 行无关的 MCP 端点 503 回退;
- `/api/health` 200 @2.1ms,load 0.50,RSS 81MB。

**日志文件会按日期滚动**:排查时先 `ls -t data/nofx_*.log | head -1`。我这次差点误判"修复没生效"
—— 旧文件末尾是上一个进程的 `System shut down`,新进程写的是 `nofx_2026-07-30.log`。

**订正我自己的一次误读(值得记,省下一轮的时间)**:我一度把 WLDUSDT
`dynamicOwner=3 claimedTrail=2` 当成"有一张活单没人认领"的反向缺陷。**是我读错了** ——
完整字段是 `dynamicOwner=3(trail=2 be=1)`,那个 3 含 1 张保本止损,而 `claimedTrail=2`
恰好等于 `trail=2`。**`dynamicOwner` 与 `claimedTrail` 分母不同**,不能直接相减。
WLD 是健康的。另注意 WLD 同时被两个交易员持有(OKX entry 0.3049 算法单 ID `37857509…`
与 Binance entry 0.3036 ID `20000013…`),每个仓位各 2 条 armed、orderID 互不相同,也正常。

回滚:`cp nofx.bak-20260729-175747 nofx` + `git revert 17e0f88`。

## 2026-07-29 Deploy: 多档 DD 档位标识真正生效 v1.17.2 (pid 66117, commit a1114fc)
备份 `/opt/webstack/nofx/nofx.bak-20260729-152541` (md5 7aceafce6e614d9fcd1766f3de270f81),
新二进制 md5 `921f4a0b04e7bd96d824949719fe5430`。`systemctl stop` → 复制 → `systemctl start`
(必须先 stop,否则 `cp` 报 `Text file busy`)。`MainPID=66117` == `pgrep -x nofx`,`NRestarts=0`,
`is-enabled=enabled`。构建:`go build -o /tmp/nofx_v1172 .`(main 包在仓库根,**不是** cmd/nofx)。

修的是 v1.17.1 档位标识**在生产上等于死代码**这件事(4 个根因 D-1…D-4,详见
`.ai-memory/unified-protection-system.md` v1.17.2 段)。核心:`drawdownTierTagIndex` 拿
ATR 换算后的规则去和原始配置倍数做浮点相等比较 → 永远返回 0;`reclaimLiveDrawdownTrailingOrders`
是唯一漏掉 `resolveDrawdownRulesATR` 的消费者 → 档位↔委托反向映射 + `min=2.5000` 脏指纹,
`supersedeOlderArmedRecords` 的窄匹配永远碰不到 → 4 条 armed 记录抢 2 个委托、每 ~20s 重写一次。
生产 5 个策略里 **4 个是 ATR 单位**,而 v1.17.1 的 13 个测试全用百分比 fixture,测的是生产
永远不走的分支。教训:测试 fixture 的单位必须对齐线上配置的单位。

上线即刻效果(第一个 reconcile 周期 23:28:51,`basis: exchange order shape` 判主):
- `…445192 → native_trailing close=100% act=0.292060`、`…445212 → native_partial_trailing
  close=30% act=0.285137`,反向映射消失,两处冲突一次收敛。
- 账本 PRE `orders=14 armed=16 conflicts=2` → POST `14/14/0`,**交易所 orderID 零丢零增**
  (没有撤单重挂抖动)。11 分钟后含新开 SPCX 为 `armed=15 / 15 distinct / conflicts=0`。
- `Reclaimed live drawdown trailing order` 从修复前 980 行 → 重启后 **0**;
  `tier tag disagrees` 0;ERROR 0;WARN 0;panic 0;`unprotected/phantom` 0。
  load 1.22,RSS 78MB,`/api/health` 200 @0.8ms。
- 唯一的 trailing 撤单是正常语义:`Immediate trailing canceled (replaced by tier trailing)`。
注意:OKX `SetTrailingStopLoss` 有传 `algoClOrdId`(带 `T<N>`),但日志行没打 clOrdId,
所以线上 `T<N>` 只能靠单测 + reclaim 解码路径证明,日志里看不到——要查得读交易所侧委托。
回滚:`cp nofx.bak-20260729-152541` 回去 + `git revert a1114fc`。

## 2026-07-29 Deploy: BN 回撤档修正 + 切换交易员去阻塞 (pid 27043, commit 90de970)
备份 `/opt/webstack/nofx/nofx.bak-20260729-114912` (md5 62772c95…),新二进制 md5 7aceafce…。
**这次同时把进程交给了 systemd**:此前 pid 5515 是 `./start.sh` 手起的,systemd 不持有它,
`Restart=on-failure` 形同虚设。现在 `MainPID=27043` == `pgrep -x nofx`,自启+自愈才真生效。

三个问题(用户报的两个 + 自查挖出的一个):
  1. **BN 面板 DD 档被画到入场价下方**(用户截图:entry 64367.60,DD-1 -0.38% / DD-2 -0.22%)。
     根因是**交易所语义不对称**,不是策略错、不是交易所单子错:
     `applyMatch` 会用回读的 `trigger_price`(= `OpenOrder.StopPrice`)覆盖 `activationPrice`。
     - OKX `trader_orders.go:1494` 未激活时 `stopPrice := activePx` → 覆盖成激活价本身,无害。
     - BN 算法单分支 `stopPrice := algoOrder.TriggerPrice` = **当前跟踪止损价**(跟峰值棘轮上移),
       根本不是激活门槛。拿它当锚,`execution = 跟踪价×(1-callback)` 等于把 callback 减第二次。
     所以三个 OKX 交易员看不到,**只有 BN 有** —— 用户的观察是对的。
     修法只落在展示口径:`execution_price` 改用 `firstPositive(plannedActivationPrice, activationPrice)`。
     **刻意不改 BN 适配器**:`ActivationPrice=triggerPrice` 是 `nativeTrailingEffective` 和幻影激活
     守卫依赖的既定契约(见 `auto_trader_risk.go:1876`),动它会波及强平/撤单;而 `execution_price`
     只被 `PositionProtectionPanel` 消费,无风险路径读它。
     反向验证:还原代码精确复现 64121.87(-0.38%)/64225.10(-0.22%);修后 +0.52%/+1.48%,
     与 `protection_plan_snapshot` 早已落库的正确值一致(该路径传 peak=0 所以没中招)。
  2. **切换交易员要十几秒**。`apiPositionsSnap` 只有真实 API 请求能写,监控循环走适配器缓存
     从不刷它 → 切到 >5min 未看的交易员必走阻塞分支,在 1 核机上和 4 个监控循环抢 CPU,
     实测 `/api/positions` 16.39s / 10.22s,而 OKX 调用本身 <1s(18:33:54→18:34:00 有 6s 死区
     = 排队而非交易所慢)。前端必须先拿到 symbol 列表才能并行取挂单,于是串成用户可见延迟。
     修法:监控循环里 `WarmPositionsSnapshotIfStale`,按年龄节流 `apiReadWarmAge=stale/2=2.5min`,
     异步不阻塞。**没有放宽写侧新鲜度**(`apiReadFreshWindow` 仍 8s),否则平仓对账会误判新仓。
  3. **自查挖出的停机级隐患**:`fetchAndProjectPositions` 对适配器返回的 map 用**裸类型断言**,
     且跑在 `refreshPositionsAsync` 的裸 goroutine 里。实测(`/tmp/panic_probe.go`)
     **`singleflight.Do` 会重新 panic 而不是吞掉** → 交易所返回一个畸形字段就能杀掉整个 nofx。
     修法:逗号-ok 断言 + 跳过坏行告警 + `recover()`。这不是用户报的,但比那两个都严重。

门禁:`go build ./...` / `go vet ./...` 干净,`go test ./...` 全绿(trader 46.1s),
gofmt 与基线一致(未动仓库既有的 38 行基线差异),前端 `tsc --noEmit` 0、`vitest` 143/143。
8 个新测试全部反向验证过(还原修法即变红)。
上线验证:
  - 优雅停机 54s 打出 `System shut down safely`(~5GB SQLite 完成 checkpoint)。
  - **14 条 armed 保护记录 100% 原样收养,交易所单号 0 变化** → 没有撤单重挂 churn。
  - 6 个持仓全部 `verified=true`、`staleTrail=0`、`unexpectedTP=0`/`unexpectedSL=0`(70/70 轮)。
  - 4 traders 全载,0 ERROR / 0 panic / 0 WARN / 0 限频,畸形仓位守卫 0 触发,RSS 68MB。
  - 负载 **1.70 → 0.60**:见下条 systemd 缺陷。
**我自己引入又修掉的缺陷(务必记住)**:`nofx.service` 的 `ExecStartPre` 双启动守卫退出 1 会被
`Restart=on-failure` 当成崩溃重试,而守卫在手起进程活着时**永远**拒绝 → 空转 **569 次重启**,
每 10s 拉一个 shell,在 1 核机上白烧 CPU(正是我在查的那个延迟的帮凶)。
已加 `StartLimitIntervalSec=300` / `StartLimitBurst=5` 封顶,并 `systemctl reset-failed` 归零计数。
教训:给 unit 加 `ExecStartPre` 硬失败守卫时,必须同时设启动限速,否则守卫本身变成 CPU 泄漏。

## 2026-07-29 Deploy: 保本止损(BE)按仓位身份+档位收敛 (pid 2643455, commit 6ba8900)
生产实况:claude/WLDUSDT SHORT 在交易所侧堆出 **4 张 break_even_stop**
(0.3604/0.3594/0.3568/0.3539),而配置只有 2 档 BE(0.5×ATR / 1.5×ATR)。
四层根因(全部修掉,不是补洞):
  1. `checkPositionDrawdown` 的 entryPrice 取自实时 `GetPositions()`,加仓会推动均价
     (0.3622→0.3691→0.3697,约 1.9%)→ `entrySamePosition` 的 0.05% 容差判成"新仓位"
     → frozen-ATR 缓存未命中 → 按当时 ATR 重新冻结。
     修法:`store/frozen_atr_state.go` 增 `PositionCreatedTime`(交易所 cTime,加仓不变)
     作为权威身份,0=未知才退回 entry 容差。结构止损同一条规则——它的失效更糟:
     reconcile 路径 `allowCompute=false`,未命中直接返回 (0,false),结构止损会从计划里
     **静默消失**。
  2. 分类器用 `breakEvenArmed bool` 做额度——布尔只容一张,配两档时第二档永远被判
     `stale_bot_duplicate`。修法:新增 `trader/break_even_ownership.go`,照抄仓库已经
     解决过同类问题的 `nativeTrailingOwnership`(认领单号集合 + Armed 单独留给归属判定)。
     额度按**所有状态**的档位数算(扛重启窗口),单号只从 armed 认领。
  3. `store/dynamic_protection_state.go` 把 `break_even_stop` 当只按 protectionType 的
     单例组 → arm BE2 时把 BE1 降级成 `replaced`,而 BE1 的单还活着。修法:单例键
     加 stage 维度(`group|stage`),并加 `SaveDynamicProtectionRecordByKey` 原地更新。
  4. BE 价格跟着实时均价走,却**只挂不撤**。修法:新增
     `trader/break_even_tier_replace.go`,挂新单前按**单号**撤同档旧单(三重 AND 门:
     记录属本 trader/BE/armed/同档 → 有 ExchangeOrderID → 该 ID 仍在 openOrders 且
     形状确认且偏离超 0.05%)。撤单失败只告警仍挂新单,保证该档不裸奔。
     不做 by-reason 撤单:`store/reason_codec.go` 里所有档位的 mechanism code 都是 `BE`,
     按 reason 撤会连坐兄弟档(v1.16.5 那一类)。
自查又发现两个"我这次填了单号才可能出现"的新洞,一并修掉:
  (a) 认领集合可能收进同 symbol/side **已平仓位**的单号(此前所有 BE 单号都是 ""
      所以不可达)→ 加仓位身份过滤;
  (b) 刚重启时每仓位只有一档有 armed 记录 → 额度会读成 1 而实际 2 张单在挂
      → 档位数按所有状态计。
反向验证(每个修法都先证明测试能抓住它):撤销 tier-aware 单例 → 2 个 store 测试红;
去掉同档判断 → 会误撤兄弟 `be2_order`;强制 `isBreakEvenTaggedOrder` 返回 true →
会误撤 ladder 单;**去掉 replace 调用 → 精确复现生产缺陷 `got [50149.99, 50300,
51152.99, 51306]` = 2 档 4 单**;档位数退回只数 armed → 额度 1 而非 2。
另加对抗场景:撤单失败仍挂新单(不裸奔)、无偏离时连轮 0 撤单(幂等)、单档语义不变。
门禁:`go vet ./...` 干净,`go test ./...` 全绿,`go build -o` 成功,gofmt 与基线一致
(28 files, +1895/-116)。无 web/ 改动,不需要前端重建。
上线验证(部署前后同一指标对比):
  - `be=` 分布 部署前 be=0×3519 / be=1×4961 / **be=2 一次都没有**(8480 行);
    部署后 be=0×76 / **be=2×38** —— 第二档终于被正确认领。
  - `stopTolerated` 部署前 1342 次(容差分支在掩盖撤单);部署后每轮 1 次,只对
    SKHYNIXUSDT 那张**部署前遗留**的孤儿止损。
  - 部署后 `Break-even stop applied` 0 次、`BE tier replaced` 0 次、cancel 0 次
    → 挂撤循环消失(不是被掩盖)。
  - 42 条 frozen-ATR 记录原本 `position_created_time=0`,部署后陆续打出
    `🔒 Frozen ATR: adopted position identity cTime=...` 完成收养。
  - 4 traders 全部加载,27 条保护记录恢复,api/web=200,0 ERROR / 0 panic,
    全部持仓 `state=protected verified=true`(114/114)。
已知边界(如实记录,不是遗漏):
  - **Binance 不返回 createdTime**(恒为 0),所以 trader BN 上这条身份修法会退化回
    旧的 entry 容差;WLD 那个缺陷本身在 OKX。
  - 加仓后交易所侧保护单的**数量**不会跟着放大(GPT/SKHYNIXUSDT 实况:仓位 0.035,
    亏损侧止损只 0.017 = 49%)。这是仓库**既有的、刻意的**取舍,不是这次引入的:
    "按单号解析自己的单、不churn"正是 2026-07-27 那次单子摞叠事故
    (BN CLUSDT 2.43+0.73+1.22)的修法,改成每次加仓都重挂就会把它请回来。
    残余风险由 managed 陪跑兜住——managed 记录 `close_ratio_pct=100`、`quantity=None`,
    按**执行时的实时仓位**平 100%,不吃这个陈旧量。未经用户决定不动这条。
  - SKHYNIXUSDT 上那张 1109.99/0.013 孤儿止损是部署前残留:它的单号当年没落库,
    所以新的按单号撤单逻辑看不见它。多一张止损=多一层保险,reconciler 的容差分支
    (protection_reconciler.go:318-320 有注释)刻意放行;多一张**无人认领的 trailing**
    才会真多平仓,那条必须落到撤单路径。

## 2026-07-23 Fix: positions/history perf — "Network error" toast root cause (pid 2035360)
BN (and every) trader panel kept popping a "网络错误/Network error" toast because
`GET /api/positions/history?limit=50` took 12-26s, past the frontend's 30s axios
timeout (httpClient.ts: `!error.response` → Network error toast).
Two compounding causes:
  1. N+1 JSON re-parse: full enrichment loaded the SAME decision record many times
     per position (entry 3x, exit 2x, per close-event, + aiSL/aiTP pass) and
     re-unmarshalled its JSON blobs each time → 300+ GetRecordByCycle loads at limit=50.
  2. Missing composite index: each GetRecordByCycle linear-scanned thousands of rows
     (only had idx_decision_records_trader_time on (trader_id, timestamp)).
Fixes (commit 2ad4216, pushed origin/dev):
  - api/handler_order.go: per-request memo for GetRecordByCycle + built review refs
    (recordByCycleMemo / reviewRefMemo). Also skip stale-snapshot reconcile in
    handlePositions (PositionsSnapshotFresh guard) to avoid closing a freshly-opened
    position off a stale-while-revalidate read.
  - store/decision.go: added GORM composite index idx_decision_records_trader_cycle
    on (trader_id priority:1, cycle_number priority:2).
Rollout: memoized binary deployed as pid 2035360 (14s→9-15s, not enough alone), THEN
the composite index was created manually in the prod DB → endpoint dropped to ~1.5s
(BN 1.79s, claude 1.51s, Claude-R 1.22s, GPT 0.96s). The GORM tag now persists the
index across future AutoMigrate; the prod DB already has it. No further deploy needed
solely for the tag.

## 2026-07-20 Deploy: structural backup wick-immunity fix (pid 1449553)
Fixed a bug where ratchet #1 could park a PHYSICAL backup stop right on the
close-confirm structural level, destroying its wick-immunity. Root cause:
`computeTrailBoundary` decides the ratchet from the pre-clamp swing, but
`clampStructuralBoundary` can pull the tight level back to the entry-floor →
tight==prevBoundary==backup, and a resting order gets placed at the close-confirm
price (observed live on TRUMP long @1.6018 and WLD short @0.3796).
Two guards added in `trader/structural_sl_guard.go`:
  1. `applyStructuralTrail`: only count a ratchet when the clamped tight is
     STRICTLY tighter than prevBoundary; otherwise no-op (no ratchet, no backup).
  2. `syncStructuralBackupStop`: only place the physical backup when it is
     genuinely LOOSER than `rec.TrailBoundary` (below tight for long, above for
     short); else desired=0 → cancel any existing + place nothing.
Tests: 4 new cases in `structural_backup_stop_test.go` (SkipsWhenCoincidesWithTight,
SkipsWhenTighterThanTight, PlacesWhenLooserThanTight, LongWickImmunity) — all pass.
On restart the new guard AUTO-CANCELED the stale TRUMP backup (OKX confirmed
`Order cancelled 3756612587153416192`); SKHYNIX/SOL backups (looser than tight)
correctly retained. Deployed via ./start.sh path; 4 traders, 0 errors.
NOTE: start.sh pgrep fixed this deploy — was `pgrep -f "/opt/webstack/nofx/nofx"`
which false-matched the `cp .../nofx` command line and refused to start (left nofx
DOWN briefly). Now `pgrep -x nofx` (exact process-name match).

## Production Environment Location
**NEVER deploy to `/root/projects/nofxmax` directly in production!**

### Correct Production Paths
- **Binary location**: `/opt/webstack/nofx/nofx`
- **Database**: `/opt/webstack/nofx/data/data.db` (3.9GB+, contains 4 active traders)
- **Working directory**: `/opt/webstack/nofx/`
- **Environment config**: `/opt/webstack/nofx/.env`

### Development Environment
- **Source code**: `/root/projects/nofxmax/`
- **Git operations**: Perform in `/root/projects/nofxmax/`
- **Build binary**: `/root/projects/nofxmax/nofx` (then copy to production)

## Deployment Procedure

1. **Build in development directory**:
   ```bash
   cd /root/projects/nofxmax
   /usr/local/go/bin/go build -o nofx .
   ```

2. **Stop the old process FIRST and confirm it exited**:
   ```bash
   # CRITICAL: the running process holds the binary file open. You MUST kill it
   # and confirm death BEFORE copying, or cp fails with "Text file busy" and the
   # old (unpatched) binary keeps running while you think you deployed.
   old_pid=$(ps aux | grep -E "/opt/webstack/nofx/nofx|\./nofx" | grep -v grep | grep -v claude | awk '{print $2}')
   kill -9 $old_pid
   sleep 3
   # Confirm it's actually dead (do NOT proceed until this prints CONFIRMED DEAD):
   ps -p "$old_pid" >/dev/null 2>&1 && echo "STILL ALIVE — retry kill" || echo "CONFIRMED DEAD"
   ```

3. **Copy to production ONLY after the process is dead**:
   ```bash
   cp /root/projects/nofxmax/nofx /opt/webstack/nofx/nofx
   # Verify the binary mtime updated (proves the copy actually happened):
   stat -c '%y' /opt/webstack/nofx/nofx
   ```

4. **Start from production directory** (use start.sh — it sets OOM protection):
   ```bash
   cd /opt/webstack/nofx
   ./start.sh
   ```
   `start.sh` wraps `nohup ./nofx > /tmp/nofx.log 2>&1 &` and then sets
   `oom_score_adj=-800` on nofx (and -500 on the s-ui Binance proxy) so the OOM
   killer targets idle agent processes first, never the trading process. This
   matters because RAM is only 1.9G and the box has hit OOM before (2026-07-06).
   oom_score_adj is per-process and resets to 0 on restart, which is exactly why
   it lives in the start script — every deploy re-applies it automatically.

   Manual fallback (if not using start.sh):
   ```bash
   cd /opt/webstack/nofx
   nohup ./nofx > /tmp/nofx.log 2>&1 &
   echo -800 > /proc/$!/oom_score_adj   # protect trading process from OOM killer
   ```

5. **Verify**:
   ```bash
   # Check process is the NEW one (started just now, not hours ago)
   ps aux | grep "\./nofx" | grep -v grep | awk '{print $2, $9}'

   # Check logs for trader count (should be 4)
   grep "Total loaded trader configurations" /tmp/nofx.log

   # Check API
   curl http://localhost:8080/api/health
   ```

## Common Mistakes to Avoid

❌ **WRONG**: Running from `/root/projects/nofxmax/` → uses empty dev database
❌ **WRONG**: `/app/data/nofx-hotfix` → old binary path, doesn't exist
❌ **WRONG**: `cp` before killing the process → "Text file busy", old binary keeps
   running. ALWAYS kill + confirm dead, THEN copy. Verify with `stat -c '%y'`.
❌ **WRONG**: Chaining `kill ... & cp ...` in one backgrounded command → cp runs
   before the kill takes effect. Run kill, confirm death, then copy as separate steps.
✅ **CORRECT**: Running from `/opt/webstack/nofx/` → uses production database with 4 traders

## Deployment Failure Signature (2026-07-16)

Symptom: you "deployed" but the fix isn't live. Root cause: `cp` failed silently
with "Text file busy" because the old process still held the binary. The old
process kept running for ~20 min. Always check the binary mtime AND the process
start time after deploy — if the process start time is older than your `cp`, the
copy didn't take and you're running stale code.

## Frontend Deployment (nginx proxy gotcha, 2026-07-17)

The frontend is a Docker container (`nofx-frontend`, compose project `nofxmax`,
`/root/projects/nofxmax/docker-compose.yml`), serving a **baked-in** image (no bind
mount). Deploy = rebuild image + recreate container:
```bash
cd /root/projects/nofxmax
docker tag nofxmax-nofx-frontend:latest nofxmax-nofx-frontend:rollback-<desc>  # rollback point
docker compose build nofx-frontend            # runs tsc && vite build inside image
docker compose up -d --no-deps nofx-frontend  # --no-deps: backend is a HOST PROCESS, not a container
```

⚠️ **CRITICAL nginx regression on every rebuild**: the repo `nginx/nginx.conf` has
`proxy_pass http://nofx:8080/api/`, which assumes a containerized backend. In THIS
production the backend is a **host process** (`./nofx` on `:8080`), reachable from the
container only via the docker bridge gateway (`172.20.0.1:8080`, confirm with
`docker inspect nofx-frontend --format '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}'`).
A fresh build re-bakes `nofx:8080` → `/api/` returns 502. After every recreate:
```bash
gw=172.20.0.1  # verify per above
docker exec nofx-frontend sed -i "s#proxy_pass http://nofx:8080/api/;#proxy_pass http://$gw:8080/api/;#" /etc/nginx/conf.d/default.conf
docker restart nofx-frontend   # restart (NOT recreate) keeps the sed edit + reloads clean
```
Verify: `curl -w '%{http_code}' http://127.0.0.1:3000/api/health` must be 200 (not 502).
(`nginx -s reload` right after start fails with "invalid PID number" — use `docker restart`.)

## Database Verification

Expected trader count: **4 active traders**

```bash
# Check database
sqlite3 /opt/webstack/nofx/data/data.db "SELECT COUNT(*) FROM strategies WHERE is_active = 1;"
# Should return: 2 (strategies table) + underlying traders = 4 total
```

If startup log shows "0 traders", you're using the **wrong database**.

## Deployment 2026-07-19 — protection: min_profit=0 gate + rolling 2-level backup stop

Commit `b50ef0d` (8 live-path files only; backtest/research uncommitted per constraint).
- Code: `min_profit_atr<=0` DISABLES the trail min-profit gate (was coerced to 1.0);
  default now 0. Rolling 2-level structural backup: superseded ratchet boundary held
  as a physical intrabar stop one step behind the close-confirm level, 4.5-ATR backstop
  untouched, guard cancels prior backup by id on roll. FrozenATRRecord +BackupBoundary
  +BackupOrderID; ProtectionPlan +AllowedExtraStopPrices (reconciler tolerates, no churn).
- Live config flip (GPT `4658ad10`, BN `6fd686fe`): `trail_on_profit=true`,
  `trail_min_profit_atr=0` via json_set. Root-caused the ratchet deadlock
  (on_profit=false + min_profit=1.0 could never arm).
- Rollback artifacts in `/tmp/deploy_backup/`: `nofx.20260719_084641.bak` (old binary),
  `gpt_config.json`/`bn_config.json` + `RESTORE.sql` (config revert). Code: `git revert b50ef0d`.
- Deployed clean: PID 1416334, 4 traders, 0 errors. New path activates on next GPT/BN
  position whose ratchet arms (both had no open positions at deploy time).

## Deployment 2026-07-20 — entry-gate: real closed-candle fake_retest confirmation

Commit `c7482b9` (2 live-path files: `trader/entry_gate.go` +
`trader/fake_retest_confirm_test.go`; backtest/research uncommitted per constraint).
- Replaced the near-no-op 0.15% geometric proxy in the structural_fit
  `fake_retest_trap` gate (block 2f) with real closed-candle confirmation on the
  timeframe ONE STEP BELOW primary (BN 1h→15m). Rule from confirmation-TF backtest
  (claude/GPT/BN, 48h wall-clock): for long-at-support / short-at-resistance within
  0.30% of the anchor, require a post-touch candle that CLOSED on the correct side
  (hold) OR a wick-rejection (wick>=1.5x body). Confirmed→pass; touched-but-
  unconfirmed / arriving on forming bar→block. Missing conf-TF candles→old distance
  block (graceful degradation). Conf TF = largest selected_timeframes strictly below
  primary, else primary; forming last bar dropped.
- New helpers: `tfMinutes`, `confirmationTFBars`, `fakeRetestConfirmed`,
  `isWickRejectBar`. Tests: 9 subcases (long/short hold+wick confirm, unconfirmed,
  no-touch, TF selection, forming-bar drop) — all pass; full trader suite green.
- Also earlier this session: BN prompt v2 (全量分析/逐币 + 低周期收盘确认, 主周期1h→15m
  例子) was written to strategy `6fd686fe` and ACTIVATED via UI stop/start (verified
  live at cycle 1404, old 60分就够/过去30小时 gone). Backup:
  `.tmp_prompt/bn_config_backup_20260720_052415.json`.
- Rollback: old binary `/tmp/deploy_backup/nofx.20260720_071848.bak`; code
  `git revert c7482b9`.
- Deployed clean: PID 1508058, 4 traders, 0 errors. Positions re-synced identically
  (GPT 3, claude 2, BN 0, Claude-R 0). Gate change affects new entries only.

## 2026-07-22 Deploy: Binance native trailing true root-cause fix (pid 1805235)
Commit `c41409f` (+ earlier trailing work 3e2d2bb/b307073/a7d841a). Fixes BN
native trailing triggering AT ENTRY (immediate stop-out → unresolved_exchange_close
+ profit erosion).
- TRUE ROOT CAUSE (not a Binance bug): go-binance v2.8.9 sent the trailing
  activation param under the WRONG key `activationPrice`; Binance silently ignored
  it and defaulted the trail to the order-time market price → armed at entry,
  triggered on first adverse tick.
- Fix: go.mod bump v2.8.9 → **v2.8.10** (renames activationPrice → activatePrice).
  `setTrailingStopLossCore` rewritten onto the ALGO endpoint (/fapi/v1/algoOrder):
  classic /fapi/v1/order now rejects TRAILING_STOP_MARKET with -4120. Bind
  `.ActivatePrice()` explicitly; read-back verification retained as a regression
  guard (cancel + local-monitor fallback on divergence/missing).
- Also fixed CloseLong/CloseShort quantity==0 to resolve size via cache-free
  `execPositionAmt` (symbol-scoped GetPositionRisk) instead of the 15s
  GetPositions() cache — a stale read had reported "no position" ~10s after a live
  fill (naked-position risk). Mock now honors the symbol query filter.
- LIVE PROOF: BN PLTRUSDT long entry 127.535, native trailing armed via Algo Order
  with exchange-confirmed **activation=130.5566 (+2.37% above entry)**, callback
  1.4%, state native_trailing_full. Old bug would have armed ≈entry.
- Left as-is per product decision: issue 1 (algo min-notional ~18-20 USDT → graceful
  local-monitor degradation, -4136); issue 2 (ListOpenAlgoOrders lacks activatePrice
  → GetOpenOrders approximates from triggerPrice, display only). Recorded in
  `.ai-memory/binance-native-trailing.md`.
- Rollback: old binary `/tmp/deploy_backup/nofx.20260722_154058.bak`; code
  `git revert c41409f` (+ `go mod tidy` back to v2.8.9 if needed).
- Deployed via start.sh; new PID 1805235, 4 traders loaded, health ok, no panics.
  BN trader restarted (was stopped for testing). Backend-only — no frontend rebuild.

## Last Updated
2026-07-29 - v1.17.2 多档 DD 档位标识真正生效（pid 66117, commit a1114fc；账本 16→14 条、冲突 2→0、reclaim churn 980→0）

## 2026-07-20 Deploy: unified SL band 1.5/2.5 + max-hold disabled + reward-ATR≥1.0 (pid 1556740)
Backtest-driven risk tuning, ALL 4 traders identical:
- structural_sl: floor 1.5 (kept) / backstop 4.5→2.5 / fallback 3.0→2.5
- ladder rule structural stop_loss_pct 4.5→2.5 (no-structure fallback)
- risk_control: max_hold_hours 18→0 (DISABLED), max_hold_profit_exempt_pct 2→0
  (24h/-1.5% time_stop still covers losing trades)
- entry_gate.min_reward_atr_mul 0.8 (BN 0.65) → 1.0 (block target<1.0×ATR)
Code: store/strategy.go StructuralSLConfig.WithDefaults backstop 4.5→2.5, fallback 3.0→2.5 (floor default kept 1.5).
Config backup: /tmp/cfg_backup_20260720_181623/{sid}.json (4 files).
Backtest basis: floor MUST stay 1.5 (dropping to 1.2 cost claude -15, GPT worse).
  backstop optimal is trader-specific (claude 2.5 best +18.76, GPT 3.0 best/2.5=-6.5,
  Claude-R 2.4, BN wants 4.5) — user chose unified 2.5 knowing GPT takes the hit.
  max-hold profit-exemption was net-harmful everywhere; disabling entirely best for
  claude(+7.9)/BN(+10.8), neutral GPT(+3.8), no-op Claude-R.
Deployed via start.sh; 4 traders loaded, 0 errors, health ok.

## 2026-07-20 Deploy: min-SL-distance gate 0.2→0.8 (pid 1563139)
Config-only (no rebuild). ALL 4 traders: entry_gate.min_sl_distance_atr_mul 0.2→0.8.
Blocks entries whose AI-declared invalidation is <0.8×ATR from entry (too-tight stops
that normal noise sweeps). Backtest (realized PnL, structural-target subset):
  claude -31.06 → +21.57 kept (blocked tight-stop losers -52.63)
  Claude-R -11.62 → -1.09 kept ; GPT neutral +2.01 ; BN inverted but tiny/noisy (44).
Frequency: keeps ~45-50% of structural entries (halved). User accepted "少而精".
Config backup: /tmp/cfg_backup_20260720_191738/. Restart via start.sh, 4 traders, ok.
NOTE (future, more surgical): recompute RR against ENFORCED stop (1.5×ATR floor) instead
of AI nominal invalidation — roots out the "tight declared SL inflates RR" (SPCX class).
