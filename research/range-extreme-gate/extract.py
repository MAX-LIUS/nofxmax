#!/usr/bin/env python3
"""Extract, for every closed live trade, the geometry of its entry relative to
structure — measured from the SAME prompt text the AI saw at decision time.

Why from the prompt and not recomputed: the prompt IS the causal record. Any
re-derivation risks the harness-fidelity problem documented in
entry-structure-gate-backtest.md (70.7% ceiling from kline re-alignment).

Key variable (the one the user's request is really about):
  opp_atr = distance from entry to the nearest OPPOSING zone, in ATR units.
            LONG  -> nearest RESISTANCE above entry
            SHORT -> nearest SUPPORT below entry
This is NOT the "NEAREST ZONE" line: for a LONG, support below is protective,
not opposing. Reading that line directly would score CLUSDT (0.0x support
below a long = ideal) as if it were SKHYNIX (0.4x support below a short =
shorting into a wall). They are opposite situations.
"""
import datetime, json, re, sqlite3, sys, os

DB = "/opt/webstack/nofx/data/data.db"
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "trades.jsonl")

ZONE_RE = re.compile(
    r"^\s*(\d+)\.\s*\[([ABC])\]\s*([\d.]+)\s*[–-]\s*([\d.]+)\s*\((.*?),\s*conf=(\d+),\s*([\d.]+)x ATR,\s*(\d+) touches"
)
HVN_RE = re.compile(r"^-\s*HVN[^:]*:\s*(.+)$")
POC_RE = re.compile(r"^-\s*POC=([\d.]+)")
PD_RE  = re.compile(r"^-\s*prev_day_(high|low)=([\d.]+)")
PW_RE  = re.compile(r"^-\s*prev_week_(high|low)=([\d.]+)")
FIB_RE = re.compile(r"swing_low=([\d.]+)\s+swing_high=([\d.]+)")
ATRPCT_RE = re.compile(r"ATR14?%?\s*[=:]\s*([\d.]+)")


def parse_symbol_block(prompt: str, symbol: str):
    """Return the slice of prompt describing `symbol`, or None."""
    start = prompt.find(f"=== {symbol} Market Data ===")
    if start < 0:
        return None
    nxt = prompt.find("Market Data ===", start + 20)
    end = prompt.find("=== ", nxt - 60) if nxt > 0 else -1
    return prompt[start: end if end > start else len(prompt)]


