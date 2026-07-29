"""Head-to-head on IDENTICAL hours: vendor lean vs local market-level score vs
naive breadth. The previous two runs used different hour sets (vendor = thinned
panel 5205h / local = full hourly 31273h), so they were not comparable.

Everything below is restricted to the intersection, same threshold rule, same
M5 damping, same 7-day block bootstrap.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)


def block_ci(t, v, n_boot=2000, block_days=7):
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


def vendor_lean():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        g = d.groupby(d.index)
        acc.append(pd.DataFrame({
            "lean": g["raw_up"].mean(),
            "edge_v": g["net_long"].mean() - g["net_short"].mean(),
            "nv": g.size()}))
        del d, g
    v = pd.concat(acc).sort_index()
    return v[v.nv >= 10]


def main():
    v = vendor_lean()
    lo = pd.read_pickle(f"{BASE}/local_mkt_oos.pkl")
    j = lo.join(v[["lean", "edge_v"]], how="inner").dropna(subset=["lean", "score", "edge"])
    print(f"厂商 {len(v)}h / 本地 {len(lo)}h / 交集 {len(j)}h "
          f"{j.index.min().date()}~{j.index.max().date()}")
    print(f"两侧 edge 一致性 corr = {j.edge.corr(j.edge_v):+.4f} "
          f"(应接近 1, 不然口径不同)\n")

    t = j.index.tz_localize(None).to_numpy("datetime64[ns]")
    y = j.edge.to_numpy()

    print("=== 相关性 (同一批小时) ===")
    for nm, c in (("厂商 lean", j.lean), ("本地分数", j.score), ("朴素 dn24", j.dn24),
                  ("我方 slope 占比", j.our_share)):
        print(f"  corr({nm:14s}, edge) = {c.corr(j.edge):+.4f}")

    print("\n=== 三方联合回归 edge ~ lean + 本地分数 + dn24 ===")
    cols = [("lean", j.lean), ("score", j.score), ("dn24", j.dn24)]
    Z = np.column_stack([((c - c.mean()) / c.std()).to_numpy() for _, c in cols]
                        + [np.ones(len(j))])
    beta, *_ = np.linalg.lstsq(Z, y, rcond=None)
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // 7)
    u = np.unique(blk)
    ib = {b: np.where(blk == b)[0] for b in u}
    bs = np.empty((1000, Z.shape[1]))
    for i in range(1000):
        pick = np.concatenate([ib[b] for b in RNG.choice(u, len(u), replace=True)])
        bs[i], *_ = np.linalg.lstsq(Z[pick], y[pick], rcond=None)
    names = ["厂商 lean", "本地分数", "朴素 dn24", "常数"]
    for i, nm in enumerate(names):
        l, h = np.percentile(bs[:, i], [2.5, 97.5])
        print(f"  {nm:12s} {beta[i]*100:+.5f}% CI [{l*100:+.5f},{h*100:+.5f}] "
              f"{'显著' if (l>0)==(h>0) else ''}")

    print("\n=== M5 同口径同小时: 各读数看空时 LONG 减半 ===")
    print(f"{'读数':16s} {'季胜':>6s} {'覆盖':>6s} {'小时级改善':>12s} {'95%CI':>26s} {'最差季':>9s}")
    for nm, c in (("厂商 lean", j.lean), ("本地分数", j.score),
                  ("朴素 dn24", -j.dn24), ("我方 slope 占比", -j.our_share)):
        cut = c.median()
        bear = (c <= cut).to_numpy()
        eff = np.where(bear, 0.5, 1.0) * y - y
        m, l, h = block_ci(t, eff)
        qq = pd.Series(eff, index=j.index).groupby(j.q.to_numpy()).mean()
        sig = "显著" if (l > 0) == (h > 0) else ""
        print(f"  {nm:14s} {int((qq>0).sum()):2d}/{len(qq):<3d} {bear.mean()*100:5.0f}% "
              f"{m*100:+11.4f}% [{l*100:+.4f},{h*100:+.4f}] {qq.min()*100:+8.4f}% {sig}")

    print("\n注: dn24 与 slope 占比取负号, 因为它们是'看空强度'(越大越空),"
          "\n    统一成'读数越低越看空'才能用同一条 <=中位数 规则。")


if __name__ == "__main__":
    main()
