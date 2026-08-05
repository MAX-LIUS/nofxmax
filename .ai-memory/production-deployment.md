# Production Deployment - Critical Information

## 2026-08-05 Deploy: 账户实际价值熔断上线 + claude 实盘 2.8% (pid 998467, commit 1cfc0b5)

用户诉求「实盘开启账户2.8%熔断」→「直接部署上线,百分比就行,暂时不加冻结,先看看效果再说」。
**只动后端**(前端 6 个字段随 commit 进去但未重建镜像,策略页要看到新面板需下次前端构建)。

**隔离性**:线上是 `4cc1a8f`,dev 上夹着 15 个 `trader/backtest/` 文件。建
`deploy/equity-breaker` 从 `4cc1a8f` cherry-pick `2764b4b` → `1cfc0b5`,落地 11 文件、
**零 `trader/backtest/`**。二进制 `-ldflags="-s -w"` = 48380720 bytes,
md5 `f83b3de12d53a3fc0e9654a5a993d156`,`vcs.revision=1cfc0b5`。
回滚点 `/root/.claude/jobs/eqdeploy/tmp/binbak/nofx_prev_4cc1a8f_20260805_1226`
(md5 `191404d21a36815ead7c336a58996068`);config 回滚
`/root/.claude/jobs/eqdeploy/tmp/config_rollback.json` (10828 bytes)。
`MainPID=998467`,`NRestarts=0`,`pgrep -xc nofx`=1,health 200 @0.73ms。

**写生产 config 的正确姿势(踩过坑)**:`cmd/strategypatch` 的结构体 round-trip 会
**静默丢掉 7 个 `evolution.*` 键**(字段已在 `f92641b` 删除,`store.StrategyConfig`
不再有它们,marshal 就没了)。必须用**裸 JSON deep merge**(Python)写,
`BEGIN` + `assert rowcount==1` + `commit`。写后核对:恰好 +6 键、0 删、0 改,
`evol=1` 仍在。策略 `0d1c8446-26c7-40d3-9cc9-ba531b3facb6` (`claude-hhhl`) 最终值:
`breadth_equity_enabled=1, dd_pct=2.8, dd_abs=0, scope=retracing, cut_pct=100, min_pos=0`,
并存 `breadth_frac=0.55, atr_mult=1.25, cut_winners=true`,`dry_run` 未设(即实盘)。

**部署后核对(12:37:41 UTC / 20:37:41 +8 起)**:panic 0;`ERRO` 全部是既存
`drawdown_claim_conflict.go:300`(ZEC 14 / ETH 7,same-tier fork,基线同源);`WARN 0`;
`equity unavailable` fail-safe **0 次**。138 条事件 **equity_fetch_ok 全 =1**,
`equity_thr_pct=2.8` / `equity_scope=retracing` 落在每一行(含 quorum `near_miss`,
这是设计意图:配额没到也能看到权益距离)。峰值 ratchet 到 156.16,
窗口内 max `equity_dd_pct`=1.177% < 2.8%,未误触发。

**⚠️ 2.8% 的口径与用户预期不一致(已当面提示,用户选择先跑)**:2.8% 来自我那张
**24h 延迟再入场**的行(-0.11%)。claude 真实的 `entry_cooldown` 是 **45min 且按 symbol**,
任何亏损平仓都会 arm,所以下一个 20min 轮次可以开**别的币** → 真实再入场窗口 20~60min,
该档位回测是 **-55%~-60%**、maxDD 64~69%。另:`breadth_cut_winners=true` 意味着权益触发
也会砍掉回撤组里的赢家,而 `break_even_stop` 是唯一大额正贡献出口(126 笔 +54.13)。

**入口不是 `cmd/server`**:主包就是仓库根 `nofx`(`go build -o … .`)。

## 2026-08-05 Hotfix: 广度熔断状态非触发路径也落盘 (commit c2f0f4d / dev 89a281c)

上线后核对时发现的**既存**(非本次引入)保护缺口。`breadth_velocity_states` 三行
`equity_peak` 全 =0、`last_bar` 分别停在 07-31 / 07-14 / 07-01,启动日志
`age=536.2h, stale=true` —— `SaveBreadthVelocityState` **只有一个调用点且在函数尾部**,
而 `no_quorum` / `near_miss` / `cooldown` 三个常见结局都在尾部之前 `return`,
`persistNeeded` 被整个丢掉,**只有真正触发的那次循环才落盘**。
`git show 4cc1a8f:trader/giveback_guard.go` 确认旧版结构完全相同 → 老 bug。

对账户价值熔断这是实质缺口:权益高水位的 ratchet **本质上只发生在安静循环里**
(没触发才会一直抬高),不落盘 = 重启后 `gbEquityPeak` 归零、以当前(可能已回撤的)
权益重新起算,**熔断线被悄悄下移**。修法:函数开头装 `defer` 写入器,覆盖全部 return
路径;放 `defer` 里还顺带覆盖了**触发后的 peak re-base**(它在最后一次 `persistNeeded`
赋值之后)。回归测试 `TestGivebackGuardEquityPeakPersistsOnNonFirePath`
(安静循环 + 权益新高 → 断言读回 250),去掉修复后报 `persisted peak = 0.00`。
新二进制 md5 `c970fce8bf678be50be64932b0ea0c77`,`vcs.revision=c2f0f4d`,48380720 bytes。

## 2026-08-04 Deploy: 剥离进化画像引擎 (pid 896148, commit 4cc1a8f)

前后端都动了。回滚点 `/root/.claude/jobs/5cbb3cf4/tmp/binbak/nofx_prev_9186326_20260804_1035`
(md5 `618e1bcd30f47a2a7b0bc6358553105e` = 线上 commit `9186326`),新二进制
md5 `191404d21a36815ead7c336a58996068`,`vcs.revision=4cc1a8f`。
前端回滚镜像 `nofxmax-nofx-frontend:rollback-pre-evolrm-20260804`。
`MainPID=896148` == `pgrep -xc nofx`(=1),`NRestarts=0`,health 200 @0.65ms。

**用户诉求**:「历史画像很坑人请完全切断历史画像对系统的作用并去掉这部分代码,
我不需要他有越来越明确的技术倾向影响系统决定」→ 确认即「进化引擎」/`coin_evolution_profiles`。

