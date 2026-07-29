"""Quarterly artifact minting driver — the answer to the 2026-10-01 hard expiry.

WHAT THE EXPIRY ACTUALLY IS
===========================
Not a hardcoded date bomb. `coin_direction_attribute_v4/v6.py` reads
`valid_from` / `valid_until_exclusive` out of the artifact's own metadata and
returns NEUTRAL with reason MODEL_OUT_OF_WINDOW when the decision time falls
outside. That is CORRECT design: a quarterly model must not silently keep scoring
after its window, and silent staleness is worse than a loud NEUTRAL.

So the expiry is not a defect to be patched — it is a driver that must exist.
The delivery's actual defect is that it ships no way to mint the next artifact:
`fit_hurdle_model` is never called anywhere in it, and `PlattCalibrator.fit` is
never constructed. `.ai-memory/coin-direction-vendor-eval.md` §7.1 also refutes
replacing the model with a local one (OOS R^2 <= 0.05), so the only way forward is
to reproduce the vendor's own recipe. This script does that.

WHAT IT DOES NOT DO
===================
It does not extend an existing artifact's window. Rewriting `valid_until` on a
model trained on older data would defeat the whole point of the gate. It trains a
NEW model on data up to the quarter boundary and writes a new artifact whose window
is that quarter.

RECIPE (identical to walkforward.py, which reproduced 15 honest quarters)
  features   add_local_features -> the artifact's own 86 columns
  labels     72h path score, trendable = 30th percentile of |score|
  purge      72h between train end and quarter start (= label horizon)
  train win  365 days ending at (quarter start - 72h)
  model      class-balanced L2 logistic hurdle (their fit_hurdle_model)
  calibrate  Platt on the last 20% of the training window, by time
  mask       cov >= 0.80 (the inference-side gate; NOT "all 86 finite" — the
             WinsorStandardizer median-imputes, so the stricter mask distorts)
  V4 clock   open-time + 1h = close-time, applied ONCE to the V4 copy

Two pitfalls this script is written to make impossible:
  - the +1h V4 clock shift is applied in exactly one place (build_symbol, reused
    from the walkforward module) so it can never be applied twice;
  - the artifact is verified by LOADING IT BACK through the vendor's own runtime
    and scoring one symbol, so a mint that the vendor code cannot consume fails
    loudly here instead of silently returning NEUTRAL in production.

USAGE
  python mint_artifact.py --quarter 2026Q4 --out /path/to/artifacts
  python mint_artifact.py --quarter 2026Q4 --dry-run     # fit + verify, write nothing
"""
from __future__ import annotations

import argparse
import gc
import hashlib
import json
import os
import sys
import time
import warnings

import numpy as np
import pandas as pd

warnings.simplefilter("ignore")

DELIVERY = ("/root/projects/nofxmax/coin_direction_final_claude_delivery_2026-07-29"
            "/coin_direction_final_claude_delivery")
EVAL_DIR = os.path.dirname(os.path.abspath(__file__))
WORK = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"

sys.path.insert(0, f"{DELIVERY}/v4_candidate/src")
sys.path.insert(0, f"{DELIVERY}/evidence/src")
sys.path.insert(0, WORK)
sys.path.insert(0, EVAL_DIR)

import joblib  # noqa: E402
from coin_trend_classifier_v3 import (  # noqa: E402
    add_local_features, add_future_labels, calibrate_label_thresholds,
    apply_label_thresholds,
)
from trend_classifier_v3_models import fit_hurdle_model, PlattCalibrator  # noqa: E402

REFERENCE_ARTIFACT = f"{DELIVERY}/v4_candidate/coin_trend_classifier_v4_2026q3.joblib"
PROTOCOL_VERSION = "v4-rigorous-1"
TRAIN_DAYS = 365
PURGE_HOURS = 72
LABEL_QUANTILE = 0.30
CAL_TAIL_FRAC = 0.20
COV_MIN = 0.80
TRAIN_STRIDE = 4
MIN_LABELLED = 2000
MIN_FIT, MIN_CAL = 1000, 300
MIN_SYMBOLS = 8


def reference_columns() -> list[str]:
    """The 86 feature columns, taken from the shipped artifact.

    Read from the artifact rather than recomputed, so a change in
    add_local_features can never silently reorder or rename the inputs a model is
    fitted on. If the recipe genuinely changes, this raises instead of drifting.
    """
    ref = joblib.load(REFERENCE_ARTIFACT)
    return list(ref["model"].transformer.columns)


def quarter_bounds(q: str) -> tuple[pd.Timestamp, pd.Timestamp]:
    p = pd.Period(q, freq="Q")
    start = pd.Timestamp(p.start_time, tz="UTC")
    end = pd.Timestamp(p.end_time, tz="UTC").ceil("D")
    return start, end


