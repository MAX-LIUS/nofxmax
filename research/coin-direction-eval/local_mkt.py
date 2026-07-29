"""Step 2: fit a LOCAL market-level model walk-forward and ask whether it
matches the vendor lean's incremental power over naive breadth.

Step 1 refuted the shortcut (the lean is not affine in market averages, OOS R2
<= 0.05). So stop imitating the intermediate and predict the outcome directly.

Protocol (identical discipline to the vendor evaluation):
  data    mkt.pkl, 40056 contiguous hours 2022-01-01 ~ 2026-07-28
  target  edge = mean_i(net_long) - mean_i(net_short), the realized 72h
          ATR-ladder market edge net of 10bps
  splits  quarterly walk-forward, trailing 12-month train, 72h purge, test the
          next quarter, refit each quarter
  model   ridge on standardised market-average features; lambda chosen on an
          INNER time split of the training window only
  guard   72h label overlap => ~n/72 effective observations. All CIs use a
          7-day block bootstrap. Never a random split.
"""
from __future__ import annotations
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
PURGE = pd.Timedelta(hours=72)
LAMS = (10.0, 30.0, 100.0, 300.0, 1000.0, 3000.0, 10000.0)


def ridge(X, y, lam):
    Xc = np.column_stack([X, np.ones(len(X))])
    A = Xc.T @ Xc + lam * np.eye(Xc.shape[1])
    A[-1, -1] -= lam
    return np.linalg.solve(A, Xc.T @ y)


def apply(b, X):
    return np.column_stack([X, np.ones(len(X))]) @ b


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


def main():
    mk = pd.read_pickle(f"{BASE}/mkt.pkl")
    fcols = [c for c in mk.columns if c.startswith("m_")]
    mk = mk.dropna(subset=["edge", "dn24"])
    X_all = mk[fcols].to_numpy("float64")
    ok = np.isfinite(X_all).all(axis=1)
    mk, X_all = mk[ok], X_all[ok]
    print(f"市场级面板 {len(mk)} 小时 {mk.index.min().date()}~{mk.index.max().date()}")
    print(f"标签重叠 72h => 有效独立观测约 {int((mk.index[-1]-mk.index[0])/pd.Timedelta(hours=72))}\n")

    y_all = mk.edge.to_numpy("float64")
    qs = pd.date_range("2023-01-01", "2026-07-01", freq="QS", tz="UTC")
    rows = []
    for qstart in qs:
        qend = qstart + pd.offsets.QuarterBegin(startingMonth=1)
        tr_end = qstart - PURGE
        tr_start = tr_end - pd.Timedelta(days=365)
        trm = (mk.index >= tr_start) & (mk.index < tr_end)
        tem = (mk.index >= qstart) & (mk.index < qend)
        if trm.sum() < 2000 or tem.sum() < 100:
            continue
        Xtr, ytr = X_all[trm], y_all[trm]
        mu, sd = Xtr.mean(0), Xtr.std(0)
        sd[sd == 0] = 1.0
        Ztr = (Xtr - mu) / sd
        # inner time split for lambda: last 25% of the training window
        icut = int(len(Ztr) * 0.75)
        best_lam, best_s = None, -9e9
        for lam in LAMS:
            b = ridge(Ztr[:icut], ytr[:icut], lam)
            p = apply(b, Ztr[icut:])
            s = np.corrcoef(p, ytr[icut:])[0, 1] if p.std() > 0 else -9
            if np.isfinite(s) and s > best_s:
                best_s, best_lam = s, lam
        b = ridge(Ztr, ytr, best_lam)
        Zte = (X_all[tem] - mu) / sd
        rows.append(pd.DataFrame({
            "score": apply(b, Zte),
            "edge": y_all[tem],
            "dn24": mk.dn24.to_numpy()[tem],
            "our_share": mk.our_share.to_numpy()[tem],
            "q": str(qstart.date()),
            "lam": best_lam}, index=mk.index[tem]))
        print(f"  {qstart.date()} 训练 {trm.sum():5d}h lam={best_lam:7.0f} "
              f"内验 corr {best_s:+.3f} 测试 {tem.sum():4d}h", flush=True)

    o = pd.concat(rows).sort_index()
    o.to_pickle(f"{BASE}/local_mkt_oos.pkl")
    t = o.index.tz_localize(None).to_numpy("datetime64[ns]")
    print(f"\n样本外合计 {len(o)} 小时 {o.index.min().date()}~{o.index.max().date()}")
    print(f"corr(本地分数, edge) = {o.score.corr(o.edge):+.4f}")
    print(f"corr(dn24, edge)     = {o.dn24.corr(o.edge):+.4f}")
    print(f"corr(本地分数, dn24) = {o.score.corr(o.dn24):+.4f}")

    print("\n=== 联合回归 edge ~ 本地分数 + dn24 (标准化) ===")
    Z = np.column_stack([
        (o.score - o.score.mean()) / o.score.std(),
        (o.dn24 - o.dn24.mean()) / o.dn24.std(),
        np.ones(len(o))])
    y = o.edge.to_numpy()
    beta, *_ = np.linalg.lstsq(Z, y, rcond=None)
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // 7)
    u = np.unique(blk)
    ib = {bb: np.where(blk == bb)[0] for bb in u}
    bs = np.empty((1000, 3))
    for i in range(1000):
        pick = np.concatenate([ib[bb] for bb in RNG.choice(u, len(u), replace=True)])
        bs[i], *_ = np.linalg.lstsq(Z[pick], y[pick], rcond=None)
    for i, nm in enumerate(["本地分数(标准化)", "dn24(标准化)", "常数"]):
        lo, hi = np.percentile(bs[:, i], [2.5, 97.5])
        print(f"  {nm:20s} {beta[i]*100:+.5f}% CI [{lo*100:+.5f},{hi*100:+.5f}] "
              f"{'显著' if (lo>0)==(hi>0) else ''}")

    print("\n=== M5 同口径: 分数看空时 LONG 减半, 逐季 ===")
    cut = o.score.median()
    wins = 0
    qres = []
    for q, gq in o.groupby("q"):
        bear = (gq.score <= cut).to_numpy()
        ml = gq.edge.to_numpy()          # proxy: edge is long-minus-short
        # M5 effect on a long book: damping when bearish avoids half the loss
        eff = (np.where(bear, 0.5, 1.0) * ml - ml).mean()
        qres.append((q, eff, bear.mean()))
        wins += eff > 0
    for q, eff, cov in qres:
        print(f"  {q}: 改善 {eff*100:+.4f}%  看空覆盖 {cov*100:4.0f}%")
    m, lo, hi = block_ci(t, (np.where((o.score <= cut).to_numpy(), 0.5, 1.0)
                             * o.edge.to_numpy() - o.edge.to_numpy()))
    print(f"\n  逐季 {wins}/{len(qres)} 胜, 小时级 {m*100:+.4f}% "
          f"CI [{lo*100:+.4f},{hi*100:+.4f}] {'显著' if (lo>0)==(hi>0) else '不显著'}")


if __name__ == "__main__":
    main()
