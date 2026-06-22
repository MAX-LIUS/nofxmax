# 统一保护系统 - AI 记忆文档

> **状态**: 生产运行 | 归因已修复+重建 | ATR保护框架已上线(路线A) | reconciler churn 监控中
> **更新**: 2026-06-22 (v1.8.0 — 每维度三选一+DD ATR;churn 部分修复)
> **版本**: v1.8.0

---

## 🧭 恢复入口 (RECOVERY ANCHOR) — clear 对话后先读这一节

### 我是谁 / 在做什么
我是 Kiro,在帮用户把 **Claude-R 交易员**做成 Claude 的"升级版":核心是用 **ATR 自适应保护**替代固定百分比,让每个币按自身波动率个性化止盈止损(打掉 Claude "一套%通杀所有币"的弱点)。仓库 `/root/projects/nofxmax`,分支 `feat/expectancy-maker-trailing-vol`。语言:中文沟通。

### 部署/环境关键事实(务必记住)
- 生产 = docker compose,工作目录 `/root/projects/nofxmax`,`docker-compose.yml`。两个容器:`nofx-trading`(后端,端口8080)、`nofx-frontend`(端口3000)。
- 部署方式:`docker compose build nofx`(或 nofx-frontend)→ `docker compose up -d`。容器跑的是**镜像内置 `/app/nofx`**,不是 `/app/data/nofx-hotfix`(那是死文件)。
- 编译/测试用 docker:`docker run --rm -v /root/projects/nofxmax:/app -w /app -e CGO_ENABLED=1 -e CGO_CFLAGS=-D_LARGEFILE64_SOURCE --entrypoint sh golang:1.25-alpine -c '...'`(需 TA-Lib/CGO)。
- DB:容器内 `/app/data/data.db`(SQLite,~2GB),宿主 `/opt/webstack/nofx/data/data.db`。查询:`docker exec nofx-trading sqlite3 /app/data/data.db "..."`。
- **DB备份(归因重建回滚点)**:`/opt/webstack/nofx/data/data.db.attrib-backup`(2GB,勿删)。
- **磁盘曾满死机**:清理用 `docker builder prune -af`(最大头)+ `npm cache clean --force`。死机/大扫描前先 `df -h`,留 >10GB。
- **分类器(classifier)会间歇拦截写命令/go命令**:失败时重试,或用 `echo probe` 探活后重试;大扫描用 `docker exec -d` detached + 输出到文件轮询。
- **OOM 教训**:回测 Sweep 曾保留每点明细撑爆内存死机,已修(`res.Results=nil`)。大扫描务必内存轻量,且在 host 跑别和交易容器抢内存。

### 当前线上真实状态(2026-06-22 ~11:xx CST 实测)
- 容器:nofx-trading / nofx-frontend 均 healthy,backend=200 frontend=200,磁盘 ~68%。
- 后端镜像 `755966103ffb`,前端 `085d3559c4d4`。最近回滚点镜像 `aacded3f`(churn 修复前)。
- **Claude-R**(交易员 id `02273139_..._claude_1781859724`,strategy id `85b160fb-144d-4ded-a6fe-92b3cdc96596`):
  - `atr_protection` = `{enabled:1, timeframe:1h, atr_period:14, sl:4.5, tp1:3.0, tp2:5.0, be1:2.0, be2:2.5, min_eff_pct:0.3, max_eff_pct:25}`,multiple_mode 未设→回退 fixed。
  - 这组倍数 = 8币种6月回测最优(全内部解,PF1.11)。fixed 模式,**用户要求先保持 fixed 观察,勿切 AI**。
- Claude 本体 strategy id `6fd686fe-df0f-4d30-98cf-5f93e0a89a0c`(勿混淆;ATR 只配在 Claude-R)。

### ⚠️ 未结事项 = reconciler churn 事件(最重要,接着干这个)

**进度更新 2026-06-22 下午:churn 有 3 种形态,已修 2 个防范层,核心 A/B 待做。**

- **形态1(已修)**:同 trader 同 symbol+side 出现多个 OPEN 行(WLD 03:50+08:52)。`missingTP=false unexpectedTP=1` 纯重复单。修复=`protection_reconciler.go` 覆盖完整快速通道(coverage complete 时直接撤 stale 重复单不 re-place)。测试 `TestProtectionReconcilerCancelsStaleDuplicateTPWithoutReplaceWhenCoverageComplete` 绿。
- **方案C(已完成,防范同 trader 重复行)**:`store/position_dedup.go`(MergeDuplicateOpenPositionsForKey + FindDuplicateOpenPositionKeys,带 dry-run)+ `CreateOpenPosition` 净仓 guard(同 trader+symbol+side 已有 OPEN 就合并不新建)+ `cmd/posmerge`(一次性迁移工具,默认 dry-run)。4 测试绿。**注意:store 测试需 CGO=1(sqlite 驱动 go-sqlite3),装 build-base 即可,不需 ta-lib**。生产当前已无同 trader 重复行(dry-run 0 组),工具暂无活,代码作预防保留。
- **多交易员交叉冲突排查结论**:当前 3 活跃实例(Claude-R ...1781859724→账户02273139 / Claude本体 ...1779550392→账户4801de05 / openai ...1781600398→账户24be455b)**分属 3 个独立 OKX 账户,无活跃共享**。隐患:DB 里有账户绑过新旧两个 trader 实例(旧实例已停用、0 OPEN、近1h 日志零出现),若旧实例重启会与新实例共享账户、各自 reconciler 互判 = churn。
- **防范层1(已完成)**:`trader/account_exclusivity.go` 全局 `exchangeID→owner trader id` 注册表。`Run()` claim,仅独占者启 reconciler/drawdown;同账户第二活跃实例被拒(只观察不写)。`Stop()` 释放。空 exchangeID 不阻断。3 测试绿。`auto_trader.go` 加字段 `ownsAccountProtection` + Run/Stop 接入。
- **reconciler/verifyLivePositionForProtection 现状**:都用 `at.trader.GetPositions()` 取交易所净仓,**无 trader 归属过滤**;跨账户隔离靠不同 API key。层1 已从源头堵住同账户多实例,故层2(逐仓归属)暂不需要。

