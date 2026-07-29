"""The decisive question: does the 86-feature model add anything OVER a one-line
naive market-breadth read?

In bps terms the model's LONG separation (+20.3) is no better than "share of
coins down over 24h" (+23.4). So test incrementality directly:
  1. On the full 15-quarter walk-forward panel (5205 hours = real power):
     double-sort model lean x naive breadth, and check whether the model's
     advantage survives inside each naive bucket.
  2. Logistic/OLS: regress the DOWN advantage on both reads together.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)


def bootstrap_mean(t, v, n_boot=2000, block_days=7):
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.mean(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    s = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        s[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(s, [2.5, 97.5]))


def build_hourly():
    """Per-hour: model lean, naive breadth, and the realized long-vs-short edge."""
    rows = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        if d.empty:
            continue
        need = ["raw_up", "net_long", "net_short", "slope168"]
        nv = "ret_24" in d.columns
        cols = need + (["ret_24"] if nv else [])
        d = d[cols + ["symbol"]] if "symbol" in d.columns else d[cols]
        g = d.groupby(d.index)
        h = pd.DataFrame({
            "lean": g["raw_up"].mean(),
            "our_share": (d.slope168 < 0).groupby(d.index).mean(),
            # realized market edge: mean(long) - mean(short) net return that hour
            "edge": g["net_long"].mean() - g["net_short"].mean(),
            "mL": g["net_long"].mean(),
            "mS": g["net_short"].mean(),
            "n": g.size()})
        if nv:
            h["dn24"] = (d.ret_24 < 0).groupby(d.index).mean()
            h["m_ret24"] = g["ret_24"].mean()
        rows.append(h[h.n >= 10])
        del d, g, h
    return pd.concat(rows).sort_index()


def main():
    h = build_hourly()
    print(f"面板小时数 {len(h)}  {h.index.min()} ~ {h.index.max()}")
    have_nv = "dn24" in h.columns
    if not have_nv:
        # rebuild naive breadth from the feature cache
        acc = []
        for p in sorted(glob.glob(f"{BASE}/wf_cache/*.pkl")):
            d = pd.read_pickle(p)
            d.index = pd.to_datetime(d.index, utc=True)
            if "ret_24" not in d.columns:
                continue
            acc.append(d[["ret_24"]])
            del d
        a = pd.concat(acc)
        del acc
        nv = pd.DataFrame({"dn24": (a.ret_24 < 0).groupby(a.index).mean(),
                           "m_ret24": a.groupby(a.index)["ret_24"].mean(),
                           "nn": a.groupby(a.index).size()})
        del a
        nv = nv[nv.nn >= 10]
        h = h.join(nv[["dn24", "m_ret24"]], how="inner")
        print(f"并入朴素读数后 {len(h)} 小时")

    h = h.dropna(subset=["lean", "dn24", "edge"])
    t = h.index.tz_localize(None).to_numpy("datetime64[ns]")
    print(f"corr(lean, dn24) = {h.lean.corr(h.dn24):+.3f}\n")

    lc, dc = h.lean.median(), h.dn24.median()
    print("=== 1. 单独看: 看空读数下的已实现多空边际 edge = 均net_long - 均net_short ===")
    print("    (edge<0 表示该小时做空更好)")
    for lbl, bear in (("模型 lean 低", h.lean <= lc), ("朴素 下跌币占比 高", h.dn24 >= dc)):
        b = bear.to_numpy()
        m1, l1, u1 = bootstrap_mean(t[b], h.edge.to_numpy()[b])
        m0, l0, u0 = bootstrap_mean(t[~b], h.edge.to_numpy()[~b])
        print(f"  {lbl:18s}: 看空组 edge {m1*100:+.4f}% [{l1*100:+.4f},{u1*100:+.4f}] | "
              f"看多组 edge {m0*100:+.4f}% [{l0*100:+.4f},{u0*100:+.4f}] | "
              f"差 {(m0-m1)*100:+.4f}%")

    print("\n=== 2. 双重排序: 朴素读数固定后, 模型 lean 还有增量吗 ===")
    h["nv_b"] = pd.qcut(h.dn24, 3, labels=["朴素看多", "朴素中性", "朴素看空"])
    h["md_b"] = pd.qcut(h.lean, 3, labels=["模型看空", "模型中性", "模型看多"])
    piv = h.pivot_table(index="nv_b", columns="md_b", values="edge",
                        aggfunc="mean", observed=True) * 100
    cnt = h.pivot_table(index="nv_b", columns="md_b", values="edge",
                        aggfunc="size", observed=True)
    print("  edge 均值 (%):")
    print(piv.round(4).to_string())
    print("  样本数:")
    print(cnt.to_string())
    # within-naive-bucket model spread
    print("\n  每个朴素桶内 (模型看多 - 模型看空) 的 edge 差:")
    for b in piv.index:
        if "模型看多" in piv.columns and "模型看空" in piv.columns:
            print(f"    {b}: {piv.loc[b,'模型看多'] - piv.loc[b,'模型看空']:+.4f}%")

    print("\n=== 3. 联合回归: edge ~ lean + dn24 (标准化), 谁在解释 ===")
    X = np.column_stack([
        (h.lean - h.lean.mean()) / h.lean.std(),
        (h.dn24 - h.dn24.mean()) / h.dn24.std(),
        np.ones(len(h))])
    y = h.edge.to_numpy()
    beta, *_ = np.linalg.lstsq(X, y, rcond=None)
    resid = y - X @ beta
    # Newey-West-ish: block bootstrap the coefficients
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // 7)
    uniq = np.unique(blk)
    idx_by = {b: np.where(blk == b)[0] for b in uniq}
    bs = np.empty((1000, 3))
    for i in range(1000):
        pick = np.concatenate([idx_by[b] for b in RNG.choice(uniq, len(uniq), replace=True)])
        bb, *_ = np.linalg.lstsq(X[pick], y[pick], rcond=None)
        bs[i] = bb
    names = ["lean(标准化)", "dn24(标准化)", "常数"]
    for i, nm in enumerate(names):
        lo, hi = np.percentile(bs[:, i], [2.5, 97.5])
        star = "显著" if (lo > 0) == (hi > 0) else ""
        print(f"  {nm:16s} 系数 {beta[i]*100:+.5f}% CI [{lo*100:+.5f},{hi*100:+.5f}] {star}")
    print(f"  R2 = {1 - resid.var()/y.var():.5f}")

    print("\n=== 4. 单读数回归对照 ===")
    for nm, col in (("仅 lean", "lean"), ("仅 dn24", "dn24")):
        Xi = np.column_stack([(h[col] - h[col].mean()) / h[col].std(), np.ones(len(h))])
        bi, *_ = np.linalg.lstsq(Xi, y, rcond=None)
        r = y - Xi @ bi
        print(f"  {nm:10s}: 系数 {bi[0]*100:+.5f}%  R2 = {1 - r.var()/y.var():.5f}")


if __name__ == "__main__":
    main()