**隔离性**:线上 `9186326`(分支 `deploy/protection-state-dimensions`),dev 上夹着 4 个
research commit(纯 .md + .py)。仍按规矩建 `deploy/evolution-removal` 从 `9186326`
起 cherry-pick `f92641b`,落地 24 文件、**零 `trader/backtest/`**
(`git diff --name-only 9186326..HEAD | grep -c trader/backtest/` = 0)。

**删的是两条真实作用路径,不只是提示词**:
1. 提示词注入 `Historical Performance Profile: 适配度 76/100 | EMA20一致胜率70%…`
   (`kernel/engine_prompt.go` 两处);
2. **更危险的那条**:`applyEvolutionAdaptations` 在下单前直接改写 `d.PositionSizeUSD`,
   含 `allow_confidence_65` → **×1.15 给历史赢家加注**,绕过闸门分数与风险预算。
删除 6 个文件 1980 行 + 摘掉 store/kernel/trader/api/前端全部引用。
**`coin_evolution_profiles` 表与数据原样保留**(不删生产数据);库里 5 个策略 config
仍带 `evolution` 键,Go `json.Unmarshal` 默认忽略未知字段(仓库零 `DisallowUnknownFields`)
→ 自动失效,**不需要写生产 DB**。回归测试 `TestLegacyEvolutionKeyIgnored` 钉住这条契约。

**⚠️ 二进制必须 stripped**:线上那个是 `stripped`,我第一次 `go build` 出来
`with debug_info, not stripped` 68.5MB。改用 `-ldflags="-s -w"` 得 48.4MB 与线上同形。
磁盘 92%,20MB 不是小事;`go version -m` 的 vcs 信息不受 strip 影响,panic 栈也不受影响
(函数名来自 pclntab 不是符号表)。

**门禁**:`go build`/`go vet` 干净;全量 `go test`(排除 `trader/backtest`)**EXIT=0**;
`tsc --noEmit` 0;`madge --circular` 162 文件无循环;`vite build` 35.98s。

**部署后核对(10:34:47 起)**:4 traders 全载;`ERRO 12` 全部是既存
`drawdown_claim_conflict.go:300`(SPCX same-tier fork,基线同源);`WARN 24` 全部
`drawdown_order_reclaim.go:227`;panic 0。`verified=true` 84 / `verified=false` 24,
后者只有 SPCX+ETH 各 12,且同一秒有 `(post-reclaim) … all extra stops were reclaimed`
→ 与基线(54 degraded,同样 SPCX+ETH)同源,**不是本次引入**。
**关键验证**:4 个 trader 部署后第一轮决策 prompt 全部 `✓clean`
(查 `input_prompt`/`system_prompt` 是否含 `Historical Performance`/`适配度`/`EMA20一致胜率`,
10:41-10:46 四条全干净);`/api/evolution/profiles` → **404**;
`web/dist` 里 `EvolutionProfile`/`evolution/profiles`/`进化引擎` 命中 **0**。

**前端 nginx 坑第 N 次复现**(记忆有效):rebuild 后配置被烤回 `proxy_pass http://nofx:8080/api/`,
必须 `docker exec … sed` 改 `172.20.0.1:8080` 再 `docker restart`(不是 recreate)。
改前 `/api/` 502,改后 200。三处 bundle hash 一致:本地 dist / 线上 index.html / 容器内
均 `index-BTXqFCrh.js`。

回滚:`systemctl stop nofx` → `cp /root/.claude/jobs/5cbb3cf4/tmp/binbak/nofx_prev_9186326_20260804_1035 /opt/webstack/nofx/nofx`
→ `systemctl start nofx`;前端 `docker tag nofxmax-nofx-frontend:rollback-pre-evolrm-20260804
nofxmax-nofx-frontend:latest` → `docker compose up -d --no-deps nofx-frontend` → 重做 nginx sed。
**无 DB 迁移、无配置变更、无数据删除**,回滚不需恢复任何配置。

## 2026-08-04 Deploy: 挂单量档位归属 + 动态武装记忆不被擦除 (pid 815160, commit f997a6a)

后端 only。回滚点 `/root/.claude/jobs/5cbb3cf4/tmp/binbak/nofx_prev_3d91258_20260804_0240`
(md5 `b10fe32b1521f839d61713f7a2b2a568` = 线上 commit `3d91258`),新二进制
md5 `34b3c1a12de4c40225d6e3b393d4f14d`,`vcs.revision=f997a6ab`。
`MainPID=815160` == `pgrep -x nofx`(计数 1),`NRestarts=0`,health 200 @1.2ms。
分支 `deploy/protection-arm-memory` 从 **3d91258**(线上 commit)起 cherry-pick
dev 上的 `dd65899`+`6f19929`,落地 4 个文件、零 `trader/backtest/`
(`git diff --name-only e235edd..HEAD | grep -c trader/backtest/` = 0)。
dev 上那 9 个回测 commit 仍未上线,**每次部署仍必须走 cherry-pick 分支**。

**修的是一条完整因果链**(线上 SOLUSDT long claude-hhhl/OKX,23:01:47 起 **2.5 小时
654 次 reclaim + 438 轮 degraded** 未收敛,面板全红而交易所侧 5 张保护单一直都在场):

1. `22:57` **qty resize 误撤邻档 TP**(根因起点)。TP1(73.6931,目标 0.084)与
   TP2(73.7761,在场 0.06)相距 **0.1126%**,落在 `protectionPriceTolerancePct`
   0.2% 容差内。TP1 自己那张成交后,TP1 的档位目标把 TP2 的单当成自己的覆盖,
   判 `covers 0.06 of 0.084 (71.4%)` → 撤 → 重挂 → 复读;"取量最小的候选"保证
   每轮受害者都是无辜的小邻档。第 4 轮撞 OKX `51279`。
   **教训:价格容差只能识别"是不是同一档",绝不能用来"区分相邻档"** —— 相邻档
   本来就可能比容差更近。改为 `nearestQtyTargetForOrders` 唯一归属 + 角色排除矛盾。
   日志措辞也修了:原文写 `after position grew`,而同一秒的日志是
   `📉 Partial close: 0.420000 → 0.340000`,与事实直接矛盾。
2. `22:59:09` 重挂被拒 → reconciler 把 `protectionState` 写成 `reconcile_failed: …`
   → **覆盖掉 `native_trailing_armed`**;`23:01:47` 起再被改写成
   `exchange_protection_verified`,武装记忆永久丢失。
