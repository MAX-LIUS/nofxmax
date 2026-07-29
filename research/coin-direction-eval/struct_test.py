"""Step 1 (structural): is the vendor lean an affine function of the 86
MARKET-AVERAGE features?

If yes, the per-coin hurdle model + two Platt calibrators + joblib artifact are
unnecessary machinery for the only read that survived evaluation, and the lean
can be reproduced by a local market-level model with no expiry.

Honesty guards:
  - the 72h label overlap means ~n/72 effective observations; report it.
  - fit/score split is BY TIME (never random), so a high score cannot come from
    interpolating between neighbouring overlapping hours.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"


def vendor_lean():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up)]
        g = d.groupby(d.index)
        acc.append(pd.DataFrame({"lean": g["raw_up"].mean(), "nv": g.size()}))
        del d, g
    v = pd.concat(acc).sort_index()
    return v[v.nv >= 10]


def ridge_fit(X, y, lam):
    Xc = np.column_stack([X, np.ones(len(X))])
    A = Xc.T @ Xc + lam * np.eye(Xc.shape[1])
    A[-1, -1] -= lam                      # do not penalise the intercept
    return np.linalg.solve(A, Xc.T @ y)


def r2(y, p):
    return 1.0 - np.sum((y - p) ** 2) / np.sum((y - y.mean()) ** 2)


def main():
    mk = pd.read_pickle(f"{BASE}/mkt.pkl")
    v = vendor_lean()
    fcols = [c for c in mk.columns if c.startswith("m_")]
    j = mk.join(v[["lean"]], how="inner").dropna(subset=["lean"])
    print(f"市场级面板 {len(mk)} 小时, 厂商 lean {len(v)} 小时, 交集 {len(j)} 小时")
    print(f"标签重叠 72h => 有效独立观测约 {len(j)//72}\n")

    X = j[fcols].to_numpy("float64")
    # market averages are still NaN where every coin lacked the feature
    good = np.isfinite(X).all(axis=1)
    X, y, idx = X[good], j.lean.to_numpy("float64")[good], j.index[good]
    print(f"全列有限的小时 {len(X)} / {len(j)}")

    mu, sd = X.mean(0), X.std(0)
    sd[sd == 0] = 1.0
    Z = (X - mu) / sd

    # split BY TIME: first 70% fit, last 30% score
    cut = int(len(Z) * 0.70)
    print(f"按时间切分: 拟合 {cut} 小时 ({idx[0].date()}~{idx[cut-1].date()}), "
          f"打分 {len(Z)-cut} 小时 ({idx[cut].date()}~{idx[-1].date()})\n")

    print("=== 用 86 个市场均值特征线性逼近厂商 lean ===")
    for lam in (1.0, 10.0, 100.0, 1000.0):
        b = ridge_fit(Z[:cut], y[:cut], lam)
        pin = np.column_stack([Z[:cut], np.ones(cut)]) @ b
        pout = np.column_stack([Z[cut:], np.ones(len(Z) - cut)]) @ b
        print(f"  ridge lam={lam:7.1f}: 样本内 R2 {r2(y[:cut], pin):.4f}  "
              f"样本外 R2 {r2(y[cut:], pout):.4f}  "
              f"corr(样本外) {np.corrcoef(y[cut:], pout)[0,1]:+.4f}")

    # How few features are actually needed? Greedy forward selection on the fit half.
    print("\n=== 贪心前向选择: 前 8 个特征就能逼近到什么程度 ===")
    chosen, remaining = [], list(range(Z.shape[1]))
    for step in range(8):
        best, bi = -9e9, None
        for c in remaining:
            cols = chosen + [c]
            b = ridge_fit(Z[:cut][:, cols], y[:cut], 10.0)
            p = np.column_stack([Z[cut:][:, cols], np.ones(len(Z) - cut)]) @ b
            s = r2(y[cut:], p)
            if s > best:
                best, bi = s, c
        chosen.append(bi)
        remaining.remove(bi)
        print(f"  +{fcols[bi]:34s} 累计样本外 R2 = {best:.4f}")

    print("\n=== 对照: 单个朴素读数能逼近 lean 到什么程度 ===")
    for nm in ("dn24", "our_share"):
        c = j[nm].to_numpy("float64")[good]
        m = np.isfinite(c)
        print(f"  {nm:10s}: corr = {np.corrcoef(c[m], y[m])[0,1]:+.4f}  "
              f"R2 = {r2(y[m], np.polyval(np.polyfit(c[m], y[m], 1), c[m])):.4f}")


if __name__ == "__main__":
    main()