- **⭐ 待做 = 当前正在 churn 的 HYPE(Claude-R 账户02273139 单仓 0.4@67.29)= 形态2:单仓 ladder TP 价位/数量漂移**。`70.02@0.1` 每30秒重下累积(22:06/22:07 各一张),`missingTP=true unexpectedTP=1` 死循环。这是**方案 A/B**:plan 每周期算的 TP tier 与交易所现存单精确匹配失败。需查为何 plan TP 价位/qty 每周期漂移(疑 anchor 重算 / DD min_profit ATR 化 / qty 0.1 与仓位 0.4 比例对不上)。cooldown=60s 但 churn 间隔 38-40s,可能状态切换绕过 cooldown(未完全验证)。
  - 方案A(治本):detectMissing/unexpected 改"精确价位匹配"→"TP 覆盖充足性"判定。
  - 方案B(止血):placeAndVerifyLadder 重下前放宽等价容差+校验累计qty已覆盖就不重下;missing+unexpected 并存且 bot TP 累计 qty≥仓位需要时判覆盖充足、不动单进长 cooldown。

##### ✅✅ 形态2 已根治(2026-06-22 晚,终极根因+修复完成)
- **终极根因(铁证)**:每周期日志 `⚠️ Protection tier dropped as non-executable: HYPE price=71.844853 qty=0.080000 err=quantity 0.8 below min contracts 1.0`。Claude-R HYPE 仓 0.4,ladder TP 算 2 个 tier:70.02(qty0.1=1合约,能下)+ **71.84(qty0.08=0.8合约 < 交易所最小1合约,永久下不出去)**。下单路径 `validateProtectionPlanExecution` 把 71.84 drop,但 **reconciler 的 `detectMissingProtection` 用未 drop 的原始 plan** → 永远 missingTP=true → 无限重下;70.02 反复累积。**不是价位漂移/多仓/多实例,是 min-contract floor 与 detectMissing 不一致。**
- **修复(精准低风险)**:`protection_reconciler.go` anchor 之后、detect 之前,加 `validateProtectionPlanExecution` 过滤,使"期望集合==可下集合"。HYPE:71.84 被滤、70.02 保留→missingTP=false,再由形态1快速通道清累积重复单。两修复闭环。
- 测试 `trader/protection_reconciler_subminqty_test.go:TestProtectionReconciler_SubMinTierDoesNotCauseMissingChurn` 绿。

#### 🧪 测试环境关键教训(务必记住)
- **trader 包测试必须 CGO_ENABLED=1 + apk add build-base**。凡建 store/DB 的测试用 sqlite 驱动(go-sqlite3 需 CGO),CGO=0 会**假性失败**(`go-sqlite3 requires cgo, This is a stub`)。我曾误判 break-even 等 8 个测试"失败",实为 CGO=0 环境问题。正确命令:`docker run --rm -v /root/projects/nofxmax:/app -w /app -e CGO_ENABLED=1 --entrypoint sh golang:1.25-alpine -c 'apk add --no-cache build-base >/dev/null 2>&1; go test ./trader/ ./store/ -count=1'`。**不需要 ta-lib**(go.mod 无 TA-Lib 绑定,sqlite 用 modernc 纯Go——但部分测试显式用 gorm sqlite 驱动需 CGO)。纯 build(非测试)可 CGO=0。
- 修了一个跨测试泄漏:`protection_reconciler_test.go` 的 break-even 测试加了 `reconcileCooldowns["BTCUSDT_long"]` 自清理(全局 map 跨测试污染)。