3. **死锁**:`protectionState` 是 AutoTrader 上的**内存 map**(不持久化),账本是
   持久化的。两者不一致后 —— 账本 armed 让武装路径判"已 armed,跳过重新武装"
   (`auto_trader_risk.go:1458` 的 `armedFingerprints` **只读账本、从不设内存状态**),
   内存 false 让 `classifyTrailing` **第一行 `if !o.Armed` 就 return false** →
   在场 trailing 单每轮记成 `staleTrail` → degraded → reclaim 重写账本 → 复读。
   **谁都不会去修对方。**
   ⚠️ 这也解释了此前查不通的 `claimedTrail=1` 与 `staleTrail=1` **并存**:
   认领集合里确实有那张单,但 `Armed=false` 让判断根本没走到集合那一步。

**⚠️ 必须诚实记录:churn 停下来不是新加的 rehydrate 的功劳。**
`🩹` 日志部署后至今 **0 次**。重启清空内存 map 后,是**早已存在**的启动恢复路径
`auto_trader.go:658-666` 从账本把 5 个仓位全部设回 `native_trailing_armed`
(连"全平档优先于部分档"的优先级都和新写的一致)。所以:
- **真正治本的是第 ① 和第 ② 条**(不再误撤 + 失败不擦记忆),消除死锁的**成因**;
- `rehydratedNativeTrailingState` 在**重启路径上是冗余的**,只在"运行中被改写、
  没重启"那条路上才有价值(即 SOL 这次的形态)。它比启动恢复**更严**:要求账本
  armed **且** 该 orderID 确实在交易所在场且是 TRAILING 单。
  ⚠️ **既有风险(本次没动)**:`auto_trader.go:658-666` 只读账本、不验交易所,
  陈旧 armed 记录会掩蔽 `missingTP` → 该补的止盈永远不补。下次碰这块时优先修它。
- `supersedeOlderArmedRecords` 的 `continue` 缺口修复**本次未被触发**(churn 停了就
  不再 persist,`🗂 Superseded` = 0)。SOL 那两条只差仓位数量的 armed 记录**仍在账本里**
  (都指向 ...704,同档同单,无害),等仓位平掉由
  `DeleteDynamicProtectionRecordsForInactive` 清理,或下次真武装时收敛。

**基线(02:25-02:40)→ 部署后(02:44-02:59)**:
`protected verified=true` 432 → 441;**degraded 48 → 0**;**SOL reclaim 48 → 0**;
ERRO 0 → 0;panic 0 → 0;`missingSL/TP=true` 0 → 0;`unexpectedSL/TP≠0` 0 → 0;
`Invalid token`/`all endpoints failed`/`Switched to fallback` 全 0。
5 个持仓(BTC/ETH/SKHYNIX/SOL LONG + XAU SHORT)全部 `state=protected verified=true`。
SOL 从 `degraded staleTrail=1 dynamicOwner=1(trail=0 be=1)` 变为
`protected staleTrail=0 dynamicOwner=2(trail=1 be=1)`。
⚠️ 基线 48 degraded 与 48 次 SOL reclaim **一一对应** —— degraded 全部来自 SOL,
其余 4 仓位当时就是干净的。别把这 48 当成全局劣化。

**门禁**:`go build`/`go vet` 干净;全量 `go test`(排除 `trader/backtest`)**22 包全绿
EXIT=0**;14 个新用例;**四处修复各做破坏性验证**,还原任一处都能让对应用例转红
(这是唯一能证明用例真的在钉不变量的方法,不能只看"测试通过")。
改动的 4 个文件 `gofmt` 干净(仓库基线本来就有 75 个未格式化文件,与本次无关)。

**两个被验证推翻的提议(记下来避免重走)**:
- 「把 `native_trailing` 纳入 store 层独占组」**会破坏真实不变量**:
  `TestDynamicProtectionStateKeepsNativeTrailingTiersArmed` 转红,两个并存的
  partial 档会互相降级。多档 trailing 并存是正常态,不是重复。
- 「`dynamicProtectionStageFromFingerprint` 的 `parts[4]` 应改 `parts[5]`」**是死代码**:
  该函数只在 `singletonDynamicProtectionGroup` 返回非空时可达,而它只对
  `break_even_stop` 返回非空 —— BE 指纹正好 5 字段,`parts[4]` 就是 stage;
  DD 的 11 字段指纹永远走不到那里。

回滚:`systemctl stop nofx` → `cp nofx_prev_3d91258_20260804_0240 /opt/webstack/nofx/nofx`
→ `systemctl start nofx`。无 DB 迁移、无配置变更、无前端改动。

## 2026-08-03 Deploy: 保护档位角色感知匹配 + 计划闸门 + 结构位梯队 (pid 778013, commit 3d91258)

后端 only。回滚点 `/root/.claude/jobs/5cbb3cf4/tmp/binbak/nofx_prev_37b07ad1_20260803_1930`
(md5 `37b07ad1…` = 线上 commit `e235edd`),新二进制 md5 `b10fe32b1521f839d61713f7a2b2a568`,
`vcs.revision=3d91258`。`MainPID=778013` == `pgrep -x nofx`(计数 1),`NRestarts=0`,health 200 @2.7ms。

**⚠️ 隔离性教训再次生效**:线上 `e235edd`,而 `e235edd..dev` 又夹进了**同一批 9 个回测 commit**
(上次特意排除的那批)。`go list -deps .` 仍命中 `nofx/trader/backtest`,`api/server.go:659`
的 `startBlockSimReplayer()` 仍是常驻任务 → 再次建 `deploy/protection-tier-guard` 从 `e235edd`
起只 cherry-pick `d8762ec`,落地 **10 个文件、零 `trader/backtest/`**(已用
`git diff --name-only e235edd..HEAD | grep -c trader/backtest/` 验证 = 0)。
**结论:图表那次的教训不是一次性的,dev 上只要有回测提交未上线,每次部署都必须走 cherry-pick 分支。**

**修的三个缺陷(同一症状"保护单被误撤且不重挂"的三个独立成因)**:
1. **档位身份只按价格** → `consumeAllowedProtectionSlot` 改为角色+价格三级匹配
   (角色+价格 → 无角色槽位 → 角色未知退回纯价格)。前置条件:Binance 适配器之前只解
   `ProtectionTier` **不解 `ProtectionRole`**,不补这个则改了匹配器也静默退回按价格匹配,
   75.7% 的碰撞面一动不动(342 份快照 259 份至少一对档位落在 0.2% 容差内)。
