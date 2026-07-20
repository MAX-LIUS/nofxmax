# Production Deployment - Critical Information

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

## Last Updated
2026-07-20 - entry-gate closed-candle fake_retest confirmation deployment

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