#### ✅ 本轮全部完成(待部署,未 commit)
1. 形态1 覆盖完整快速通道(`protection_reconciler.go`)+ 测试。
2. 形态2 min-contract 一致性过滤(`protection_reconciler.go`)+ 测试。
3. 方案C 同 trader 净仓去重(`store/position_dedup.go` + `CreateOpenPosition` guard + `cmd/posmerge` 迁移工具 dry-run)+ 4 测试。
4. 防范层1 账户独占(`trader/account_exclusivity.go` + auto_trader.go Run/Stop 接入 + ownsAccountProtection 字段)+ 3 测试。
- **全部测试 CGO=1 通过:`nofx/trader` ok 17.9s,`nofx/store` ok 1.1s,无回归。**
- **部署前**:记录回滚镜像(当前后端 `755966103ffb`),`docker compose build nofx` → `up -d nofx`,然后盯 churn 归零 + 无新 panic。**未 commit(遵用户规矩)。生产当前后端镜像未含这些修复——churn 仍在,等部署。**

- **churn 代价**:始终不烧钱(只刷 API/日志,撤单多成交0,SL 全程在位)。

#### 旧记录(形态1 早期分析,保留)
- **现象**:08:50 部署「每维度+DD」后,reconciler 高频报 `unexpected cleanup incomplete after replacement`。
- **安全**:`missingSL=false` 全程,止损一直在,**无资金风险**。但持续 cancel/replace 在刷交易所 API + 刷日志,运维面坏。
- **根因(已确认)**:不是 ATR 价格漂移(frozen-ATR 修了只部分见效)。真因 = **ladder_tp 与 drawdown 对 TP 单归属互判** + stage-before-cleanup 流程在 ladder 共存时自造重复单。
- **13:14-13:16 实测的精确循环机制**(关键证据):
  1. 稳定态:`verified=true profitOwner=ladder_tp unexpectedTP=0`(2个ladder TP单匹配plan,干净)。
  2. 下一周期:`nativeTrailingArmed` 一 arm → `profitOwner` 跳 drawdown,staleBot 0→1或2,`unexpectedTP=1`(把已有 ladder TP 当 unexpected)→ 触发 stage replacement。
  3. stage 时 `placeAndVerifyProtectionPlanWithRetry` 重下整套 ladder(又造重复 TP),再 `cancelUnexpectedProtectionOrdersByID` 只取 staging **前**收集的旧 ID,新下的重复单没被清 → 第302行复查 `remainingUnexpectedTPs=1` → 报 `cleanup incomplete`。
  4. 下周期 staleBot 归 0 又 verified=true,如此 0→2→0→1 反复。
- **已修复部分**:`trader/atr_protection_resolver.go:frozenATRForPosition`(按 symbol+entryPrice 冻结 ATR)→ churn 从 6+ 币种降到 2 个仓。
- **⚠️ 实测纠正(2026-06-22 13:15)**:
  - churn **未自愈**,稳定 ~220次/小时(10时98/11时222/12时225),近30min WLD 90次/ZEC 31次。
  - **不止旧仓**:WLDUSDT 有**两个仓**——03:50(变更前)+ **08:52(部署后)**,后者也 churn。所以"新仓干净/等旧仓平掉自愈"的假设**错了**。ZEC 也有两个(06-20 21:56 + 06-22 06:10)。
  - 当前 OPEN 16 仓(ETH×3/ZEC×2/WLD×2/SPCX×2/XAU×2/HYPE/BTC/SOL/TRUMP/XAG),只有 WLD/ZEC churn。
  - 持仓表字段:status 大写 `OPEN`/`CLOSED`;时间列是 `entry_time`(非 open_time),无 `unrealized_pnl` 列。
- **下一步选择**(churn 未自愈,被动等已被证伪,倾向主动修,**动手前等用户确认**):
  1. 主动修 A(治本,推荐):修 stage-before-cleanup,在 ladder 共存场景下重下前先判断该 TP 价位是否已在 plan allowed 内(已有单就别重下),或把新下单 ID 也纳入 cleanup 复查豁免。
  2. 主动修 B(更小):类比 `protection_reconciler.go:274` 对 unexpectedStops 的保留逻辑,在 `profitOwner != ""`(ladder_tp/drawdown 其一已满足)且 unexpected 全是 staleBot 自家单时,对 unexpectedTP 也加保留,不触发 replace。
  3. 被动等(已证伪,不推荐)。

### 用户交代的巡检任务(进行中)
- 用户要求"今天每小时检查监督系统运行情况"。每个整点巡检:① 容器健康/磁盘 ② 错误日志(panic/fatal/reconcile failed)③ churn 是否扩散到新仓 ④ WLD/ZEC 旧仓是否平掉。巡检命令模板见本节"部署/环境"。

### 路线A 四步状态(均已上线)
1. ✅ ATR 框架 config+runtime(resolve-at-placement)
2. ✅ 移除旧 R 值 sidecar(前后端)
3. ✅ 每维度三选一 UI(%/ATR手填/AI按币种)+ DD ATR化
4. ✅ Claude-R 配回测最优倍数 fixed 上线
+ AI 倍数提示词系统已建(multiple_mode=ai 时按币种 AI 给倍数,缓存4h),Claude-R 暂不用。

### 待办/可选(非紧急)
- churn 主动修(见上,需用户确认)。
- 几周后用更大真实样本重做参数优化(回测工具链 `cmd/backtest`/`cmd/btrobust`/`cmd/btvalidate` 已就绪)。
- 32 个未提交改动文件(本轮所有工作),用户未要求 commit,**勿擅自 commit**。

