#!/usr/bin/env python3
"""Full grid: 3 patterns × 4 timeframes × {fixed params}. Takes ~90s."""
import sys, statistics as st
sys.path.insert(0, '.')
from engine import backtest

SYMS = ["SPYUSDT", "QQQUSDT", "NVDAUSDT", "TSLAUSDT", "MSTRUSDT", "XAUUSDT"]
PATS = ["wick", "engulf", "reclaim"]
MULTS = [1, 2, 3, 6]  # 5m, 10m, 15m, 30m

results = []
for sym in SYMS:
    for pat in PATS:
        for mult in MULTS:
            trades = backtest(sym, mult, pat, N_recent=3, M_prior=6,
                              k_atr=0.5, R_mul=2.0, arm_R=1.0, trail_atr=1.0)
            if len(trades) < 5:
                continue
            r = [t[2] for t in trades]
            results.append({
                "sym": sym, "pat": pat, "tf": f"{mult*5}m", "n": len(trades),
                "mean": st.mean(r), "med": st.median(r),
                "win": sum(1 for x in r if x > 0) / len(r)
            })
            print(f"{sym:<12} {pat:<7} {mult*5:>3}m  n={len(trades):<3} "
                  f"mean={st.mean(r):+.3f} med={st.median(r):+.3f} win={sum(1 for x in r if x>0)/len(r):.1%}",
                  file=sys.stderr)

print(f"\n{'sym':<12}{'pat':<9}{'tf':>5}{'n':>5}{'mean':>8}{'med':>8}{'win':>7}")
for x in sorted(results, key=lambda d: d["mean"], reverse=True)[:20]:
    print(f"{x['sym']:<12}{x['pat']:<9}{x['tf']:>5}{x['n']:>5}{x['mean']:>+8.3f}"
          f"{x['med']:>+8.3f}{x['win']:>7.1%}")
