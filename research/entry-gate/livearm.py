"""The one test that matters, on the population that matters.

armcheck.py showed earlier break-even arming loses less in raw ATR (p=0.0000,
19-20/20 quarters) but is WORSE per unit of risk actually exposed. Both are true:
it wins by taking less risk, not by choosing better.

That distinction is decisive, because the research event table is RANDOM ENTRY with
NEGATIVE expectancy (qmeanR ~ -0.05), where any exposure cut looks like a gain. The
live book has POSITIVE expectancy (62.1% win, median +37.5bps), where cutting
exposure cuts profit. An exposure-reduction "finding" can invert sign between them.

So: replay the ACTUAL live trades bar by bar from klines and ask what different arm
thresholds would have done. n=550 cannot confirm a subtle effect, but it can show
whether the sign inverts -- which is the question.
"""
import json, numpy as np, pandas as pd
from scipy import stats
import bars

ARMS_ATR = [0.20, 0.30, 0.45, 0.60, 0.80, 1.00, 99.0]   # 99 = never arm
GIVE_ATR = 1.8
TP1_ATR, TP1_FRAC = 1.2, 0.30

d = pd.DataFrame(json.load(open("/tmp/rumers/live_all.json")))
d = d[(d.entry > 0) & d.xt.notna() & (d.xt > d.et) & d.bps.notna()].copy()

def replay(seg_h, seg_l, seg_c, entry, a, side, stop_atr, arm_atr):
    """Replay one live trade under a given (stop, arm) geometry. Returns ATR P&L."""
    floorA = -stop_atr
    peakA, pos, realA = 0.0, 1.0, 0.0
    armed = tp1 = False
    for k in range(len(seg_c)):
        if side == "LONG":
            fA = (seg_h[k] - entry) / a
            aA = (entry - seg_l[k]) / a
            cA = (seg_c[k] - entry) / a
        else:
            fA = (entry - seg_l[k]) / a
            aA = (seg_h[k] - entry) / a
            cA = (entry - seg_c[k]) / a
        if -aA <= floorA:                      # adverse-first
            return realA + pos * floorA
        if (not tp1) and fA >= TP1_ATR:
            realA += TP1_FRAC * TP1_ATR
            pos -= TP1_FRAC; tp1 = True
        peakA = max(peakA, fA)
        if (not armed) and peakA >= arm_atr:
            armed = True; floorA = max(floorA, 0.0)
        if armed and peakA - GIVE_ATR > floorA:
            floorA = peakA - GIVE_ATR
    return realA + pos * cA

rows = []
for sym, g in d.groupby("sym"):
    f = bars.load_symbol(sym)
    if f is None or len(f) < 200:
        continue
    ts = f.index.view("int64") // 10**6
    h = f["high"].to_numpy(); l = f["low"].to_numpy(); c = f["close"].to_numpy()
    atr = bars.wilder_atr(h, l, c, 14)
    for _, r in g.iterrows():
        i = int(np.searchsorted(ts, r.et, side="right")) - 1
        j = int(np.searchsorted(ts, r.xt, side="right")) - 1
        if i < 20 or j <= i or j >= len(ts):
            continue
        a = atr[i]
        if not np.isfinite(a) or a <= 0:
            continue
        # extend the window so a WIDER geometry can play out past the real exit
        jj = min(len(ts) - 1, i + max(48, j - i))
        sh, sl, sc = h[i:jj+1], l[i:jj+1], c[i:jj+1]
        rec = dict(sym=sym, side=r.side, reason=r.reason, live_bps=r.bps,
                   atr_pct=100.0 * a / r.entry)
        for aa in ARMS_ATR:
            rec[f"arm{aa}"] = replay(sh, sl, sc, r.entry, a, r.side.upper(), 2.4, aa)
        rows.append(rec)

e = pd.DataFrame(rows)
print(f"live trades replayed = {len(e)}")
print(f"live actual: mean={e.live_bps.mean():+.1f}bps win={100*(e.live_bps>0).mean():.1f}%")

print("\n=== replay under stop 2.4 ATR, varying arm threshold ===")
print("    (P&L in raw ATR units, same population, same entries)")
out = []
for aa in ARMS_ATR:
    v = e[f"arm{aa}"]
    lab = "never" if aa == 99.0 else f"{aa:.2f}"
    out.append(dict(arm_atr=lab, mean_ATR=v.mean(), median_ATR=v.median(),
                    win=100*(v > 0).mean(), p10=v.quantile(0.10),
                    p90=v.quantile(0.90)))
print(pd.DataFrame(out).set_index("arm_atr").round(4).to_string())

print("\n=== paired vs live-like arm 0.60, bootstrapped over trades ===")
ref = e["arm0.6"]
rng = np.random.default_rng(7)
for aa in ARMS_ATR:
    if aa == 0.60:
        continue
    diff = (e[f"arm{aa}"] - ref).to_numpy()
    bs = np.array([rng.choice(diff, len(diff), replace=True).mean()
                   for _ in range(2000)])
    lab = "never" if aa == 99.0 else f"{aa:.2f}"
    lo, hi = np.percentile(bs, [2.5, 97.5])
    print(f"  arm {lab:>5}: delta={diff.mean():+.4f} ATR  95%CI=[{lo:+.4f},{hi:+.4f}]"
          f"  {'SIGNIFICANT' if lo*hi > 0 else 'not sig'}")

print("\n=== does the sign match the research population? ===")
print("  research (random entry, negative expectancy): arm 0.30 BEAT arm 0.60")
dd = (e["arm0.3"] - e["arm0.6"]).mean()
print(f"  live (positive expectancy): arm 0.30 - arm 0.60 = {dd:+.4f} ATR")
print(f"  -> {'SAME sign, effect survives' if dd > 0 else 'SIGN INVERTS on the live book'}")

print("\n=== by live outcome group ===")
e["grp"] = np.where(e.live_bps > 0, "winner", "loser")
print(e.groupby("grp")[[f"arm{a}" for a in (0.30,0.60,1.00,99.0)]].mean().round(4).to_string())