2. **计划闸门** `filterCancelIDsAgainstPlan`:两个 `no re-place` 撤单点都过闸门。判据是
   **"撤掉会不会让某档失去覆盖"**,不是"价位在计划里" —— 后者会让同价位重复单
   (TP 成交缩仓后遗留的整仓止损)永久驻留、每轮重复告警且永不收敛。
3. **TP1 消失的真正机制**(与碰撞无关):`validateProtectionPlanExecution` 在 reconcile 路径
   是 `quiet=true`,滤掉档位**连日志都没有**且**不登记进容忍名单** → 该档位不在计划里
   (missing 检测看不到→不重挂)+ 不在 allowed 里(判多余→撤掉)= **永久真空**。
   现场证据是 07:10:06 那行 `missingTP=false unexpectedTP=1` —— 两个数字同时成立只有这一种解释。

**结构位梯队**(用户要求"按有效性层层退",swing 第一优先):T1 swing 枢轴 → T2 order block
(之前只当收紧器,仍尊重 `PreferProvenLevels`)→ T3 聚类(**前高前低+成交密集区在这层**,
走 `DetectStructuralLevels`,≥2 触碰 / ≥30 置信度)→ T4 bar 极值兜底。**斐波那契刻意排除**
(推导价位,非市场防守过的位)。回看窗 **28→120 根**:T3 的触碰次数/密集区需要历史深度,
28 根直接把这层饿死 —— 这是 ZEC 边界为空的一部分。已核 OKX `/market/candles` 上限 300、
Binance 1500,且 `entry_structure_gate.go:182 structuralFetchBars=120` 本就取 120,负载不变。
`EntryTolATR` 默认 0.25×ATR:止损坐在结构**外侧**一段,不压在位上(压在位上等于把
"价格触碰"和"结构失效"当同一件事)。

**部署窗口**:仅 1 个持仓(claude HYPEUSDT LONG),风险最低。基线(重启前 15min):
47 次 `protected verified=true`、0 degraded、0 ERRO、0 panic。
**部署后核对(19:30→19:50)**:48 次 `protected verified=true`、**degraded 0**、ERRO 0、panic 0;
HYPEUSDT 5 张挂单全部 `unexpectedSL=0 unexpectedTP=0` —— **这是新匹配器最关键的验证点**
(若角色匹配误判,这里会立刻冒 unexpected);4 交易员权益快照均续写(11:41-11:46,间隔 20min);
`Invalid token`/`all endpoints failed`/`Switched to fallback` 全 0。
⚠️ **"401" 的 13 次命中是 `auto_trader_loop.go:401` 行号误匹配,不是鉴权失败** —— 且这行
恰好证明 AI 决策循环在跑(symbol 排队 wait)。下次别再被这个 grep 误导。
⚠️ `protection_ladder_anchor.go:155`(锚点 Rule 1"档位已成交")重启前后频率一致
(19:2x 53 次 → 19:3x 52 次),是 HYPEUSDT TP1/TP2 的既存行为,**与本次无关**。

**三条新日志(之前 `computeStructuralBoundary` 一行日志都没有,边界为空无法归因)**:
`🛡 Protection cancel guard`(救回了单 —— 同时是分类器误判的告警)、
`🧷 Protection plan narrowing`(门禁滤掉但在场)、`🧱 Structural boundary`(命中/未命中)。
部署后 20min 内三条均为 0:闸门只在救单时打印,结构位日志只在**开仓时**触发,
而唯一持仓是重启前就开的 → 符合预期,**不代表代码没生效**。

回滚:`systemctl stop nofx` → `cp nofx_prev_37b07ad1_20260803_1930 /opt/webstack/nofx/nofx`
→ `systemctl start nofx`。无 DB 迁移、无配置变更、无前端改动。

## 2026-08-02 Deploy: 竞赛对比图时间轴/缩放/持仓浮窗 (pid 711520, commit e235edd)

前后端都动了。后端回滚点 `/root/.claude/jobs/5cbb3cf4/tmp/dbbak/nofx_prev_7215def1_20260802_1750`
(md5 `7215def1…` = 线上 commit `ede8ea9`),新二进制 md5 `37b07ad1c89eead917a53f17837896d9`,
`vcs.revision=e235edd`。前端回滚镜像 `nofxmax-nofx-frontend:rollback-pre-chart-20260802`。
`MainPID=711520` == `pgrep -x nofx`,`NRestarts=0`,health 200 @0.7ms。

**⚠️ 隔离性:必须 cherry-pick,不能直接从 dev HEAD 构建。** 线上是 `ede8ea9`,而
`ede8ea9..dev` 之间除图表那一笔外还有 **9 个回测引擎 commit**。回测包**不是纯离线的** ——
`api/handler_blocksim.go` 把 `nofx/trader/backtest` 链进了生产二进制(`go list -deps .` 命中),
且 `api/server.go:659` 的 `startBlockSimReplayer()` 是常驻后台任务,调用面正是我改过的
`loader.go` / `portfolio_sim.go` / `model.go`(其中 `realizedPnL()` 漏乘 `exitFrac` 是行为修正,
会改变 blocksim 的输出数值)。故建 `deploy/chart-only` 分支从 `ede8ea9` 起只摘 `a420a0a`,
落地 16 个文件、零 `trader/backtest/` 文件。**下次部署前务必先跑
`git diff --name-only <线上commit>..HEAD` 看清附带了什么。**

**vcs.modified=true 是未跟踪的第三方目录** `coin_direction_final_claude_delivery_2026-07-29/`
造成的(`git check-ignore` 未命中),不是构建树脏。revision `e235edd` 仍可唯一定位。

部署前基线(01:53 CST 前):7 持仓(GPT 2/claude 3/CR 2),`protected verified=true` 104 次、
`degraded verified=false` **28** 次。
部署后核对(01:53→02:08):`protected verified=true` 87 次、**`degraded` 0 次**;0 panic/fatal;
4 交易员权益快照均正常续写;`Invalid token`/`all endpoints failed`/`Switched to fallback`/`401`
全 0;`Configured 2 fallback endpoint(s)` 各 trader 均打印;AI 决策与回撤监控循环正常。
**唯一 ERRO 是既存问题** `drawdown_claim_conflict.go:300`(SPCXUSDT same-tier fork):
重启前 20/h、重启后 24/h,频率一致 → 与本次无关。
⚠️ 更正:前一条部署记录写的"≈700/h"不适用于今天,今日实测 20-24/h,勿再照抄。

