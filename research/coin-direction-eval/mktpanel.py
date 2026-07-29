"""Build an hourly MARKET-LEVEL panel: cross-sectional means of the 86 local
features, plus naive breadth reads and the realized long-vs-short edge.

Why this object: the only vendor read that survived evaluation is the aggregate
lean = mean_i(raw_up_i). raw_up is a logistic score over standardised features,
and with the near-flat slopes we measured, mean_i sigma(w.x_i) is close to an
affine function of mean_i(x_i). So the per-coin model may be unnecessary: a
market-level model over market-average features could reproduce the same read
with no vendor artifact, no joblib, no sklearn pin, and no expiry.

Output: mkt.pkl  (index = hour UTC, one row per hour)
"""
from __future__ import annotations
import glob
import gc
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"

# The realized outcome we care about, and the naive reads we must beat.
OUTCOME = ["net_long", "net_short"]
DROP = set(OUTCOME + ["future_score_24", "future_score_72", "slope168"])


def main():
    files = sorted(glob.glob(f"{BASE}/wf_cache/*.pkl"))
    print(f"币种缓存 {len(files)} 个", flush=True)

    feat_cols = None
    # accumulate sum / count per hour so we never hold all coins at once (1 core, 1.9GB)
    sums = {}
    cnts = {}
    extra = {}   # naive breadth counters
    for i, p in enumerate(files):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        if feat_cols is None:
            feat_cols = [c for c in d.columns if c not in DROP]
            print(f"市场级特征 {len(feat_cols)} 列", flush=True)
        num = d[feat_cols].to_numpy(dtype="float64")
        ok = np.isfinite(num)
        num = np.where(ok, num, 0.0)
        idx = d.index.to_numpy()
        g = pd.DataFrame(num, index=idx, columns=feat_cols)
        gk = pd.DataFrame(ok.astype("float64"), index=idx, columns=feat_cols)
        s = g.groupby(level=0).sum()
        c = gk.groupby(level=0).sum()
        for k, v in ((0, s), (1, c)):
            tgt = sums if k == 0 else cnts
            for t, row in zip(v.index.to_numpy(), v.to_numpy()):
                if t in tgt:
                    tgt[t] += row
                else:
                    tgt[t] = row.copy()
        # naive reads + realized edge, computed on the same rows
        e = pd.DataFrame({
            "nl": d.net_long.to_numpy(),
            "ns": d.net_short.to_numpy(),
            "dn24": (d.ret_24.to_numpy() < 0).astype("float64"),
            "r24ok": np.isfinite(d.ret_24.to_numpy()).astype("float64"),
            "sl_dn": (d.slope168.to_numpy() < 0).astype("float64"),
            "slok": np.isfinite(d.slope168.to_numpy()).astype("float64"),
            "n": 1.0}, index=idx)
        eg = e.groupby(level=0).sum()
        for t, row in zip(eg.index.to_numpy(), eg.to_numpy()):
            if t in extra:
                extra[t] += row
            else:
                extra[t] = row.copy()
        del d, num, ok, g, gk, s, c, e, eg
        if i % 10 == 0:
            gc.collect()
            print(f"  {i+1}/{len(files)}", flush=True)

    ts = sorted(sums)
    S = np.vstack([sums[t] for t in ts])
    C = np.vstack([cnts[t] for t in ts])
    with np.errstate(invalid="ignore", divide="ignore"):
        M = np.where(C > 0, S / np.maximum(C, 1e-9), np.nan)
    mk = pd.DataFrame(M, index=pd.DatetimeIndex(ts, name="t"),
                      columns=[f"m_{c}" for c in feat_cols])

    E = np.vstack([extra[t] for t in ts])
    ecols = ["nl", "ns", "dn24", "r24ok", "sl_dn", "slok", "n"]
    ed = pd.DataFrame(E, index=mk.index, columns=ecols)
    mk["n"] = ed.n
    mk["edge"] = ed.nl / ed.n - ed.ns / ed.n
    mk["dn24"] = ed.dn24 / ed.r24ok.replace(0, np.nan)
    mk["our_share"] = ed.sl_dn / ed.slok.replace(0, np.nan)

    mk = mk[mk.n >= 10].sort_index()
    print(f"\n市场级面板 {mk.shape}  {mk.index.min()} ~ {mk.index.max()}")
    mk.to_pickle(f"{BASE}/mkt.pkl")
    print("已写 mkt.pkl")


if __name__ == "__main__":
    main()
