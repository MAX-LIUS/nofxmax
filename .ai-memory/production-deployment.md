# Production Deployment - Critical Information

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
2026-07-29 - BE 保本止损按仓位身份+档位收敛（4 层根因全修，止住 BE 单堆积与挂撤循环）

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
