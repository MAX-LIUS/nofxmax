"""The user's objection, measured: does an earlier break-even arm SCRATCH trades
that would have gone deeply adverse and then recovered into big winners?

Mechanism note first, because it bounds the risk: break-even arms on a FAVOURABLE
excursion. A trade that goes straight against us never arms, so the arm threshold
cannot touch it -- it is the stop's business, not break-even's. The population
genuinely at risk is narrower and specific:

    price goes favourable past the arm threshold -> wiggles BACK through entry
    -> gets scratched at break-even -> and only THEN runs to a big profit.

That is the "premature scratch" set. This measures its size and its cost, and
prices two mitigations:
    - slack: arm the floor slightly BELOW entry (-0.15 ATR) so noise cannot scratch
    - a middle arm threshold (0.45 ATR)

Reported as the full distribution, not just the mean, because the objection is
precisely about the right tail.
"""
import numpy as np, pandas as pd
import bars, events as EV, evalgate as E

MAX_HOLD = 48
COST_BPS = 14.0
STOP_ATR = 2.4
TP1_ATR, TP1_FRAC = 1.2, 0.30
GIVE_ATR = 1.8

# (arm_atr, floor_offset_atr, label)
CFGS = [
    (0.60,  0.00, "live-like arm0.60"),
    (0.30,  0.00, "arm0.30"),
    (0.30, -0.15, "arm0.30 slack-0.15"),
    (0.45, -0.10, "arm0.45 slack-0.10"),
]


def sim(f, idx, atr, side, arm_atr, floor_off):
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

    atr_bps = (a / entry) * 10000.0
    cost_A = COST_BPS / np.maximum(atr_bps, 1e-9)
    m = len(idx)
    pnl = np.full(m, np.nan)
    scratched = np.zeros(m)      # exited at/near the break-even floor
    max_fav = np.full(m, np.nan) # best favourable the trade EVER reached
    max_adv = np.full(m, np.nan)
    for k in range(m):
        if not ok[k] or not np.isfinite(a[k]) or a[k] <= 0:
            continue
        floorA = -STOP_ATR
        peakA, pos, realA = 0.0, 1.0, 0.0
        armed = tp1 = done = False
        j = 0
        rf, ra = 0.0, 0.0
        for j in range(MAX_HOLD):
            if not np.isfinite(cloA[k, j]):
                break
            fA, aA = favA[k, j], advA[k, j]
            rf = max(rf, fA); ra = max(ra, aA)
            if -aA <= floorA:
                realA += pos * floorA
                if armed:
                    scratched[k] = 1.0
                pos = 0.0; done = True; break
            if (not tp1) and fA >= TP1_ATR:
                realA += TP1_FRAC * TP1_ATR
                pos -= TP1_FRAC; tp1 = True
            peakA = max(peakA, fA)
            if (not armed) and peakA >= arm_atr:
                armed = True
                floorA = max(floorA, floor_off)
            if armed and peakA - GIVE_ATR > floorA:
                floorA = peakA - GIVE_ATR
        if not done and pos > 0:
            realA += pos * cloA[k, j]
        # full remaining favourable potential over the whole window
        wf = favA[k]; wa = advA[k]
        max_fav[k] = np.nanmax(wf) if np.isfinite(wf).any() else np.nan
        max_adv[k] = np.nanmax(wa) if np.isfinite(wa).any() else np.nan
        pnl[k] = realA - cost_A[k]
    return pnl, scratched, max_fav, max_adv


def run():
    syms = EV.all_symbols()
    acc = []
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
        for side in ("LONG", "SHORT"):
            rec = {"q": q, "side": side}
            for arm, off, lab in CFGS:
                p, sc, mf, ma = sim(f, idx, atr, side, arm, off)
                rec[lab] = p
                rec[lab + "|scr"] = sc
                if lab == "arm0.30":
                    rec["max_fav"] = mf
                    rec["max_adv"] = ma
            d = pd.DataFrame(rec)[keep]
            acc.append(d.dropna(subset=[CFGS[0][2]]))
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    e = pd.concat(acc, ignore_index=True)
    print(f"\nevents={len(e)} quarters={e.q.nunique()}")
    base, cand = "live-like arm0.60", "arm0.30"

    print("\n=== THE OBJECTION: trades that went DEEP adverse then recovered big ===")
    print("    (deep adverse = max_adv >= 1.0 ATR; big recovery = max_fav >= 1.5 ATR)")
    deep = e[(e.max_adv >= 1.0) & (e.max_fav >= 1.5)]
    print(f"  population: n={len(deep)} = {100*len(deep)/len(e):.1f}% of all events")
    for arm, off, lab in CFGS:
        v = deep[lab]
        print(f"    {lab:<22} mean={v.mean():+.4f} ATR  median={v.median():+.4f}"
              f"  scratched={100*deep[lab+'|scr'].mean():5.1f}%")
    d = (deep.groupby("q")[cand].mean() - deep.groupby("q")[base].mean()).dropna()
    fr, p, _, nq = E.sign_test(d)
    print(f"  arm0.30 - arm0.60 on THIS population: {d.mean():+.4f} ATR"
          f"  fracpos={fr:.2f} p={p:.4f} nq={nq}")

    print("\n=== cost broken out by how big the trade could have become ===")
    e["pot"] = pd.cut(e.max_fav, [-9, 0.5, 1.0, 2.0, 3.0, 99],
                      labels=["<0.5", "0.5-1", "1-2", "2-3", ">3"])
    rows = []
    for b, s in e.groupby("pot", observed=True):
        r = dict(potential=b, n=len(s), pct=100*len(s)/len(e))
        for arm, off, lab in CFGS:
            r[lab] = s[lab].mean()
        r["delta_0.30_vs_0.60"] = s[cand].mean() - s[base].mean()
        rows.append(r)
    print(pd.DataFrame(rows).set_index("potential").round(4).to_string())

    print("\n=== how many BIG winners get scratched? ===")
    big = e[e.max_fav >= 2.0]
    print(f"  trades with >=2.0 ATR potential: n={len(big)} ({100*len(big)/len(e):.1f}%)")
    for arm, off, lab in CFGS:
        print(f"    {lab:<22} scratched={100*big[lab+'|scr'].mean():5.1f}%"
              f"  mean={big[lab].mean():+.4f} ATR")

    print("\n=== overall, all four configs (quarter-equal-weighted) ===")
    rows = []
    perq = {}
    for arm, off, lab in CFGS:
        pq = e.groupby("q")[lab].mean()
        perq[lab] = pq
        rows.append(dict(config=lab, qmean_ATR=pq.mean(), q_sd=pq.std(),
                         worst_q=pq.min(), win=100*(e[lab] > 0).mean(),
                         scratch=100*e[lab+"|scr"].mean(),
                         p95=e[lab].quantile(0.95)))
    print(pd.DataFrame(rows).set_index("config").round(4).to_string())
    print("\n=== each vs live-like, paired quarters ===")
    for arm, off, lab in CFGS:
        if lab == base:
            continue
        d = (perq[lab] - perq[base]).dropna()
        fr, p, _, nq = E.sign_test(d)
        print(f"  {lab:<22} delta={d.mean():+.4f} fracpos={fr:.2f} p={p:.4f}"
              f" worst={d.min():+.4f}")
    e.to_parquet("cache/scratchcost.parquet", index=False)


if __name__ == "__main__":
    run()
