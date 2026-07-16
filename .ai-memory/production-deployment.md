# Production Deployment - Critical Information

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

4. **Start from production directory**:
   ```bash
   cd /opt/webstack/nofx
   nohup ./nofx > /tmp/nofx.log 2>&1 &
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

## Database Verification

Expected trader count: **4 active traders**

```bash
# Check database
sqlite3 /opt/webstack/nofx/data/data.db "SELECT COUNT(*) FROM strategies WHERE is_active = 1;"
# Should return: 2 (strategies table) + underlying traders = 4 total
```

If startup log shows "0 traders", you're using the **wrong database**.

## Last Updated
2026-07-16 - After attribution fix deployment
