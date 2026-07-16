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

2. **Copy to production**:
   ```bash
   cp /root/projects/nofxmax/nofx /opt/webstack/nofx/
   ```

3. **Restart from production directory**:
   ```bash
   # Find and stop old process
   old_pid=$(ps aux | grep "/opt/webstack/nofx/nofx\|./nofx" | grep -v grep | awk '{print $2}')
   kill -9 $old_pid
   
   # Start from production directory
   cd /opt/webstack/nofx
   nohup ./nofx > /tmp/nofx.log 2>&1 &
   ```

4. **Verify**:
   ```bash
   # Check process
   ps aux | grep nofx | grep -v grep
   
   # Check logs for trader count (should be 4)
   grep "Total loaded trader configurations" /tmp/nofx.log
   
   # Check API
   curl http://localhost:8080/api/health
   ```

## Common Mistakes to Avoid

❌ **WRONG**: Running from `/root/projects/nofxmax/` → uses empty dev database
❌ **WRONG**: `/app/data/nofx-hotfix` → old binary path, doesn't exist
✅ **CORRECT**: Running from `/opt/webstack/nofx/` → uses production database with 4 traders

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
