"""First pass: does any single feature carry DIRECTION information?

Sign convention is fixed per feature by its economic meaning, decided before
running: a mean-reversion feature says "high -> prefer SHORT", a trend feature
says "high -> prefer LONG". We test the stated direction only. Testing both and
keeping the winner would be fitting the sign to the data.
"""
import numpy as np, pandas as pd
import evalgate as E

ev = E.load()
print(f"events={len(ev)} quarters={ev.q.nunique()} symbols={ev.sym.nunique()}")
print(f"baseline: long={ev.long_bps.mean():.2f}bps short={ev.short_bps.mean():.2f}bps "
      f"coin={(0.5*(ev.long_bps+ev.short_bps)).mean():.2f}bps")

# (feature, sign, threshold) — sign=+1 means "high value -> prefer LONG".
# Thresholds are the natural neutral point of each feature, not fitted.
CANDS = [
    # Mean reversion on location: high in range -> prefer SHORT.
    ("rpos30",        -1, 0.5),
    ("rpos72",        -1, 0.5),
    # Trend following on slope: positive slope -> prefer LONG.
    ("slope24",       +1, 0.0),
    ("slope72",       +1, 0.0),
    ("slope168",      +1, 0.0),
    # Momentum: recent return.
    ("ret4",          +1, 0.0),
    ("ret24",         +1, 0.0),
    ("ret72",         +1, 0.0),
    ("ret24_atr",     +1, 0.0),
    # Mean reversion on stretch: far above EMA -> prefer SHORT.
    ("ema20_dev",     -1, 0.0),
    # Candle shape: long lower wick = rejection of lows -> prefer LONG.
    ("lower_wick_frac", +1, 0.25),
    ("upper_wick_frac", -1, 0.25),
    ("close_pos_in_bar", +1, 0.5),
    # Flow: taker buy imbalance -> prefer LONG.
    ("taker_imb",     +1, 0.0),
    # Edge distance asymmetry: closer to high than low -> prefer SHORT.
    ("dist_hi30_atr", +1, 0.0),
]

rows = []
for name, sign, thr in CANDS:
    x = ev[name].to_numpy().astype(float)
    if name == "dist_hi30_atr":
        # Asymmetry of distances, centred at zero.
        x = ev["dist_hi30_atr"].to_numpy() - ev["dist_lo30_atr"].to_numpy()
        thr = 0.0
    score = sign * (x - thr)
    r = E.direction_eval(ev, score, 0.0)
    r["feature"] = f"{name}({'+' if sign>0 else '-'})"
    rows.append(r)

df = pd.DataFrame(rows).set_index("feature")
cols = ["qmean","qmedian","frac_pos","binom_p","worst_q","pooled","vs_long_qmean","vs_long_frac_pos"]
print("\n=== DIRECTION SKILL vs coin-flip baseline (bps/trade, quarter-equal-weighted) ===")
print(df[cols].sort_values("qmean", ascending=False).round(4).to_string())
