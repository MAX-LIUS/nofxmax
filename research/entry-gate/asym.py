"""Is the SHORT-over-LONG advantage structural, or a bear-market artifact?

This decides whether "be less long" is a gate finding or just sample fitting.
If the sign flips with market direction, it is NOT a gate — it is a beta call,
and we have cheaper ways to express beta.
"""
import numpy as np, pandas as pd
import evalgate as E

ev = E.load()
g = ev.groupby("q")
tab = pd.DataFrame({
    "n": g.size(),
    "long": g.long_bps.mean(),
    "short": g.short_bps.mean(),
})
tab["short_minus_long"] = tab["short"] - tab["long"]
# Market direction proxy: median trailing 72h return across all events in the quarter.
tab["mkt_ret72_med"] = g.ret72.median()
print("=== per-quarter legs (bps/trade) ===")
print(tab.round(2).to_string())

s = tab["short_minus_long"]
frac, p, npos, n = E.sign_test(s)
print(f"\nshort-minus-long: mean={s.mean():.2f}bps median={s.median():.2f} "
      f"positive {npos}/{n} binom_p={p:.4f}")
print(f"correlation with quarterly market return: {tab['short_minus_long'].corr(tab['mkt_ret72_med']):.3f}")

# Split quarters by market direction.
up = tab[tab.mkt_ret72_med > 0]
dn = tab[tab.mkt_ret72_med <= 0]
print(f"\nUP quarters   (n={len(up)}): short-long = {up.short_minus_long.mean():+.2f}bps")
print(f"DOWN quarters (n={len(dn)}): short-long = {dn.short_minus_long.mean():+.2f}bps")