**端点行为实测(生产,新旧对比见 `competition-chart-timeaxis.md`)**:
`hours=0`「全部」从旧的 501 点 / 7.5 天变为 1502 点 / **06-16..08-03(48 天)**;
1D/3D/7D/30D/全部五个区间**各不相同**(旧代码后四个完全相同,都是 07-31 起);
降采样保峰谷已对数据库真值验证 —— 预算 100000/1500/300 三档下峰 **299.61** / 谷 **138.74**
与 `SELECT MAX/MIN(total_equity)` 完全一致。持仓字段 `position_count`/`position_count_recon`/
`position_notional`/`long_notional`/`short_notional`/`margin_used_pct` 均正常返回。

**前端 nginx 坑再次复现**(记忆有效):`docker compose build` 后配置被烤回
`proxy_pass http://nofx:8080/api/`,必须 `docker exec … sed` 改成 `172.20.0.1:8080`
再 `docker restart`(不是 recreate)。修前若不改,`/api/` 直接 502。
三处 bundle hash 一致:线上 index.html / `web/dist/assets` / 容器内均为 `index-N1zLDqyB.js`,
经前端代理(:3000)调新端点返回 1502 点且含持仓字段。

回滚:后端 `systemctl stop nofx` → `cp nofx_prev_7215def1_20260802_1750 /opt/webstack/nofx/nofx`
→ `systemctl start nofx`;前端 `docker tag nofxmax-nofx-frontend:rollback-pre-chart-20260802
nofxmax-nofx-frontend:latest` → `docker compose up -d --no-deps nofx-frontend` → 重做 nginx sed。
无配置变更,无 DB 迁移(只新增读路径与只读回放),回滚不需恢复任何配置。

## 2026-08-02 Deploy: 备用端点按 Priority 排序 + 6 端点全通 (pid 641545, commit ede8ea9)
备份 `/root/.claude/jobs/5cbb3cf4/tmp/dbbak/nofx_prev_5acea85f` (md5 `5acea85f…` = 线上 v1.17.7 / commit `6a817f2`),
新二进制 md5 `7215def1a329eb1581721af72ebc53de`,`vcs.revision=ede8ea9`。
`MainPID=641545`,`ActiveState=active`,0 PANIC / 0 ERROR。

**用户诉求**:「模型主备来源 claude 和 gpt 各 3 个,一共 6 个,测试是否都可用」→ 实测后修优先级 bug → 填 key 上线。

**首测 1/6 可用**(密钥更换前):claude 3 个全 401、openai 仅 1 个通。**根因不是网关而是密钥拓扑** ——
claude/openai 两行主密钥是**同一个 11 字符失效 token**,而 `switchToNextEndpoint` 对空 `api_key`
的处理是「不换密钥、沿用当前」,加上 `CallWithMessages` 每次调用前 `restoreToPrimary()`,
**空密钥备用端点永远拿主密钥去打** → 主密钥一坏,3 个空密钥备用同时陪葬,等于没有备份。
用户换密钥后复测 4/6,填完最后一个 key 后 **6/6 全通**。

**修掉的 bug(本次唯一代码改动)**:`switchToNextEndpoint` 只按数组下标推进,**从未读取 `Priority`**,
注释却写着「Find next available endpoint with higher priority」。生产 claude 行是
NovaI(prio=2) 在数组第 0 位、lt48(prio=1) 在第 1 位 → **实际先打低优先级、与运维意图相反**。
修法:在 `SetFallbackEndpoints` **入口**做一次 `sort.SliceStable`(升序),使下标遍历本身即优先级顺序。
放配置期而非切换期=不变量只有一处、每次故障转移少一趟比较;稳定排序让同 prio 保持书写顺序;
**排序作用于副本**——调用方切片来自 `GetFallbackEndpoints` 可能被复用,就地重排会污染他人数据。
`mcp/failover_test.go` 新增 4 例(含用生产配置复现的回归例);**摘掉排序验证过 3 例失败、装回全绿**。

**DB 改动(唯一一处)**:`ai_models` claude 行 `fallback_endpoints[0].api_key` 由空填为 novaiapi 专属 key。
openai 行同位置**库里已是用户给的值,脚本判定 no change needed 未写库**。改前值备份于
`tmp/dbbak/fallback_before.txt`。脚本 `set_fallback_key.py` 按 **base_url 子串**匹配目标而非数组下标
(JSON 重排也不会写错端点)、key 走环境变量不进 argv、参数化 UPDATE、写后回读校验、只打印掩码。

**⚠️ 两个未解决的问题(用户已知,本次未动)**:
1. **claude try#1(lt48)靠继承主密钥才通,自身 `api_key` 仍为空** → 主密钥轮换/失效时
   claude 主+try#1 会同时失效,只剩 try#2。用户只给了 try#2 的 key,无法一并修。
2. **备用密钥在库里是明文**,主密钥是 `ENC:`+AES-GCM。`UpdateWithFallbacks` 直接
   `json.Marshal` 进 text 列,不走 `EncryptedString` → 库文件泄露即泄露全部备用密钥。
   要修需带 `ENC:` 前缀探测的兼容读(存量明文)+ 一次迁移。