---

## 🔎 巡检事件 (2026-06-22) — reconciler churn

- **现象**:per-dim+DD 部署(08:50 CST)后,protection reconciler 报 `unexpected cleanup incomplete after replacement` 高频(09时 259次)。持仓状态 protected↔degraded 反复跳。
- **安全面**:`missingSL=false` 全程 —— 止损一直在,无资金风险。
- **根因(确认)**:不是 ATR 价格漂移(第一个 frozen-ATR 假设错了,修了没降)。真因是 **ladder_tp 与 drawdown 对 TP 单的归属互斥** + 配置在持仓存续期内被改(compromise→backtest-best 倍数),导致旧仓挂单与新 plan 反复"互判 unexpected → 替换 → 再判"。`profitOwner` 在 ladder_tp/drawdown 间跳即证据。
- **已做的修复**:`trader/atr_protection_resolver.go` 加 `frozenATRForPosition`(按 symbol+entryPrice 冻结 ATR,reconciler 复用,避免逐周期重算)。**部分见效**:churn 从 6+ 币种降到仅 2 个旧仓(WLD 03:50、ZEC 06:10 开,均在变更前)。**部署后开的新仓(TRUMP 10:11/SEC 10:33)不 churn**。
- **残留**:2 个变更前的旧仓持续 churn,会在它们平仓后自愈;新仓干净。回滚镜像 aacded3f。
- **待定**:若旧仓 churn 持续太久,考虑(a)对 ladder_tp+drawdown 共存时的 unexpected 判定加豁免,或(b)等其自然平仓。不要再盲目改,先观察新仓是否稳定。

## 🚧 ATR 保护框架(路线A,建设中)

### ⚠️ ATR vs 现有保护/盈利控制:不是两套并行,是"改写距离"(v1.7.0 一致性修复)
- ATR 不另起系统:开仓时把现有 ProtectionConfig 的 TP/SL/BE 距离改写成 ATR 倍数换算值,交同一套保护/盈利控制执行。"保护/盈利控制开着"正常——ATR 只改它的距离参数。
- 曾有一致性 bug(已修):开仓用 ATR 距离,但每周期复查用原始百分比比对,会把 ATR 单当"不对"改回百分比。修复:
  1. `protection_reconciler.go:173` → `BuildConfiguredProtectionPlanForSymbol`(ATR版);AI fallback ladder 用 `resolveATRProtection` 后配置。
  2. `auto_trader_risk.go` BE 监控 → 新增 `getActiveBreakEvenRulesATR(symbol,entryPrice)`,BE 在与开仓一致的 ATR 触发距离 arm。
  3. DD(MinProfit/MaxDrawdown)未 ATR 化,全程百分比,自洽。
  4. 只读展示路径不下单,不影响交易。
- protection 测试全绿无回归,部署 health200,回滚镜像 b63ac111。

- 回测证实:ATR 驱动 > Claude 固定%(方向性,两样本都成立)。
- **但具体倍数过拟合**:真实入场(151笔/3.5周)最优 SL2.5/TP2 3.0,机械(328笔/6月)最优 SL4.0/TP2 6.0,交叉验证 real_opt 在机械样本 top87.6%(接近最差)。折中方案两边都平庸。
- **唯一跨样本稳定**:BE2≈2.5×,TP1∈[2,3]。
- **结论=路线A**:先把 ATR 框架上线 + Claude-R 用保守 `compromise_wideSL`(SL3.5/TP1 2.5/TP2 5.0/BE1 1.5/BE2 2.5)小仓实跑,攒更大真实样本后再优化。不要现在拍死参数。

### 设计:resolve-at-placement(沿用 BE r_multiple 既有模式)
- `trader/auto_trader_risk.go:resolveBreakEvenRulesForPosition` 是先例:把 r_multiple 在开仓时按 SL 距离换算成 profit_pct。ATR 同理:`effPct = M×ATR(1h)/entry×100`。
- 关键转换点(都要注入 ATR):FullTPSL.Value、LadderTPSL rules pct、BreakEven TriggerValue/OffsetPct、Drawdown MinProfit/MaxDrawdown、`auto_trader_loop.go:valueSourceToAbsolutePrice`。

### 已完成(config 层,已测)
- `store/atr_protection.go`:`ATRProtectionConfig{Enabled, Timeframe(默认1h), ATRPeriod(14), StopLossATR/TakeProfit1ATR/TakeProfit2ATR/BreakEven1ATR/BreakEven2ATR, MinEffPct(0.3)/MaxEffPct(25)}` + `EffectivePercent()` 换算(带 clamp)。
- `store/strategy.go`:StrategyConfig 加 `ATRProtection`(omitempty,**默认 off=完全 no-op**,不影响任何现有交易员)。
- 测试 `store/atr_protection_test.go`:换算/上下界/禁用维度/向后兼容 JSON,4 用例 PASS。

