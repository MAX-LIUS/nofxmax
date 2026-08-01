"""The only lever that touches the live failure population.

Live book: 48.7% of losers never reach 0.5 ATR favourable, so break-even never
arms and they run to a ~2.4 ATR structural stop at -200bps. Meanwhile armed
trades earn +62bps (215 of them). The arm threshold is the valve between those
two populations -- and it is a SETTING, requiring no forecast.

geometry.py hinted arm=0.35R helps at stop 1.5 but hurts at stop 2.5. That is a
confound: arm expressed in R means a different ATR distance at every stop width.
Here arm is expressed in ATR units so it is comparable across stop widths and
directly comparable to the live 0.56 ATR observation.

Grid: stop {1.5,2.0,2.5,3.0} ATR x arm {0.30,0.45,0.60,0.80,1.00} ATR.
"""
import numpy as np, pandas as pd
import bars, events as EV, evalgate as E

MAX_HOLD = 48
COST_BPS = 14.0
STOPS = [1.5, 2.0, 2.5, 3.0]
ARMS_ATR = [0.30, 0.45, 0.60, 0.80, 1.00]
TP1_ATR = 1.2      # fixed in ATR so it does not move with the stop
TP1_FRAC = 0.30
GIVE_ATR = 1.8


def sim(f, idx, atr, side, stop_atr, arm_atr):
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n - 1)], np.nan)
    a = atr[idx]
    fh = EV._windows(h, ent, MAX_HOLD)
    fl = EV._windows(l, ent, MAX_HOLD)
    fc = EV._windows(c, ent, MAX_HOLD)
    if side == "LONG":
        favA = (fh - entry[:, None]) / a[:, None]
        advA = (entry[:, None] - fl) / a[:, None]
        cloA = (fc - entry[:, None]) / a[:, None]
    else:
        favA = (entry[:, None] - fl) / a[:, None]
        advA = (fh - entry[:, None]) / a[:, None]
        cloA = (entry[:, None] - fc) / a[:, None]

    r_unit_bps = (stop_atr * a / entry) * 10000.0
    cost_R = COST_BPS / np.maximum(r_unit_bps, 1e-9)
    m = len(idx)
    out = np.full(m, np.nan)
    armed_f = np.zeros(m)
    for k in range(m):
        if not ok[k] or not np.isfinite(a[k]) or a[k] <= 0:
            continue
        # everything in ATR units, converted to R only at the end
        floorA = -stop_atr
        peakA = 0.0
        pos, realizedA = 1.0, 0.0
        armed = tp1 = done = False
        j = 0
        for j in range(MAX_HOLD):
            if not np.isfinite(cloA[k, j]):
                break
            fA, aA = favA[k, j], advA[k, j]
            if -aA <= floorA:                      # adverse-first
                realizedA += pos * floorA
                pos = 0.0; done = True; break
            if (not tp1) and fA >= TP1_ATR:
                realizedA += TP1_FRAC * TP1_ATR
                pos -= TP1_FRAC; tp1 = True
            peakA = max(peakA, fA)
            if (not armed) and peakA >= arm_atr:
                armed = True
                floorA = max(floorA, 0.0)          # break-even
            if armed and peakA - GIVE_ATR > floorA:
                floorA = peakA - GIVE_ATR
        if not done and pos > 0:
            realizedA += pos * cloA[k, j]
        armed_f[k] = 1.0 if armed else 0.0
        out[k] = realizedA / stop_atr - cost_R[k]   # -> R units
    return out, armed_f


def run():
    syms = EV.all_symbols()
    agg = {(s, aa): {} for s in STOPS for aa in ARMS_ATR}
    for i, sym in enumerate(syms):
        f = bars.load_symbol(sym)
        if f is None or len(f) < 400:
            continue
        feats = EV.build_features(f)
        atr = feats.pop("_atr")
        n = len(f)
        idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE * 4)
        if len(idx) == 0:
            continue
        atrp = feats["atr_pct"][idx]
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        q = pd.PeriodIndex(pd.to_datetime(f.index.to_numpy()[idx], utc=True),
                           freq="Q").astype(str).to_numpy()
        for st in STOPS:
            for aa in ARMS_ATR:
                A = agg[(st, aa)]
                for side in ("LONG", "SHORT"):
                    v, am = sim(f, idx, atr, side, st, aa)
                    mk = keep & np.isfinite(v)
                    if not mk.any():
                        continue
                    for qq in np.unique(q[mk]):
                        sel = mk & (q == qq)
                        if not sel.any():
                            continue
                        rr = A.setdefault(qq, np.zeros(4))
                        rr[0] += v[sel].sum(); rr[1] += sel.sum()
                        rr[2] += am[sel].sum(); rr[3] += (v[sel] > 0).sum()
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    per_q = {}
    rows = []
    for key, A in agg.items():
        qs = sorted(A)
        d = pd.DataFrame([A[q] for q in qs], index=qs,
                         columns=["sumR", "n", "sumArm", "sumWin"])
        d = d[d.n > 0]
        per_q[key] = d.sumR / d.n
        rows.append(dict(stop=key[0], arm_atr=key[1],
                         qmeanR=(d.sumR / d.n).mean(),
                         arm_pct=100 * d.sumArm.sum() / d.n.sum(),
                         win=100 * d.sumWin.sum() / d.n.sum(),
                         n=int(d.n.sum())))
    t = pd.DataFrame(rows)
    print("\n=== qmeanR: rows=stop(ATR), cols=arm threshold(ATR) ===")
    print(t.pivot(index="stop", columns="arm_atr", values="qmeanR").round(4).to_string())
    print("\n=== arm rate % ===")
    print(t.pivot(index="stop", columns="arm_atr", values="arm_pct").round(1).to_string())
    print("\n=== win rate % ===")
    print(t.pivot(index="stop", columns="arm_atr", values="win").round(1).to_string())

    print("\n=== within each stop, arm 0.30 vs 0.60 ATR (paired quarters) ===")
    for st in STOPS:
        d = (per_q[(st, 0.30)] - per_q[(st, 0.60)]).dropna()
        fr, p, _, nq = E.sign_test(d)
        print(f"  stop {st}: delta(0.30-0.60)={d.mean():+.4f}R fracpos={fr:.2f}"
              f" p={p:.4f} nq={nq}")

    print("\n=== best cell vs live-like (stop 2.5, arm 0.60) ===")
    ref = per_q[(2.5, 0.60)]
    best = t.sort_values("qmeanR", ascending=False).iloc[0]
    bk = (best["stop"], best["arm_atr"])
    d = (per_q[bk] - ref).dropna()
    fr, p, _, nq = E.sign_test(d)
    print(f"  best={bk} qmeanR={best.qmeanR:+.4f} vs ref={ref.mean():+.4f}")
    print(f"  delta={d.mean():+.4f}R fracpos={fr:.2f} p={p:.4f} worst={d.min():+.4f}")


if __name__ == "__main__":
    run()