def build_training_frame(feat_cols, train_start, train_end, cache_dir):
    """Load the per-symbol feature caches and concatenate the training window."""
    if not os.path.isdir(cache_dir):
        raise SystemExit(
            f"feature cache {cache_dir} missing — run walkforward.py first "
            f"(it builds per-symbol caches; rebuilding here would duplicate that "
            f"logic and risk a second +1h clock shift)")
    keep = feat_cols + ["future_score_24", "future_score_72"]
    parts = []
    syms = sorted(f[:-4] for f in os.listdir(cache_dir) if f.endswith(".pkl"))
    for s in syms:
        fr = pd.read_pickle(f"{cache_dir}/{s}.pkl")
        sub = fr[(fr.index >= train_start) & (fr.index < train_end)]
        if TRAIN_STRIDE > 1:
            sub = sub[sub.index.hour % TRAIN_STRIDE == 0]
        if len(sub) > 200:
            missing = [c for c in keep if c not in sub.columns]
            if missing:
                raise SystemExit(f"{s} cache lacks {len(missing)} required columns "
                                 f"(first: {missing[:3]}) — cache is stale")
            parts.append(sub[keep].copy())
        del fr, sub
    if len(parts) < MIN_SYMBOLS:
        raise SystemExit(f"only {len(parts)} symbols have data in "
                         f"{train_start.date()}~{train_end.date()}, need {MIN_SYMBOLS}")
    train = pd.concat(parts).sort_index()
    del parts
    gc.collect()
    return train, len(syms)


def fit_quarter(quarter: str, cache_dir: str):
    feat_cols = reference_columns()
    qstart, qend = quarter_bounds(quarter)
    train_end = qstart - pd.Timedelta(hours=PURGE_HOURS)
    train_start = train_end - pd.Timedelta(days=TRAIN_DAYS)
    print(f"季度 {quarter}: 生效窗 [{qstart} , {qend})")
    print(f"训练窗 [{train_start} , {train_end})  purge {PURGE_HOURS}h  "
          f"特征 {len(feat_cols)} 列")

    train, n_sym = build_training_frame(feat_cols, train_start, train_end, cache_dir)
    print(f"训练样本 {len(train)} 行 / {n_sym} 币(缓存中)")

    th = calibrate_label_thresholds({"t": train}, str(train_start), str(train_end),
                                    cohort="mint", quantile=LABEL_QUANTILE)
    train = apply_label_thresholds(train, th)

    # cov >= 0.80: the inference-side gate. See module docstring.
    cov = np.isfinite(train[feat_cols].to_numpy(dtype="float32")).mean(axis=1)
    mask = (train["direction_72"].notna() & train["trendable_72"].notna()
            & (cov >= COV_MIN)).to_numpy()
    if mask.sum() < MIN_LABELLED:
        raise SystemExit(f"only {mask.sum()} labelled rows pass cov>={COV_MIN}, "
                         f"need {MIN_LABELLED}")
    train["direction_72"] = train["direction_72"].fillna(0).astype("int8")
    train["trendable_72"] = train["trendable_72"].fillna(0).astype("int8")

    # Platt calibration on the LAST 20% of the training window by time. Time-based,
    # not random: a random split would leak, since a 72h label makes neighbouring
    # rows near-duplicates.
    cut = train.index[mask][int(mask.sum() * (1.0 - CAL_TAIL_FRAC))]
    fit_mask = mask & np.asarray(train.index < cut)
    cal_mask = mask & np.asarray(train.index >= cut)
    print(f"标注 {int(mask.sum())} 行 → 拟合 {int(fit_mask.sum())} / "
          f"校准 {int(cal_mask.sum())} (切点 {cut})")
    if fit_mask.sum() < MIN_FIT or cal_mask.sum() < MIN_CAL:
        raise SystemExit(f"split too small: fit={int(fit_mask.sum())} cal={int(cal_mask.sum())}")

    model = fit_hurdle_model("mint", "logistic", 72, train, feat_cols, fit_mask)
    cal = train.loc[cal_mask]
    raw_up, raw_tr = model.predict_raw(cal)
    tr_lbl = cal["trendable_72"].to_numpy()
    pc_dir = PlattCalibrator.fit(raw_up[tr_lbl == 1],
                                 cal["direction_72"].to_numpy()[tr_lbl == 1])
    pc_tr = PlattCalibrator.fit(raw_tr, tr_lbl)
    print(f"Platt(direction) 斜率 {pc_dir.coefficient:+.6f} 截距 {pc_dir.intercept:+.6f}")
    print(f"Platt(trendable) 斜率 {pc_tr.coefficient:+.6f} 截距 {pc_tr.intercept:+.6f}")
    print("注意: 斜率≈0 或为负说明该季训练窗里方向分数几乎没有可校准的信息 —— "
          "这是**结论**不是故障(评估阶段交叉拟合出的斜率就是 −0.09~−0.01)。")

    n_fit = int(fit_mask.sum())
    rows = int(mask.sum())
    del train, cal, raw_up, raw_tr, tr_lbl
    gc.collect()

    artifact = {
        "protocol_version": PROTOCOL_VERSION,
        "model_version": f"ctc-v4-{quarter.lower()}",
        "active_quarter": quarter,
        "valid_from": str(qstart),
        "valid_until_exclusive": str(qend),
        "model": model,
        "calibrators": {"direction": pc_dir, "trendable": pc_tr},
        "selected_policy": {"name": "W2", "p_trendable_min": 0.50, "dir_prob_min": 0.55},
        "label_thresholds": th,
        "training": {
            "start": str(train_start),
            "end_exclusive": str(train_end),
            "rows": rows,
            "fit_rows": n_fit,
            "symbols": n_sym,
            "quantile": LABEL_QUANTILE,
            "coverage_min": COV_MIN,
            "purge_hours": PURGE_HOURS,
            "minted_by": "research/coin-direction-eval/mint_artifact.py",
        },
    }
    return artifact, feat_cols, qstart