def extract_geometry(block: str, entry: float, is_long: bool):
    """Pure geometry from the decision-time text. Returns dict or None."""
    if not block or entry <= 0:
        return None
    res, sup, hvns = [], [], []
    poc = pdh = pdl = pwh = pwl = None
    swing_lo = swing_hi = None
    section = None
    for raw in block.splitlines():
        line = raw.rstrip()
        if "RESISTANCE ZONES" in line:
            section = "r"; continue
        if "SUPPORT ZONES" in line:
            section = "s"; continue
        if line.startswith("NEAREST ZONE"):
            section = None
        m = ZONE_RE.match(line)
        if m and section:
            lo, hi = float(m.group(3)), float(m.group(4))
            rec = dict(grade=m.group(2), low=lo, high=hi,
                       conf=int(m.group(6)), atr_dist=float(m.group(7)),
                       touches=int(m.group(8)), sources=m.group(5))
            (res if section == "r" else sup).append(rec)
            continue
        m = HVN_RE.match(line)
        if m:
            hvns = [float(x) for x in re.findall(r"[\d.]+", m.group(1))]
        m = POC_RE.match(line)
        if m: poc = float(m.group(1))
        m = PD_RE.match(line)
        if m: 
            if m.group(1) == "high": pdh = float(m.group(2))
            else: pdl = float(m.group(2))
        m = PW_RE.match(line)
        if m:
            if m.group(1) == "high": pwh = float(m.group(2))
            else: pwl = float(m.group(2))
        m = FIB_RE.search(line)
        if m and swing_lo is None:
            swing_lo, swing_hi = float(m.group(1)), float(m.group(2))

    # ATR in price units: recover by inverting a zone's reported x-ATR distance.
    #
    # The prompt rounds that distance to ONE decimal, so the inversion
    # atr = d / atr_dist amplifies rounding error as atr_dist -> 0. Taking the
    # first zone above 0.05x produced impossible ATRs (0.00%-0.2% of price on
    # 1h crypto) and one 343-ATR "trade" that alone dominated a bin mean.
    #
    # Two defences: (a) only invert zones at >= 0.3x, where one-decimal
    # rounding is at most ~17% error, and take the MEDIAN across all such
    # zones rather than the first; (b) reject the trade outright if the
    # recovered ATR is outside a plausible band, rather than emit a bad row.
    cands = []
    for z in res + sup:
        if z["atr_dist"] >= 0.3:
            mid = (z["low"] + z["high"]) / 2.0
            d = abs(mid - entry)
            if d > 0:
                cands.append(d / z["atr_dist"])
    if not cands:
        return None
    cands.sort()
    atr_price = cands[len(cands) // 2]
    if atr_price <= 0:
        return None
    atr_pct = atr_price / entry * 100.0
    if not (0.15 <= atr_pct <= 20.0):
        return None

    def atr_units(px):
        return abs(px - entry) / atr_price

    # OPPOSING zone: what a move against the trade runs into first.
    opp_zones = res if is_long else sup
    opp = None
    for z in opp_zones:
        mid = (z["low"] + z["high"]) / 2.0
        if (is_long and mid > entry) or (not is_long and mid < entry):
            u = atr_units(mid)
            if opp is None or u < opp["atr"]:
                opp = dict(atr=u, grade=z["grade"], conf=z["conf"],
                           touches=z["touches"], sources=z["sources"])

    # PROTECTIVE zone: the mirror image of opp_atr, on the side that HELPS the
    # trade (LONG -> support below, SHORT -> resistance above). This exists
    # only as the displacement placebo required by CRITERIA.md: it is built
    # from the same zone list, same ATR scale, same parser, differing ONLY in
    # sign. If prot_atr predicts outcome as well as opp_atr does, then the
    # measured effect is volatility/zone-density, not entry geometry.
    prot_zones = sup if is_long else res
    prot = None
    for z in prot_zones:
        mid = (z["low"] + z["high"]) / 2.0
        if (is_long and mid < entry) or (not is_long and mid > entry):
            u = atr_units(mid)
            if prot is None or u < prot:
                prot = u

    # Nearest opposing HVN / POC (volume-dense shelf).
    dense = []
    for px in hvns + ([poc] if poc else []):
        if (is_long and px > entry) or (not is_long and px < entry):
            dense.append(atr_units(px))
    dense_atr = min(dense) if dense else None

    # Nearest opposing period level (prev day/week extreme).
    per = []
    for px in [pdh, pdl, pwh, pwl]:
        if px and ((is_long and px > entry) or (not is_long and px < entry)):
            per.append(atr_units(px))
    per_atr = min(per) if per else None

    # Range position: 0 = at swing low, 1 = at swing high.
    range_pos = None
    if swing_lo and swing_hi and swing_hi > swing_lo:
        range_pos = (entry - swing_lo) / (swing_hi - swing_lo)

    return dict(
        atr_price=atr_price,
        opp_atr=opp["atr"] if opp else None,
        opp_grade=opp["grade"] if opp else None,
        opp_conf=opp["conf"] if opp else None,
        opp_touches=opp["touches"] if opp else None,
        prot_atr=prot,
        dense_atr=dense_atr, period_atr=per_atr, range_pos=range_pos,
        n_res=len(res), n_sup=len(sup),
    )


def main():
    con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    cur = con.cursor()
    cur.execute("""
        SELECT p.id, p.trader_id, p.symbol, p.side, p.entry_price, p.exit_price,
               p.realized_pnl, p.entry_time, p.exit_time, p.close_reason,
               p.peak_atr_mult, p.trough_atr_mult, p.entry_quantity, p.leverage
        FROM trader_positions p
        WHERE p.exit_time > 0 AND p.exit_price > 0 AND p.entry_price > 0
        ORDER BY p.entry_time
    """)
    trades = cur.fetchall()
    print(f"closed trades: {len(trades)}", file=sys.stderr)

    # Phase 1: index (trader, created_at, id) WITHOUT touching input_prompt.
    # The previous correlated-subquery form made SQLite scan every large prompt
    # blob once per trade (1922 x ~20k rows) and never finished. Here each blob
    # is fetched at most once, by primary key.
    cur.execute("""
        SELECT id, trader_id, created_at FROM decision_records
        WHERE length(input_prompt) > 1000 ORDER BY trader_id, created_at
    """)
    idx = {}
    for did, tid, ca in cur.fetchall():
        idx.setdefault(tid, []).append((ca, did))
    print(f"indexed traders: {len(idx)}", file=sys.stderr)

    def _stamp(sec):
        return datetime.datetime.fromtimestamp(
            sec, datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

    def pick(tid, et_ms):
        """Resolve the decision record that CAUSED this entry.

        Not "latest before entry": the record is persisted ~10-12s AFTER the
        position opens (decide -> place -> persist), while the decision cycle
        is ~20min. Selecting "before entry" therefore lands on the PREVIOUS
        cycle every time -- verified against two hand-checked trades, where it
        gave opp_atr 0.752/2.300 instead of the true 0.438/2.500.

        So: take the first record at or after (entry - PRE) and accept it only
        if it lands within POST of entry; otherwise fall back to the newest
        record before entry.
        """
        PRE, POST = 60, 300
        rows = idx.get(tid)
        if not rows:
            return None
        lo_stamp = _stamp(et_ms / 1000 - PRE)
        hi_stamp = _stamp(et_ms / 1000 + POST)
        lo, hi = 0, len(rows)
        while lo < hi:
            mid = (lo + hi) // 2
            if rows[mid][0][:19] < lo_stamp:
                lo = mid + 1
            else:
                hi = mid
        if lo < len(rows) and rows[lo][0][:19] <= hi_stamp:
            return rows[lo][1]
        return rows[lo - 1][1] if lo > 0 else None

    n_ok = n_noprompt = n_nogeom = 0
    cache = {}
    with open(OUT, "w") as fh:
        for t in trades:
            (pid, tid, sym, side, entry, exitp, pnl, et, xt, reason,
             mfe, mae, qty, lev) = t
            did = pick(tid, et)
            if did is None:
                n_noprompt += 1
                continue
            if did not in cache:
                if len(cache) > 40:
                    cache.clear()
                cur.execute(
                    "SELECT input_prompt FROM decision_records WHERE id=?",
                    (did,))
                r = cur.fetchone()
                cache[did] = r[0] if r else None
            prompt = cache[did]
            if not prompt:
                n_noprompt += 1
                continue
            block = parse_symbol_block(prompt, sym)
            is_long = side.upper() == "LONG"
            geom = extract_geometry(block, entry, is_long) if block else None
            if not geom:
                n_nogeom += 1
                continue
            n_ok += 1
            rec = dict(id=pid, trader=tid[:10], symbol=sym, side=side.upper(),
                       entry=entry, exit=exitp, pnl=pnl, entry_time=et,
                       decision_id=did, reason=reason, mfe_atr=mfe, mae_atr=mae,
                       qty=qty, lev=lev, **geom)
            fh.write(json.dumps(rec) + "\n")
    print(f"ok={n_ok} no_prompt={n_noprompt} no_geom={n_nogeom}", file=sys.stderr)


if __name__ == "__main__":
    main()
