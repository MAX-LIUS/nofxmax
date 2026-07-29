"""How robust is M5's significance to protocol choices?

My two runs disagreed: +0.1018% CI [+0.0413,+0.1609] (significant, lag 1h,
history-only median, 13 quarters) vs +0.0844% CI [-0.0091,+0.1836] (not
significant, lag 0, full-sample median, 15 quarters). Both are defensible
protocols, which means the honest answer is a distribution over protocols, not
a single number.

Grid: lag {0,1,2}h x threshold {history p50, full p50, history p40, history p60}
      x quarter set {all 15, drop first 2 (threshold warm-up)}
Also: how many of these are significant, and what is the worst quarter.

A read that only becomes significant under a specific corner of this grid is
not deployable.
"""
from __future__ import annotations
import glob
import itertools
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
MULT = 0.5


def block_ci(t, v, n_boot=1500, block_days=7):
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    u = np.unique(blk)
    if len(u) < 3:
        return float(np.mean(v)), np.nan, np.nan
    by = {b: v[blk == b] for b in u}
    s = np.empty(n_boot)
    for i in range(n_boot):
        s[i] = np.concatenate([by[b] for b in RNG.choice(u, len(u), replace=True)]).mean()
    return float(np.mean(v)), *np.percentile(s, [2.5, 97.5])


def load():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        g = d.groupby(d.index)
        acc.append(pd.DataFrame({
            "lean": g["raw_up"].mean(),
            "edge": g["net_long"].mean() - g["net_short"].mean(),
            "nv": g.size(),
            "q": p.split("/")[-1][:-4]}))
        del d, g
    v = pd.concat(acc).sort_index()
    return v[v.nv >= 10]


def main():
    v = load()
    print(f"面板 {len(v)} 小时, {v.q.nunique()} 季\n")
    qs = sorted(v.q.unique())
    res = []
    for lag, thr, drop2 in itertools.product((0, 1, 2),
                                             ("hist_p50", "full_p50", "hist_p40", "hist_p60"),
                                             (False, True)):
        d = v.copy()
        d["pub"] = d.lean.shift(lag)
        d = d.dropna(subset=["pub"])
        if drop2:
            d = d[d.q.isin(qs[2:])]
        if thr == "full_p50":
            d["cut"] = d.pub.median()
        else:
            p = {"hist_p50": 50, "hist_p40": 40, "hist_p60": 60}[thr]
            # expanding percentile of everything strictly before the quarter
            cuts = {}
            for i, q in enumerate(sorted(d.q.unique())):
                hist = d[d.q.isin(sorted(d.q.unique())[:i])].pub
                cuts[q] = np.percentile(hist, p) if len(hist) > 200 else np.nan
            d["cut"] = d.q.map(cuts)
            d = d.dropna(subset=["cut"])
        if len(d) < 500:
            continue
        y = d.edge.to_numpy()
        bear = (d.pub <= d.cut).to_numpy()
        eff = np.where(bear, MULT, 1.0) * y - y
        t = d.index.tz_localize(None).to_numpy("datetime64[ns]")
        m, lo, hi = block_ci(t, eff)
        qq = pd.Series(eff, index=d.index).groupby(d.q.to_numpy()).mean()
        res.append(dict(lag=lag, thr=thr, drop2=drop2, n=len(d), coverage=bear.mean(),
                        m=m, lo=lo, hi=hi, sig=(lo > 0) == (hi > 0),
                        wins=int((qq > 0).sum()), nq=len(qq), worst=qq.min()))
    r = pd.DataFrame(res)
    print(f"{'lag':>3s} {'阈值':10s} {'去前2季':>7s} {'小时':>6s} {'覆盖':>5s} "
          f"{'改善':>9s} {'95%CI':>22s} {'季胜':>6s} {'最差季':>9s} 显著")
    for _, x in r.iterrows():
        print(f"{x.lag:3d} {x.thr:10s} {str(x.drop2):>7s} {x.n:6d} {x.coverage*100:4.0f}% "
              f"{x.m*100:+8.4f}% [{x.lo*100:+.4f},{x.hi*100:+.4f}] "
              f"{x.wins:2d}/{x.nq:<3d} {x.worst*100:+8.4f}% {'是' if x.sig else ''}")
    print(f"\n显著的组合 {int(r.sig.sum())}/{len(r)} = {r.sig.mean()*100:.0f}%")
    print(f"改善均值 {r.m.mean()*100:+.4f}%  范围 [{r.m.min()*100:+.4f},{r.m.max()*100:+.4f}]")
    print(f"季胜率均值 {(r.wins/r.nq).mean()*100:.0f}%   最差季均值 {r.worst.mean()*100:+.4f}%")
    print(f"\n所有组合改善为正的比例: {(r.m>0).mean()*100:.0f}%")


if __name__ == "__main__":
    main()