def verify_roundtrip(path: str, feat_cols, qstart) -> None:
    """Load the artifact back and score one symbol through the vendor runtime.

    This is the gate that matters: a mint the vendor code cannot consume would
    otherwise show up in production as a permanent silent NEUTRAL.
    """
    loaded = joblib.load(path)
    for k in ("protocol_version", "model_version", "valid_from",
              "valid_until_exclusive", "model", "calibrators", "selected_policy"):
        if k not in loaded:
            raise SystemExit(f"round-trip FAILED: artifact lacks {k!r}")
    cols = list(loaded["model"].transformer.columns)
    if cols != feat_cols:
        raise SystemExit(f"round-trip FAILED: feature columns changed "
                         f"({len(cols)} vs {len(feat_cols)})")

    import harness
    syms = sorted({f.split("-", 1)[0] for f in os.listdir(harness.KL) if f.endswith(".csv")})
    bars = None
    for s in syms:
        b = harness.load_symbol(s)
        if b is not None and len(b) > 800:
            bars, sym = b, s
            break
    if bars is None:
        print("⚠️  round-trip: no cached klines to score against; "
              "structural checks passed, scoring check SKIPPED")
        return

    v4 = bars.copy()
    v4.index = v4.index + pd.Timedelta(hours=1)   # the one and only V4 clock shift
    feats = add_local_features(v4)
    row = feats[feats.index < qstart + pd.Timedelta(days=20)].tail(1)
    if len(row) == 0:
        row = feats.tail(1)
    r_up, r_tr = loaded["model"].predict_raw(row)
    p_dir = loaded["calibrators"]["direction"].predict(r_up)
    p_tr = loaded["calibrators"]["trendable"].predict(r_tr)
    if not (np.isfinite(p_dir).all() and np.isfinite(p_tr).all()):
        raise SystemExit("round-trip FAILED: calibrated probabilities are not finite")
    print(f"✅ 回读验证通过: {sym} @ {row.index[-1]} → "
          f"p_dir={float(p_dir[0]):.4f} p_trendable={float(p_tr[0]):.4f}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--quarter", required=True, help="e.g. 2026Q4")
    ap.add_argument("--out", default=f"{WORK}/minted")
    ap.add_argument("--cache", default=f"{WORK}/wf_cache")
    ap.add_argument("--dry-run", action="store_true")
    a = ap.parse_args()

    t0 = time.time()
    artifact, feat_cols, qstart = fit_quarter(a.quarter, a.cache)

    if a.dry_run:
        print(f"\n[dry-run] 拟合完成, 未写盘。耗时 {time.time()-t0:.0f}s")
        return

    os.makedirs(a.out, exist_ok=True)
    stem = f"coin_trend_classifier_v4_{a.quarter.lower()}"
    path = f"{a.out}/{stem}.joblib"
    joblib.dump(artifact, path)
    sha = hashlib.sha256(open(path, "rb").read()).hexdigest()

    manifest = {
        "model_version": artifact["model_version"],
        "active_quarter": artifact["active_quarter"],
        "valid_from": artifact["valid_from"],
        "valid_until_exclusive": artifact["valid_until_exclusive"],
        "training": artifact["training"],
        "features": len(feat_cols),
        "created_at_unix": int(time.time()),
        "sha256": sha,
        "dependencies": {
            "python": ">=3.12", "numpy": ">=2.0", "pandas": ">=2.0",
            "scikit_learn": "1.8.x recommended", "joblib": ">=1.4",
        },
    }
    with open(f"{a.out}/model_manifest_{a.quarter.lower()}.json", "w") as fh:
        json.dump(manifest, fh, indent=1, ensure_ascii=False)

    print(f"\n写出 {path}\n     sha256 {sha}")
    verify_roundtrip(path, feat_cols, qstart)
    print(f"\n耗时 {time.time()-t0:.0f}s")
    print("上线前仍必须做的两件事(本脚本不替代):")
    print("  1) 用 walkforward.py 在该季上做一次诚实 OOS 评分, 确认这一季不是退化季;")
    print("  2) 影子记录跑满一个季度再谈动仓位 —— 铸出 artifact 只解决'能续命',")
    print("     不解决'该不该用'。")


if __name__ == "__main__":
    main()