### 待办(按序)
1. ✅ runtime:ATR resolver 已建并接入。`trader/atr_protection_resolver.go`(`atrForProtection` 取1h Wilder ATR + `resolveATRProtection` 返回 ATR 覆写后的 ProtectionConfig 副本,仅 ATRProtection.Enabled 生效)。`protection_plan.go` 重构出 `buildConfiguredProtectionPlanWith(protection)`,新增 `BuildConfiguredProtectionPlanForSymbol`(开仓时走 ATR)。`protection_execution.go:applyPostOpenProtection` 改用之。覆盖 TP1/TP2/SL/BE1/BE2;DD 暂留百分比。
2. ✅ 移除旧 R 值 sidecar:删除 trader/rvalue_*.go、kernel/{trend_detector,trend_indicators,protection_trailing,protection_calculator}.go + 测试、api/{handler,routes}_protection_unified.go;解除 auto_trader_orders.go(2处sizing)、auto_trader_risk.go(trailing+trend)、server.go(路由)接入。store/protection_unified.go + migration 保留(表已存在,无害,且 sidecar gate 已随代码删除而失效)。root 二进制编译通过。
3. ✅ UI:`web/src/components/strategy/ATRProtectionEditor.tsx`(ATR 自适应保护面板,enable + timeframe + atr_period + sl/tp1/tp2/be1/be2 倍数 + min/max%),嵌入 StrategyStudioPage 的 protection tab(ProtectionEditor 之后,用 `<>` 包裹;未新增 tab,避免 tab-key union/expanded-state 改动)。`web/src/types/strategy.ts` 加 `ATRProtectionConfig` + StrategyConfig.atr_protection?。前端 tsc+vite 构建通过,nofx-frontend 部署 healthy 200。
   - 顺手清理孤儿 UI(已删 sidecar 的前端残留,均无路由引用):UnifiedProtectionPanel/ProtectionMonitor.tsx、ProtectionSystemDemo/FullDemo.tsx、useProtectionConfig.ts、types/protection.ts。
4. ✅ 部署 + Claude-R 配 compromise_wideSL 已上线:strategy `85b160fb` 写入 `atr_protection{enabled,1h,14,sl3.5,tp1 2.5,tp2 5.0,be1 1.5,be2 2.5,min0.3,max25}`。镜像已部署 health200 无 panic。回滚镜像 22bf76ba。开仓时日志现 `🎯 ATR-protection applied`。
   - 注意:Claude-R strategy_id=85b160fb(不是 Claude 本体 6fd686fe);ladder/BE 规则已存在,resolver 按 tier index 覆写。

### 回测工具(已建,保留)
- `cmd/backtest`(真实入场)、`cmd/btrobust`(机械长周期)、`cmd/btvalidate`(双样本交叉验证)、`trader/backtest/*`、`market/historical_okx.go`(OKX history-candles)。
- ⚠️ **OOM 教训**:`Sweep` 曾对每个网格点保留 `PortfolioResult.Results`(每点×数百笔 TradeResult),宽网格扫描 OOM 被 SIGKILL(137)导致服务器死机。已修:`sweep.go` 在 `RunParams` 后 `res.Results=nil` 只留聚合指标。以后大扫描务必保持。
- ⚠️ 磁盘:`docker builder prune -af` 是最大头(曾占 12GB),npm cache 3GB。死机前先 `df -h`,留足 >10GB。

