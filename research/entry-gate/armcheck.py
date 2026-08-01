"""Is the arm/stop grid result real, or a denominator artifact?

armgrid.py reports R = realized_ATR / stop_atr. But arming break-even at 0.30 ATR
means the EFFECTIVE risk is ~0.30 ATR regardless of the nominal stop. Dividing a
small realized loss by a large nominal stop inflates R mechanically.

Three views of the same simulations:
  A. realized P&L in RAW ATR units   -- no denominator, no inflation possible
  B. realized P&L per unit of ACTUAL risk taken (mean adverse excursion realized)
  C. the nominal-R view armgrid.py used

If A is flat while C rises, the effect is a denominator artifact and must be
discarded like the ADX/ATR bps effect was.
"""
import numpy as np, pandas as pd
import bars, events as EV, evalgate as E
import armgrid as AG

STOPS = [1.5, 2.0, 2.5, 3.0]
ARMS = [0.30, 0.60, 1.00]


def sim_atr(f, idx, atr, side, stop_atr, arm_atr):
    """Returns realized P&L in RAW ATR units and the realized adverse excursion."""
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n - 1)], np.nan)
    a = atr[idx]
    fh = EV._windows(h, ent, AG.MAX_HOLD)
    fl = EV._windows(l, ent, AG.MAX_HOLD)
    fc = EV._windows(c, ent, AG.MAX_HOLD)
    if side == "LONG":
        favA = (fh - entry[:, None]) / a[:, None]
        advA = (entry[:, None] - fl) / a[:, None]
        cloA = (fc - entry[:, None]) / a[:, None]
    else:
        favA = (entry[:, None] - fl) / a[:, None]
        advA = (fh - entry[:, None]) / a[:, None]
        cloA = (entry[:, None] - fc) / a[:, None]

    # cost expressed in ATR units: bps / (atr/entry in bps)
    atr_bps = (a / entry) * 10000.0
    cost_A = AG.COST_BPS / np.maximum(atr_bps, 1e-9)
    m = len(idx)
    pnlA = np.full(m, np.nan)      # raw ATR P&L, net of cost
    riskA = np.full(m, np.nan)     # risk actually exposed (|floor| at worst)
    for k in range(m):
        if not ok[k] or not np.isfinite(a[k]) or a[k] <= 0:
            continue
        floorA = -stop_atr
        peakA = 0.0
        pos, realA = 1.0, 0.0
        armed = tp1 = done = False
        worst_floor = floorA
        j = 0
        for j in range(AG.MAX_HOLD):
            if not np.isfinite(cloA[k, j]):
                break
            fA, aA = favA[k, j], advA[k, j]
            if -aA <= floorA:
                realA += pos * floorA
                pos = 0.0; done = True; break
            if (not tp1) and fA >= AG.TP1_ATR:
                realA += AG.TP1_FRAC * AG.TP1_ATR
                pos -= AG.TP1_FRAC; tp1 = True
            peakA = max(peakA, fA)
            if (not armed) and peakA >= arm_atr:
                armed = True; floorA = max(floorA, 0.0)
            if armed and peakA - AG.GIVE_ATR > floorA:
                floorA = peakA - AG.GIVE_ATR
        if not done and pos > 0:
            realA += pos * cloA[k, j]
        pnlA[k] = realA - cost_A[k]
        # risk actually exposed = distance from entry to the binding floor while open
        riskA[k] = -worst_floor if not armed else min(stop_atr, arm_atr + 1e-9)
    return pnlA, riskA


def run():
    syms = EV.all_symbols()
    agg = {(s, aa): {} for s in STOPS for aa in ARMS}
    for i, sym in enumerate(syms):
        f = bars.load_symbol(sym)
        if f is None or len(f) < 400:
            continue
        feats = EV.build_features(f)
        atr = feats.pop("_atr")
        n = len(f)
        idx = np.arange(200, n - AG.MAX_HOLD - 2, EV.STRIDE * 4)
        if len(idx) == 0:
            continue
        atrp = feats["atr_pct"][idx]
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        q = pd.PeriodIndex(pd.to_datetime(f.index.to_numpy()[idx], utc=True),
                           freq="Q").astype(str).to_numpy()
        for st in STOPS:
            for aa in ARMS:
                A = agg[(st, aa)]
                for side in ("LONG", "SHORT"):
                    p, rk = sim_atr(f, idx, atr, side, st, aa)
                    mk = keep & np.isfinite(p)
                    if not mk.any():
                        continue
                    for qq in np.unique(q[mk]):
                        sel = mk & (q == qq)
                        if not sel.any():
                            continue
                        r = A.setdefault(qq, np.zeros(3))
                        r[0] += p[sel].sum(); r[1] += sel.sum()
                        r[2] += rk[sel].sum()
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    per_q_atr, per_q_norm, rows = {}, {}, []
    for key, A in agg.items():
        qs = sorted(A)
        d = pd.DataFrame([A[q] for q in qs], index=qs,
                         columns=["sumP", "n", "sumRisk"])
        d = d[d.n > 0]
        pnl_atr = d.sumP / d.n                     # raw ATR per trade
        mean_risk = d.sumRisk / d.n                # ATR of risk actually exposed
        per_q_atr[key] = pnl_atr
        per_q_norm[key] = pnl_atr / mean_risk      # per unit of REAL risk
        rows.append(dict(stop=key[0], arm=key[1],
                         pnl_ATR=pnl_atr.mean(),
                         real_risk_ATR=mean_risk.mean(),
                         per_real_risk=(pnl_atr / mean_risk).mean(),
                         nominal_R=(pnl_atr / key[0]).mean()))
    t = pd.DataFrame(rows)
    print("\n=== A. raw ATR P&L per trade (no denominator) ===")
    print(t.pivot(index="stop", columns="arm", values="pnl_ATR").round(4).to_string())
    print("\n=== B. P&L per unit of risk ACTUALLY exposed ===")
    print(t.pivot(index="stop", columns="arm", values="per_real_risk").round(4).to_string())
    print("\n=== C. nominal-R view (what armgrid.py reported) ===")
    print(t.pivot(index="stop", columns="arm", values="nominal_R").round(4).to_string())
    print("\n=== risk actually exposed (ATR) ===")
    print(t.pivot(index="stop", columns="arm", values="real_risk_ATR").round(3).to_string())

    print("\n=== verdict: arm 0.30 vs 0.60, in RAW ATR (paired quarters) ===")
    for st in STOPS:
        d = (per_q_atr[(st, 0.30)] - per_q_atr[(st, 0.60)]).dropna()
        fr, p, _, nq = E.sign_test(d)
        print(f"  stop {st}: delta_ATR={d.mean():+.4f} fracpos={fr:.2f} p={p:.4f}")
    print("\n=== verdict: stop 3.0 vs 1.5 at arm 0.30, in RAW ATR ===")
    d = (per_q_atr[(3.0, 0.30)] - per_q_atr[(1.5, 0.30)]).dropna()
    fr, p, _, nq = E.sign_test(d)
    print(f"  delta_ATR={d.mean():+.4f} fracpos={fr:.2f} p={p:.4f}")


if __name__ == "__main__":
    run()