**端点实测特征(留给下次判断用)**:`us.novaiapi.com` **不是坏的,是慢且抖** ——
首测 60s 超时判失败,放宽 180s 后 16.78s 正常返回;`/v1/models` 探活 401 仅 0.4s(连通性没问题)。
生产 `DefaultTimeout=300s` 容得下,但 **`auto_trader_breakout.go:199` 把超时压到 20s**,
该端点在突破路径上贴着超时线(用户明确表示「超时没问题」,未改)。
顺带确认这些 OpenAI 兼容网关**确实支持 Anthropic 原生 `/messages`+`x-api-key`**
(claude 主/try#1 都走这条路正常返回),上次全 401 纯粹是密钥问题不是格式不兼容。

**探针 `cmd/aiendpointprobe`**(已进仓库):逐个实测主+全部备用端点。复用生产同一套 provider
客户端(线格式/鉴权头/请求体与真实调用一致)、复刻空密钥继承语义、复刻 Priority 排序;
`mode=ro` 只读开库不 AutoMigrate,密钥仅掩码。`-dry-run` 只解析不发请求。

部署后验证(16:16 CST 起):3 持仓 SOL/HYPE/SPCX+SKHYNIX 全 `state=protected verified=true missingSL=false`、
`Configured 2 fallback endpoint(s)` × 3 trader、`Invalid token` / `all endpoints failed` / `Switched to fallback` 均 0。

## 2026-08-02 Deploy: v1.17.7 OKX trailing 激活判据 + 51149 半成功回读 (pid 626448, commit 6a817f2)
备份 `/opt/webstack/nofx/nofx.bak-v11705-20260802-060338` (md5 `69a264009fcd69074dce330453d84321` = 线上 v1.17.5+ / commit `9a48070`),
新二进制 md5 `5acea85ffeac6fd18c16c5503a085b91`。构建 `go build -o /tmp/nofx_v1177 .`。
`MainPID=626448` == `pgrep -x nofx`,`NRestarts=0`,`ActiveState=active`,health 200 @0.9ms。

**用户要求"仅部署我的修复"。隔离性已核实**:线上是 `9a48070`,`9a48070..HEAD` 的非文档改动
**恰好只有这一笔**(`1fdbb8d` 是纯记忆文档),所以直接从 HEAD 构建 = 线上代码 + 仅此修复,
**不需要 cherry-pick**。改动见 `unified-protection-system.md` v1.17.7 段。

部署前基线(14:03 CST):4 持仓全 `verified=true claimedTrail=3`、7 张活 trailing、
`status=pending_activation` **57 次 / `activated` 0 次**(缺陷特征)、`NOT effective` 0、熔断 0、51149 0。

部署后核对(14:04→14:22):
- **零 churn**:撤单 **0** 次、挂单 **0** 次,GetOpenOrders 稳定在 6/7(与基线同样的 6/7 波动)
  → 判据改动没有惊动任何在场交易所挂单,符合"只改判据与回读"的设计意图;
- 4 持仓(SKHYNIXUSDT/SPCXUSDT/ZECUSDT LONG + HYPEUSDT SHORT)全部
  `state=protected verified=true staleTrail=0`,`exchange protection verified (preserving dynamic state=native_trailing_armed)`;
- WARN 0、panic 0;
- **唯一 ERRO 是既存问题**:`drawdown_claim_conflict.go:300` 同一档被两张单应答
  —— 重启后 168 条,而**同日重启前已有 9679 条**,频率一致(前 ≈692/h,后 ≈720/h),
  与本次改动无关;该日志自述是**有意保留全部认领**(撤错会掀掉在工作的那张单),是 same-tier fork 路径的职责。

**⚠️ 尚未获得的验证 —— 主修复点没有观测窗口**:`status=activated` 是否开始出现,
需要**已武装的 DD 档**才会打印那行(`auto_trader_risk.go:2095`)。重启后 4 个持仓全是
`🟣 Drawdown arm pending`(利润未到武装线,如 SKHYNIX 2.88% < 5.15%),所以核心判据修复
(激活状态改读订单行自身)**目前只有单元测试背书,没有实盘证据**。
下次有持仓武装 DD 时必须回看:`grep -oE 'status=(pending_activation|activated)'`,
预期 `activated` 应从"4 天 5 次"变成常态。**51149 回读路径同理**(4 天只发生 6 次,属稀疏事件)。

回滚:`systemctl stop nofx` → `cp nofx.bak-v11705-20260802-060338 nofx` → `systemctl start nofx`。
无配置变更,无 DB 迁移,回滚不需要恢复任何配置。

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

## 2026-08-01 Deploy: Claude-R 15m gate parameterization + RR audit (pid 494713)

Commits (option B order — session_range committed & verified FIRST, then shipped together):
`9d1c269` session_range → `fac0f1e` parameterization + RR audit → `d3add9f` UI → `5035861` harness.
Binary md5 `ab68255bd0398512b319af67a015b405`; backup `nofx.bak-20260801-044713`
(prev md5 `61d0da2e...` — NOTE: that is a **binary md5 prefix, not a git commit**, I mislabelled it
for several turns); config backup `/tmp/cfg_backup_20260801-044713/`.

**Ordering matters and is not interchangeable: binary FIRST, then config.** Writing
`adx_period=10` under the old binary silently falls back to ADX(14)+threshold 30 — the
single worst measured cell (-0.120 vs baseline -0.071).

Code:
- `store/strategy.go` `RegimeGateParams.ADXPeriod` (0/unset = 14, existing configs unchanged).
- `trader/regime_gate.go` `adx_weak` honours the period; `chart_trend` honours `Lookback` (default 2).
- `trader/chart_trend_gate.go` signature widened with `pivotLB`.
- `trader/rr_audit.go` (new) + hook in `protection_execution.go` — **audit only, never alters the plan**.
  Placed after the AI/config merge, target clamping and structural fallback have all run, and
  `req.EntryPrice` is the exchange-confirmed fill: the one point where declared RR and placed RR
  are both known. Threshold `rrAuditMaterialGap=0.05` pinned to kernel tolerance by test.
- Frontend: `RegimeGatesEditor.tsx` (+`adx_period`, +`lookback`), `types/strategy.ts`,
  `i18n/strategy-translations.ts` (evidence split by primary timeframe).

Config (Claude-R only): regime_gates counter_trend(96) / chop_reject / adx_weak(thr 30, **period 10**)
all enforce; entry_gate structural `lb=4 n=3 pct=0` **audit_only**.
Basis and the reasons audit_only ≠ enforce: `.ai-memory/entry-structure-gate-backtest.md` §15m 专项.

Verification: `go test ./...` 23 pkg ok, `-race` clean, tsc clean, vite 35.71s, madge 157 files
no circular deps. Post-restart pid 494713, NRestarts=0, health 200, 0 ERROR / 0 panic,
all 9 positions `state=protected verified=true`, zero unexpected/stale/foreign orders,
managed-monitor co-run (双保险) posture intact.
All four changes observed live in the 13:16 CST cycle:
`[chop_reject] blocked ETHUSDT open_long: consensusChop=true`;
`[counter_trend] blocked SOLUSDT open_long: slope96=TREND_DN side=LONG`;
`⚠ structural_alignment_missing` (⚠ not ✗ — confirms audit-only);
`📐 RR audit ZECUSDT LONG: declared=2.32 planned_nearest=0.35 (-1.97) planned_weighted=0.66 tiers=1SL/4TP mode=manual`
(字段原名 placed_*，2026-08-01 更正为 planned_* —— 审计跑在 protection_execution.go:132，
而 below-minimum 档位丢弃在 :809，故审计测的是计划而非交易所实际挂单。同一笔 ZEC 计划 65% 阶梯
加权 RR 0.66，实际只挂上 53%、真实 0.53。);
chart_trend count 0 (deliberately not enabled — 100/100 grid cells negative on 15m).

**Reading the RR audit gap correctly (mode=manual):** the ZEC line is arithmetically right —
ladder is 4 ATR-unit TP tiers 1.1/1.7/2.5/3.6 ATR at 20/18/15/12% against a 3.2 ATR SL, so
nearest = 1.1/3.2 = 0.34 and weighted = 2.05/3.2 = 0.64. But in `manual` mode the gap measures
**"AI thesis vs the operator's own ladder", not AI overstatement** — the ladder is ours, not the
model's. And only ~65% of size exits via the ladder; the rest trails. So a large gap here is
NOT by itself evidence of a bad AI call. Treat it as a prompt for "is the ladder aligned with
the thesis", not as an AI-accuracy metric.

Deployment was blocked twice by the Claude Code auto-mode classifier (`systemctl stop/start nofx`);
verified no half-deploy state each time (md5 unchanged, pid alive, health 200) and handed the user
manual commands rather than working around the denial.

## Last Updated
2026-08-04 - 剥离进化画像引擎 (pid 896148, md5 191404d2, commit 4cc1a8f)

## 2026-08-01 Deploy: 复盘面板 ATR/BE/分页 + 屏蔽低波动独立开关 (pid 579360, commit 9a48070)
md5 69a264009fcd69074dce330453d84321 ← 上一版 37dfd393
回滚二进制: /root/.claude/jobs/5cbb3cf4/tmp/nofx.rollback_7e27aff_20260801_1930
两个 commit 一起上: a8284e1(复盘面板) + 9a48070(低波动门禁)
**前端也重建了**(Docker 容器 nofx-frontend,不是静态目录):
`docker compose build nofx-frontend` → `up -d --no-deps nofx-frontend`,
bundle hash index-BON6paYF.js 与本地构建一致,api_proxy=200。

### BE 档位显示:是显示缺陷,不是逻辑错
`triggerPct` 是**激活阈值**,`triggerPrice` 是按 **offset** 停放的止损价 ——
两个不同价位被并排渲染成 `BE1+1.3%@1856.69`,读起来像"1.3% 对应该价"。
按 entry 1862.28 逐一核对:BE1 offset 0.3% → 1856.693,BE2 offset 1.0% →
1843.657,与面板一致。解析后单位确实是百分比。改成「激活+1.3% → 止损@…」。
潜在真 bug 仍在:若解析被跳过(`no ATR for %s; falling back to configured
percents`),会把**原始 ATR 倍数**当百分比打印。
查不到 BE 配置何时改的:`trader_protection_config_history` **0 行**。

### ATR 必须落快照,否则复盘拿不回分母
`DeleteFrozenATRRecord` 在平仓时删掉 frozen-ATR 记录,所以
`protection_plan_snapshots` 加了 `atr_value`/`atr_timeframe`。没有它面板只能
用 `peak_pnl_pct / peak_atr_mult` 反算,行程≈0 时会炸(MFE +0.00%/+0.01×)。
读取用**新增的 `frozenATRForPositionReadOnly`**,不能复用 get-or-freeze 变体:
后者未命中时会实地取值并写入记录 = **让上报路径铸造交易状态**,可能把持仓
绑到开仓数小时后测得的 ATR。生产库已确认加列成功,319 行保留,旧行 ATR=0。
postgres 侧 `InitTables` 表已存在时提前返回,故必须补显式 ADD COLUMN。

### 我自己犯的错:把 floor 放在了父开关的早返回之后
`evaluateMarketStateGate` 开头有 `if !regimeCfg.Enabled { return }`,而生产库
`regime_filter.enabled` 在 **4 个实盘策略里 3 个为 0**(仅 BN-chart+ 开)。
第一版把 floor 放在其后 = 重犯 `entry_gate.min_atr14_pct` 的老问题(挂在
`entry_structure.enabled` 下,四策略全关,可设置永不执行),正好违反用户要的
"不依赖父开关"。已提到早返回**之前**。
**8/8 测试全绿没抓到**,因为所有测试都用 `Enabled: true` → 补
`TestVolatilityFloorIndependentOfRegimeFilterSwitch` 钉住,并断言天花板
**仍**留在早返回后(防止把 floor 上移时把整个门禁漏出来)。
同一处不一致另有两份:`entryPipeline.ts` 的 `active` 跟着父开关(会把正在
拦单的门显示成未生效);`api/strategy.go` 校验包在 `if regime.Enabled` 里,
恰好在"父开关关+floor 开+阈值留 0"这个最需要提示的组合下静默。均已修。
字段仍挂 `RegimeFilterConfig` 只为**分组**(与 `BlockHighVolatility` 是同一
测量上的一个窗口),不是依赖。

### 默认关闭是数据决定的,不是保守
519 笔已平仓按**品种各自** ATR 中位数切半:低波动半 234 笔 63% 胜率 **+20.01**,
高波动半 239 笔 52% **-132.69**,11 个品种 8 个同向。**低波动是当前系统赚钱的
那一半**,默认开启会砍掉盈利来源。该开关只针对被手续费吃掉的尾部(ATR% ~0.1
时 0.9-ATR 目标 0.106% vs 往返成本 0.12% = 手续费占毛利 113%,打中止盈也亏)。
实测数字已写入代码注释,防止后人"顺手打开"。
上线零行为变化已确认:5 个策略 `block_low_volatility`/`min_atr14_pct` 键全
ABSENT → false/0;`getRegimeFilterConfig` 不套默认值;
`GetDefaultStrategyConfig` 只在"无配置兜底"时用,不回填现存策略。

### min_eff_pct 档位塌陷:仍未修,等回测
0.3% 地板**不能去掉**:ATR 0.118% 时 0.9-ATR 目标 0.106% < 往返成本 0.12%。
但它导致 TP1/TP2/TP3(1.1/1.7/2.5 ATR = 0.130/0.201/0.295%)全钳到同一价,
53% 仓位 + BE1 + Drawdown 挤在 1856.693。315 快照 11 个重叠,2 个 XAUUSDT
四档全塌。0.118% 是第 **0.4** 百分位(P5=0.272%, P50=0.759%),真问题但罕见。
用户要求:主动收敛与 floor 阈值都要**按 0.1% 精度逐个回测**看实际收益变化,
回测出来再决定。改下单逻辑,未经确认不动。

## 2026-08-01 Deploy: RR 审计同时上报计划与实际可挂 (pid 532689, commit 7e27aff)

## 2026-08-01 Deploy: RR 审计同时上报计划与实际可挂 (pid 532689, commit 7e27aff)
md5 37dfd393a8ad721b75505f787580c000 ← 上一版 48edc644
回滚二进制: /root/.claude/jobs/5cbb3cf4/tmp/nofx.rollback_48edc644_20260801_115556

纯审计路径(只打日志,不改挂单),故当日第二次部署。

问题。审计钩子在 applyPostOpenProtection:138,而丢档发生在下游
validateProtectionPlanExecution,所以字段只能诚实叫 planned_*。ZEC 那次
65% 只挂上 48.3%,审计一行都没提示 —— **掉的是最远档,nearest RR 一模一样**。

两处改动:
1. 新增 `ProfitCoveragePct` / `profitCoveragePct(plan)`。RR 本身无法暴露梯度
   变窄。特别注意塌缩后的形态:全档跌破后 plan 被改写成单个全仓
   TakeProfitPrice、梯度为空,直接对梯度求和会把 100% 报成 0%,读起来像保护
   全丢、恰好相反 —— 所以回落到单价字段判 100,与 protectionLegPrices 一致。
2. 新增 `auditPlannedAndPlacedRiskReward`,对两个 plan 各审一次(审计函数是
   纯函数,由调用方跑两遍;第二遍用 quiet=true 跑挂单路径即将用的同一过滤器)。
   只在两者有差异或 declared 偏离超阈值时打印,健康入场无输出。

放在 applyPostOpenProtection 而非 placeAndVerifyProtectionPlan 内,是为了避开
重试循环 —— 否则每次重试打一行。只读性由 TestAuditPlannedAndPlacedIsReadOnly
逐档比对前后快照锁定;quiet=true 避免与挂单路径几毫秒后的告警重复。

placed_* 是忠实预测而非保证:mark price 是挂单前几微秒读的,快速行情仍可能变。
命名 placed_* 因为它就是挂单路径即将调用的同一函数、同样入参。

三种形态实测:
- 全档跌破塌缩: planned_cover=65% → placed_cover=100%,RR 0.53→1.41
  (向上塌缩的收益在审计里直接可见)
- 部分变窄: declared 偏差 +0.00 却 cover 65%→38%,正是旧逻辑漏掉的那类
- 完全健康: 无输出

部署后 3 分 44 秒 / 4723 行: 0 ERROR、0 WARN、0 撤单,132 条 verified=true
无一 false,unexpectedTP/staleTrail 全 0,99 条 not re-placing。RR 审计暂无输出
(本周期无新入场,且存量仓位不走该路径),符合预期。

## 2026-08-01 Deploy: 挂不上的档位向上取整到最小张数 + 塌缩改向最远档 (pid 524730, commit a7ff9e3)
md5 48edc6446cb4c2f5aea340bed169ae0e ← 上一版 ab68255bd0398512b319af67a015b405
回滚二进制: /root/.claude/jobs/5cbb3cf4/tmp/nofx.rollback_ab68255b_20260801_110517

问题。线上 ZECUSDT 配置 65% 的止盈梯度实际只挂上 48.3%。12%/3.6-ATR 档算出
0.99305275 张,差 0.007 张不到 MinSz 1,被 ValidateProtectionQuantity 在取整前
判死。日志原文: `qty=0.009931 err=quantity 0.99305275 below min contracts`。
历史 1690 笔 OKX 仓位中 181 笔(10.7%)少挂至少一档,平均实际覆盖率仅 34.4%。

根因是两套量化规则打架。四条挂单路径(SetStopLoss/SetStopLossTagged/
SetTakeProfit/SetTakeProfitTagged)都是"向上夹到 MinSz 再 formatSize",而校验器
拿未夹取的原始张数比 MinSz。同一个量两种口径,与 2026-07-28 ZEC 挂撤循环同类根因。

四项改动:
1. protectionSizeForQuantity 成为唯一量化入口,校验器与四条挂单路径共用。
2. ValidateProtectionQuantity 改判"解析后"张数;LotSz 同样改判解析后
   (421/421 个 OKX USDT 永续 LotSz==MinSz,此项属潜在防御)。
3. ladderWouldOversell 守卫(trader/protection_oversell.go)。部分跌破安全:
   最大档存活 ⇒ 持仓 >= 1/maxRatio 张,N 档各夹 1 张最多挂 N 张,在用梯度均满足
   N < 1/maxRatio。全档跌破会超发(1 张持仓 4 档 → 4 张),继续走塌缩。
   等号也拦:部分梯度(intendedPct<100)若吃满全仓则丢弃,否则 runner 没了;
   单档 100% 止损 intendedPct==100,等于全仓是设计意图,放行。不确定一律放行。
4. 塌缩方向最近档 → 最远档 farthestLadderTakeProfitPrice。整仓单价离场时选最近档
   等于把全仓封在第一个减仓位,ZEC 量级加权 RR 比不挂还差 -0.035。下行由不依赖
   止盈梯度的机制承担:BE1 1.3 ATR、BE2 2.5 ATR、dd1 2.5 ATR+1.5 回吐、
   giveback_guard 1.15 ATR,全在 3.6 ATR 远端之下先武装。最远档离 mark 更远,
   更不易触发 OKX 51279。

离线回放 1690 笔:181 笔部分跌破修复,覆盖率 34.4%→67.9%(+33.4pp),最大 80%,
无一超 100%;76 笔走塌缩,其中 34 笔若无守卫会真超发,已被拦下。

部署教训(重要)。`cp` 覆盖运行中的二进制会 `Text file busy`,restart 后仍跑旧码
且 md5 不变——必须 `systemctl stop` → `cp` → `systemctl start`,并用 md5 确认。
第一次尝试就踩了这个,pid 524618 跑的还是 ab68255b。

部署后核查(锚点 = 日志行 407009,即 19:06:25 CST = systemd ActiveEnterTimestamp
11:06:25 UTC;注意日志 CST 而 shell UTC,且不能用 awk 字符串比时间戳切分——非
时间戳开头的行里任何字母都 > '0' 会误匹配)。3096 行内:0 ERROR/panic/FATAL、
0 WARN、77 条 state=protected verified=true 且无一 false、0 次撤单。
207 条 `Ladder TP tier already executed ... not re-placing` 证明防挂撤循环仍生效。
新分支尚未触发:存量仓位已武装且 verified,reconciler 不会重挂,改动在下一次
梯度挂单时生效。

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