### 宽网格回测最优(8币种/6月/540笔机械入场,2026-06-22)
- 网格已扩到内部:SL[2-6] TP1[1-3.5] TP2[3-10] BE1[0.5-2.5] BE2[2-4]。
- 基线(Claude固定%回放):PnL 128 / Win 55% / PF 1.01 / MaxDD 965。
- **采用配置(#3,全内部、PF最高1.11)**:**SL4.5 / TP1 3.0 / TP2 5.0 / BE1 2.0 / BE2 2.5** → PnL 855 / Win 59% / PF 1.11 / MaxDD 670。
- 收敛稳定:TP2=5.0、BE1=2.0 在 top15 全收敛;SL 4-6 区间不敏感(故不追边界6.0);TP1=3.0 主导。
- **已写入 Claude-R strategy 85b160fb**(sl4.5/tp1 3.0/tp2 5.0/be1 2.0/be2 2.5,enabled=1),开仓时读取生效,无需重启。

### 待办(新需求,进行中)
- ✅ AI 倍数提示词系统(已上线):`ATRProtectionConfig.MultipleMode "fixed"|"ai"`。`trader/atr_ai_multiples.go`:开仓时按币种 prompt(注入 ATR 绝对值/百分比/价格)→ AI 返回 5 维 ATR 倍数 JSON → clamp(AIMinMult0.5/AIMaxMult10)+ 排序校正 → 按 (trader,symbol) 缓存 4h。`atr_protection_resolver.go:effectiveATRMultiples` 统一供 resolver 与 BE 监控用,AI 失败回退固定倍数。测试 PASS。
- ✅ **每维度三选一(已上线 v1.8.0)**:`ATRProtectionConfig` 加 `SLMode/TP1Mode/TP2Mode/BE1Mode/BE2Mode/DDMode`(`percent`|`fixed`|`ai`),`DimMode(dim)` 解析(per-dim → 回退 panel MultipleMode → "fixed")。resolver 与 BE 监控按维度 gate;`effectiveATRMultiples` 仅对 AI 档维度调 AI 覆写,fixed/percent 维度保留各自值。测试 `store/atr_dimmode_test.go` PASS。
- ✅ **DD 维度 ATR 化(已上线 v1.8.0)**:`DrawdownMinProfitATR` 把 DD 的「触发利润(min_profit)距离」ATR 化(作用于最高 min-profit tier=runner_exit);最大回撤 give-back 仍是峰值%(比率,不适用 ATR)。
- ✅ UI:`ATRProtectionEditor.tsx` 重写为**每维度一行**(SL/TP1/TP2/BE1/BE2/DD)× [计量方式下拉 %/ATR手填/AI按币种 + ATR倍数输入]。前端构建通过,nofx-frontend 部署 healthy200。
- ✅ Claude-R 向后兼容:per-dim 模式全 unset → 回退 fixed → SL/TP/BE 仍按回测最优倍数 ATR 化,DD 仍走 %(其倍数未设)。无行为变化。strategy 85b160fb,multiple_mode 仍 fixed。
- 后端镜像回滚点 40456135;前端已部署。

### 待办(已完成项)

---

## ✅ 平仓归因系统修复与历史重建 (v1.5.0)

### 背景认知(重要,避免重蹈覆辙)
- `trader_positions.close_reason` 旧值里 `close_long/close_short` 是**通用动作标签,不代表 AI 决策**。不要据此推断平仓原因。
- canonical 归因在 `position_close_events` 的 `category`/`mechanism`,以及订单 `client_order_id`/`order_action` 标签。
- 历史通用市价平仓单(~1950)源头**未持久化平仓原因**,无法凭空恢复 AI/回撤/时间止损细分。

### 两个前向 bug 已修(store/position.go)
1. `MarkOpenPositionsAbsentFromExchangeClosed`:持仓在交易所消失时已查到成交平仓单却硬写 `sync_absent_from_exchange`。修复:用 `dominantCloseOrderID` + `deriveCloseReason` 还原真实保护原因。
2. `ApplyLateCloseFillToClosedPosition`:延迟成交只更新 PnL 不更新 `close_reason`。修复:行上是通用/sync 原因时升级为保护单真实原因。
- 测试:`store/attribution_sync_fix_test.go`(2 用例 PASS)。

### 历史重建(一次性,已对生产执行)
- 工具:`cmd/attribrebuild/`(纯 Go,modernc sqlite,默认 dry-run,`-apply` 才写)。
- 重建分类法(确定性):①平仓单保护标签 → full_tp/full_sl/break_even/native_trailing/ladder_*；②exit_decision_cycle>0 关联决策记录 → `ai_close`；③都没有 → `market_close`(诚实标注未知)。
- 生产结果(537 笔已平,530 改):ai_close 371 / market_close 136 / full_tp 15 / native_trailing 6 / break_even 6 / full_sl 3。
- Claude 本体:ai_close 136 / market_close 32 / 机械保护 26（full_tp12/be6/trailing5/sl3）= 194。
- 备份(回滚点):`/opt/webstack/nofx/data/data.db.attrib-backup`(2GB,VACUUM INTO,integrity_check=ok)。

### 回测引擎(已建,cmd/backtest + trader/backtest)
- OKX 历史数据:`market/historical_okx.go` `GetKlinesRangeOKX`(history-candles 分页)。
- 引擎忠实复刻 Claude 保护语义(ladder TP+3/+6、SL-5、两级BE+2/+4、DD盈利6%&回撤40%),支持 percent / atr_multiple 双口径。
- 命题(反事实):在 Claude 真实入场点上,ATR 机械保护 vs 实际平仓,谁损益好。
- 待办:用重建后的干净归因重跑校验(对齐那 26 笔机械平仓)+ 全量 + 长周期多币种。

### Claude 当前真实保护参数(基线,数据库实读)
- TP ladder: +3%(平35%)/+6%(平25%); SL -5%(平100%)
- DD: 盈利≥6% & 峰值回撤≥40% → 平45%(runner_exit);兜底 盈利≥0.7%
- BE 两级: +2%→入场+0.4%(平50%); +4%→入场+1.5%(平35%)
- 主周期 1h；历史数据从 OKX 取

---

## 旧版本记录

---

## ✅ AI 主备端点 per-call 故障转移修复 (v1.4.0)

### 问题（修复前）
`mcp/client.go` 的 `resetToPrimaryEndpoint` 只把 `currentEndpointIndex` 置 -1，**不恢复 `BaseURL/APIKey/Model`**，且无主端点快照字段。一旦切到可用备用端点，client 永久停在备用，直到备用也挂或进程重启。日志实证：18:39 主端点 524 → 切 NovaI → 之后不回主。

### 修复
- `Client` 新增主端点快照字段：`primaryBaseURL/primaryAPIKey/primaryModel/primarySnapshotted`。
- `SetFallbackEndpoints` 时快照主端点（此时 client 仍持主端点值，因 SetAPIKey 在前）。
- 新增 `restoreToPrimary()`：完整恢复主端点连接参数 + index=-1（幂等）。
- 三个调用入口 `CallWithMessages` / `CallWithRequest` / `CallWithRequestFull`：**每次调用开头 `restoreToPrimary()`**，主成功直接返回；主失败才逐个试备用一次；备用成功后立即 `restoreToPrimary()`，使下次仍从主起步。备用用各自 Model（fbReq 副本，不污染主请求）。
- 删除旧 `resetToPrimaryEndpoint`。
- 行为：**优先主端点 → 失败才用一次备用 → 下次仍从主端点起步**。
- 测试 `mcp/failover_test.go`：快照/切换恢复/逐次回主/无备用no-op，全 PASS。顺修 `config_usage_test.go` 旧断言双空格→单空格。

---

## R 值系统三大功能（v1.1-1.3，均已实盘）
- v1.1 R值定仓 `trader/rvalue_sizing.go`
- v1.2 R值追踪止盈 `trader/rvalue_trailing.go`
- v1.3 趋势分级响应 `trader/rvalue_trend.go` + `kernel/trend_indicators.go`

---

## ⚠️ 关键认知：部署机制（曾踩坑）

- 生产容器 `nofx-trading` 运行的是**镜像内置的 `/app/nofx`**（由 `docker/Dockerfile.backend` 多阶段构建 `COPY --from=backend-builder /app/nofx .` 生成）。
- `/app/data/nofx-hotfix` 是历史遗留文件，**从不被执行**。直接拷贝它无任何效果。
- 正确部署：`docker compose build nofx` → `docker compose up -d nofx`（compose 工作目录 = `/root/projects/nofxmax`，由容器 label 确认）。
- 回滚：记录部署前 `docker inspect nofx-trading --format '{{.Image}}'` 的镜像 ID，必要时 `docker tag` 回旧镜像并 `up -d`。
- DB 迁移为**纯增量**（仅 `CREATE TABLE/INDEX IF NOT EXISTS`），旧镜像回滚后多余表无害。

---

## ✅ R值仓位计算实盘接入 (v1.1.0 核心)

R值系统的"初心"= **按固定风险定仓**：止损被触发时亏损 == 1R == equity × RiskPerTrade%。

### 接入文件
- `trader/rvalue_sizing.go` — 核心。
  - `resolveRValueConfig()`：**安全闸门**。仅当交易员在 `trader_protection_configs` 有**显式行**且 `enabled && unit=="R" && risk_per_trade>0` 时返回配置。未配置交易员永远走原百分比逻辑（绝不用默认 R 配置覆盖）。
  - `applyRValueSizing(decision, equity, entryPrice, side)` → `computeRValueSizing(...)`（纯函数，可测）。
  - 公式：`riskAmount = equity × RiskPerTrade/100`；`quantity = riskAmount / |entry-stop|`；`newSizeUSD = quantity × entry`。
  - 守卫：无结构止损 / 止损方向错误 / 止损距离<0.1%（过近会异常放大）→ 跳过，保持原仓位。
- `trader/rvalue_sizing_test.go` — 核心不变式 + 8 个守卫用例，`go test ./trader/` PASS。
- `trader/auto_trader_orders.go` — 在 `executeOpenLongWithRecord` / `executeOpenShortWithRecord` 中，**equity 计算之后、`applyVolatilitySizing` 之前**调用 R 重算。
  - 顺序关键：R 重算在所有原有风控 cap（volatility / position-value-ratio / margin / min-size）**之前**，因此上限始终兜底。
- `store/migration_unified_protection.go` — 新增 `GetExplicitUnifiedProtectionConfig`（返回 exists 标志）、`GetTrailingTPState`。

### 重要：决策语义
- `kernel.Decision.StopLoss` / `.TakeProfit` 是**绝对价格**（非百分比）。LONG 止损<入场，SHORT 止损>入场。
- R 重算用 AI 给出的**结构止损价**定仓，不替换 AI 的止损位。
- 配置每次开仓**实时读库**（无缓存），改配置无需重启。

### Claude-R 已激活
- trader_id: `02273139_f40c31f0-d7c4-4ea6-ad76-a5f2b56dc065_claude_1781859724`
- 配置: `enabled=true, unit=R, risk_per_trade=1.0`（1R=1%账户），SL/TP decision_mode=ai。
- 写入方式: `trader_protection_configs` 表 upsert（readfile 注入，等价 `SaveUnifiedProtectionConfig`）。
- 下次开仓时日志出现 `🎯 R-value sizing (...)`。

### 尚未接入实盘的部分（API/存储已就绪但交易回路未消费）
- （已无）三大核心功能均已接入实盘，见 v1.3.0。

## ✅ 趋势转换分级响应实盘接入 (v1.3.0 — 全功能完成)

### 新增指标 (kernel/trend_indicators.go)
- `SMA`、`WilderADX`（Wilder 平滑 ADX/+DI/-DI）、`ComputeTrendIndicators`。原 pipeline 无 ADX/MA200，新建。需 ≥200 根 K 线。
- 测试 `kernel/trend_indicators_test.go` 全 PASS。

### 接入文件
- `market/data_klines.go`：导出 `GetKlines(symbol, interval, exchange, limit)`。
- `trader/rvalue_trend.go`：`maybeTrendResponse` 拉 250 根主时间框 K 线 → 指标 → `kernel.TrendDetector` → 写 `trend_transition_logs` → 按 MaxActionLevel 分级。`trendStateSingleton` 做确认计数与去重。`applyTrendAction`：L1 仅记录；L2 减仓 50%；L3/L4 清仓。**反手永不自动开仓**，交给 AI。
- `trader/auto_trader_risk.go`：`checkPositionDrawdown` 中 R-trailing 之后调用。
- `store/protection_unified.go`：新增 `TrendResponseConfig{Enabled, MaxActionLevel(0-4), ConfirmationCycles}`。

### MaxActionLevel 安全分级
0=仅记录(默认零值，未配置者安全no-op) / 1=L1建议 / 2=减仓50% / 3=清仓 / 4=清仓+反手(反手仍交AI)

### 修复
- `kernel/protection_calculator.go` `calculateRMode` 双重 /100 bug 已修（API calculate-position 用，与实盘 `computeRValueSizing` 无关）。

### Claude-R 全功能已激活
`unit=R risk=1.0 | trailing_tp=1(act1 pull0.5 max5 dynamic) | trend_response=1 max_level=0(仅记录) confirm=2`
观察 `🧭 Trend transition` 日志后，可调高 max_action_level 开启减仓/清仓。

## ✅ R值追踪止盈实盘接入 (v1.2.0)

按 R 值口径运行追踪止盈，状态持久化跨 cycle/重启。

### 接入文件
- `trader/rvalue_trailing.go`：
  - `resolveStructuralStopForPosition(symbol, side)`：从持仓的开仓决策记录（EntryDecisionCycle → GetRecordByCycle → decision.StopLoss）还原结构止损价，作为 1R 分母。
  - `maybeRValueTrailingClose(...)`：仅当显式 R 配置且 `trailing_tp.enabled` 时生效。载入 `trailing_tp_states` 持久状态 → 重建 `kernel.TrailingTakeProfitEngine` → Update → 持久化 → 必要时 `closePositionByReason(...,"trailing_take_profit")`。
- `trader/auto_trader_risk.go`：`checkPositionDrawdown` 中，紧接百分比 trailing 块之后调用 `maybeRValueTrailingClose`。共用同一平仓出口，谁先触发谁平仓，无双重平仓。
- `kernel/protection_trailing_engine_test.go`：激活/回撤/最大目标/禁用 用例，PASS。

### 安全性
- 与百分比 trailing (`RiskControl.TrailingTakeProfitEnabled`) 是独立 gate；Claude-R 的百分比 trailing=off，无竞争。
- 还原不到结构止损 → 跳过，不影响 native trailing / drawdown / ladder。
- 平仓后重置 `trailing_tp_states`。

### Claude-R 已激活 R-trailing
- `trailing_tp.enabled=true, activation_r=1.0, pullback_r=0.5, max_target_r=5.0, mode=dynamic`
- 配置实时读库，drawdown monitor 下一 tick 生效。

---

### 旧版尚未接入部分（保留参考）

---

## 核心文件位置

### 后端 (8个文件)
- `kernel/protection_calculator.go` - R值计算器
- `kernel/protection_trailing.go` - 追踪止盈引擎
- `kernel/trend_detector.go` - 趋势检测器
- `store/protection_unified.go` - 统一配置
- `store/migration_unified_protection.go` - 数据库迁移 + 显式读取/追踪状态
- `api/handler_protection_unified.go` - API处理器
- `api/routes_protection_unified.go` - 路由定义
- `api/server.go` - 已添加路由集成 (server.go:393 AddUnifiedProtectionRoutes)

### 前端 (6个文件)
- `web/src/components/strategy/UnifiedProtectionPanel.tsx`
- `web/src/components/strategy/ProtectionMonitor.tsx`
- `web/src/types/protection.ts`
- `web/src/hooks/useProtectionConfig.ts`
- `web/src/pages/ProtectionSystemDemo.tsx`
- `web/src/pages/ProtectionSystemFullDemo.tsx`

---

## 关键概念

### 1. R值系统
- 1R = 账户的X% (如1% = 10 USDT on 1000 USDT)
- 每笔交易固定风险金额
- `RiskCalculator.Calculate()` 计算仓位

### 2. 追踪止盈
- 达到1R激活 → 追踪 → 回撤0.5R平仓
- 最大目标5R
- `TrailingTPEngine.Update()` 实时更新

### 3. 4级分级响应
- Level 1: 收紧止盈，100%仓位
- Level 2: 减仓50%
- Level 3: 清仓
- Level 4: 按新趋势操作

---

## API端点

```
GET  /api/protection/unified
PUT  /api/protection/unified
POST /api/protection/calculate-position
GET  /api/protection/presets
POST /api/protection/simulate-trailing
```

---

## 数据库

4个新表:
- `trader_protection_configs`
- `trader_protection_config_history`
- `trailing_tp_states`
- `trend_transition_logs`

---

## 完整文档

详见: `/root/FINAL_DEPLOYMENT_CHECKLIST.md`

