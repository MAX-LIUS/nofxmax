"""Replay the live book under the ACTUAL configured geometry, not my stand-in.

I previously benchmarked against "arm 0.60 ATR". The real live config (read from
the production DB, read-only) is a TWO-STAGE break-even plus a two-rule drawdown
take-profit, all in ATR units:

  claude (holds 5): BE1 1.5 ATR -> floor +0.5 | BE2 3.5 -> floor +1.5
                    dd1 profit>=2.5 give 1.5 close100 | dd2 >=4.0 give 1.2 close30
  GPT    (holds 3): BE1 1.5 ATR -> floor +0.3 | BE2 2.5 -> floor +0.7
                    dd1 profit>=2.5 give 1.5 close100 | dd2 >=4.0 give 1.8 close30

Note the floors are ABOVE entry, so these are profit-locks, not break-even. That
makes the live setup MORE tail-preserving than my stand-in, which is exactly the
direction the user's objection pointed. Question: against this real baseline, does
moving BE1 earlier still help, and what does it cost the >3 ATR tail?
"""
import json, numpy as np, pandas as pd
import bars

TP1_ATR, TP1_FRAC = 1.2, 0.30
COST_BPS = 14.0
STOP_ATR = 2.4

# (be1_arm, be1_floor, be2_arm, be2_floor, dd1_profit, dd1_give, label)
CFGS = [
    (1.50, 0.50, 3.50, 1.50, 2.5, 1.5, "LIVE claude (BE1 1.5)"),
    (1.50, 0.30, 2.50, 0.70, 2.5, 1.5, "LIVE GPT (BE1 1.5)"),
    (1.00, 0.30, 2.50, 0.70, 2.5, 1.5, "BE1 -> 1.0"),
    (0.75, 0.20, 2.50, 0.70, 2.5, 1.5, "BE1 -> 0.75"),
    (0.75, 0.00, 2.50, 0.70, 2.5, 1.5, "BE1 -> 0.75 floor=entry"),
    (0.50, 0.00, 2.50, 0.70, 2.5, 1.5, "BE1 -> 0.50 floor=entry"),
    (0.50, -0.20, 2.50, 0.70, 2.5, 1.5, "BE1 -> 0.50 floor -0.20"),
]


def replay(sh, sl, sc, entry, a, side, cfg):
    be1, f1, be2, f2, dd1p, dd1g, _ = cfg
    floorA = -STOP_ATR
    peakA, pos, realA = 0.0, 1.0, 0.0
    stage = 0
    tp1 = False
    cA = 0.0
    for k in range(len(sc)):
        if side == "LONG":
            fA = (sh[k] - entry) / a; aA = (entry - sl[k]) / a; cA = (sc[k] - entry) / a
        else:
            fA = (entry - sl[k]) / a; aA = (sh[k] - entry) / a; cA = (entry - sc[k]) / a
        if -aA <= floorA:                       # adverse-first
            return realA + pos * floorA, peakA
        if (not tp1) and fA >= TP1_ATR:
            realA += TP1_FRAC * TP1_ATR
            pos -= TP1_FRAC; tp1 = True
        peakA = max(peakA, fA)
        if stage < 1 and peakA >= be1:
            stage = 1; floorA = max(floorA, f1)
        if stage < 2 and peakA >= be2:
            stage = 2; floorA = max(floorA, f2)
        # drawdown take-profit: once profit >= dd1p, give back dd1g from the peak
        if peakA >= dd1p:
            floorA = max(floorA, peakA - dd1g)
    return realA + pos * cA, peakA


d = pd.DataFrame(json.load(open("/tmp/rumers/live_all.json")))
d = d[(d.entry > 0) & d.xt.notna() & (d.xt > d.et)].copy()
rows = []
for sym, g in d.groupby("sym"):
    f = bars.load_symbol(sym)
    if f is None or len(f) < 200:
        continue
    ts = f.index.view("int64") // 10**6
    h, l, c = f["high"].to_numpy(), f["low"].to_numpy(), f["close"].to_numpy()
    atr = bars.wilder_atr(h, l, c, 14)
    for _, r in g.iterrows():
        i = int(np.searchsorted(ts, r.et, side="right")) - 1
        j = int(np.searchsorted(ts, r.xt, side="right")) - 1
        if i < 20 or j <= i or j >= len(ts):
            continue
        a = atr[i]
        if not np.isfinite(a) or a <= 0:
            continue
        jj = min(len(ts) - 1, i + max(48, j - i))
        sh, sl2, sc = h[i:jj+1], l[i:jj+1], c[i:jj+1]
        atr_bps = (a / r.entry) * 10000.0
        cost_A = COST_BPS / max(atr_bps, 1e-9)
        rec = dict(sym=sym, side=r.side, live_bps=r.bps)
        for cfg in CFGS:
            p, pk = replay(sh, sl2, sc, r.entry, a, r.side.upper(), cfg)
            rec[cfg[-1]] = p - cost_A
            rec["peak_" + cfg[-1]] = pk
        rows.append(rec)

e = pd.DataFrame(rows)
print(f"live trades replayed = {len(e)}")
labs = [c[-1] for c in CFGS]

print("\n=== live book under each geometry (raw ATR per trade) ===")
out = []
for lab in labs:
    v = e[lab]
    out.append(dict(config=lab, mean_ATR=v.mean(), median=v.median(),
                    win=100*(v > 0).mean(), p95=v.quantile(0.95),
                    p05=v.quantile(0.05), total=v.sum()))
print(pd.DataFrame(out).set_index("config").round(4).to_string())

# tail population: trades whose peak reached >=3 ATR under the live geometry
pk = e["peak_" + labs[1]]
big = e[pk >= 3.0]
print(f"\n=== the >=3 ATR tail on the LIVE book: n={len(big)} ({100*len(big)/len(e):.1f}%) ===")
for lab in labs:
    print(f"  {lab:<26} mean={big[lab].mean():+.4f} ATR  total={big[lab].sum():+8.2f}")

print("\n=== paired vs LIVE GPT baseline, bootstrap over trades ===")
rng = np.random.default_rng(11)
ref = e[labs[1]]
for lab in labs:
    if lab == labs[1]:
        continue
    diff = (e[lab] - ref).to_numpy()
    bs = np.array([rng.choice(diff, len(diff), replace=True).mean() for _ in range(3000)])
    lo, hi = np.percentile(bs, [2.5, 97.5])
    print(f"  {lab:<26} delta={diff.mean():+.4f} ATR 95%CI=[{lo:+.4f},{hi:+.4f}]"
          f" {'SIG' if lo*hi > 0 else 'ns '}")
