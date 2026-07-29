"""Why does the retrained V4 collapse to one class? Calibrator or model?

The walk-forward showed DOWN coverage swinging 0%->100% across quarters with BA
pinned at 50%, i.e. the state machine emits a single class. Before calling that
a property of the method, rule out MY calibration choice: I used a
time-contiguous last-20% tail of the training window for Platt, and if that tail
happened to trend, the calibrator shifts every probability one way.

Compared here on one quarter, on identical fitted models:
  raw            uncalibrated p_up / p_trendable from the logistic
  tail           Platt fitted on the time-contiguous last 20% (what I did)
  crossfit       Platt fitted on pooled out-of-fold scores from 4 time folds
  vendor         the shipped 2026Q3 calibrator constants, for reference

Also prints the shipped artifact's own coverage on the same quarter, so we can
see whether the vendor's frozen calibrator would have degenerated too.
"""
from __future__ import annotations
import sys, os, warnings
import numpy as np
import pandas as pd

warnings.simplefilter("ignore")
D = "/root/projects/nofxmax/coin_direction_final_claude_delivery_2026-07-29/coin_direction_final_claude_delivery"
sys.path.insert(0, f"{D}/v4_candidate/src")
sys.path.insert(0, "/root/.claude/jobs/5cbb3cf4/tmp/cdeval")

from coin_trend_classifier_v3 import (
    calibrate_label_thresholds, apply_label_thresholds, state_from_probabilities)
from trend_classifier_v3_models import fit_hurdle_model, PlattCalibrator
import walkforward as W

QS = [pd.Timestamp(x, tz="UTC") for x in ("2023-04-01", "2023-10-01", "2025-04-01")]


def describe(tag, p_up, p_tr, truth):
    st = state_from_probabilities(p_up, p_tr, 0.50, 0.55)
    down = (st < 0).mean() * 100
    up = (st > 0).mean() * 100
    neu = (st == 0).mean() * 100
    e = st != 0
    ba = np.nan
    if e.sum() > 50:
        pr, tv = (st[e] > 0), truth[e]
        ok = np.isfinite(tv.astype(float))
        pr, tv = pr[ok], tv[ok] > 0
        if tv.any() and (~tv).any():
            ba = 0.5 * ((pr & tv).sum() / tv.sum() + ((~pr) & (~tv)).sum() / (~tv).sum())
    print(f"    {tag:<10} p_up[p5={np.percentile(p_up,5):.3f} 中位={np.median(p_up):.3f} "
          f"p95={np.percentile(p_up,95):.3f}]  p_tr中位={np.median(p_tr):.3f}  "
          f"DOWN={down:5.1f}% UP={up:5.1f}% NEU={neu:5.1f}%  BA="
          + ("  n/a" if not np.isfinite(ba) else f"{ba*100:.2f}%"))


def main():
    have = sorted(f[:-4] for f in os.listdir(W.CDIR) if f.endswith(".pkl"))
    for qstart in QS:
        qend = qstart + pd.DateOffset(months=3)
        train_end = qstart - pd.Timedelta(hours=72)
        train_start = train_end - pd.Timedelta(days=365)
        tr_p, te_p = [], []
        for s in have:
            tr = W.load_slice(s, train_start, train_end, W.TRAIN_STRIDE)
            te = W.load_slice(s, qstart, qend, W.TEST_STRIDE)
            if tr is not None and te is not None and len(tr) > 200 and len(te) > 50:
                tr_p.append(tr); te_p.append(te)
        train = pd.concat(tr_p).sort_index(); test = pd.concat(te_p).sort_index()
        del tr_p, te_p
        th = calibrate_label_thresholds({"t": train}, str(train_start), str(train_end),
                                        cohort="d", quantile=0.30)
        train = apply_label_thresholds(train, th)
        cov = np.isfinite(train[W.FEAT].to_numpy(dtype="float32")).mean(axis=1)
        mask = (train["direction_72"].notna() & train["trendable_72"].notna()
                & (cov >= 0.80)).to_numpy()
        train["direction_72"] = train["direction_72"].fillna(0).astype("int8")
        train["trendable_72"] = train["trendable_72"].fillna(0).astype("int8")

        te = test[np.isfinite(test[W.FEAT].to_numpy(dtype="float32")).mean(axis=1) >= 0.80]
        te = apply_label_thresholds(te, th)
        truth = te["future_score_72"].to_numpy()
        print(f"\n=== {qstart.date()}  训练 {int(mask.sum())} 行  测试 {len(te)} 行 ===")
        print(f"    训练集标签分布: UP={float(train.loc[mask,'direction_72'].mean())*100:.1f}%  "
              f"trendable={float(train.loc[mask,'trendable_72'].mean())*100:.1f}%")

        # (a) my time-contiguous tail
        cut = train.index[mask][int(mask.sum() * 0.80)]
        fit_m = mask & np.asarray(train.index < cut)
        cal_m = mask & np.asarray(train.index >= cut)
        model = fit_hurdle_model("d", "logistic", 72, train, W.FEAT, fit_m)
        r_up, r_tr = model.predict_raw(te)
        describe("raw", r_up, r_tr, truth)
        cal = train.loc[cal_m]
        cru, crt = model.predict_raw(cal)
        tl = cal["trendable_72"].to_numpy()
        print(f"    校准集(时间尾部) UP占比={float(cal.loc[tl==1,'direction_72'].mean())*100:.1f}% "
              f"n={int(cal_m.sum())}")
        pd_ = PlattCalibrator.fit(cru[tl == 1], cal["direction_72"].to_numpy()[tl == 1])
        pt_ = PlattCalibrator.fit(crt, tl)
        print(f"    tail 校准器 dir(coef={pd_.coefficient:.3f}, int={pd_.intercept:+.3f})")
        describe("tail", pd_.predict(r_up), pt_.predict(r_tr), truth)

        # (b) cross-fitted Platt over 4 time folds, model refit on all data
        idx = np.where(mask)[0]
        folds = np.array_split(idx, 4)
        oof_up, oof_tr, oof_y, oof_t = [], [], [], []
        for k in range(4):
            hold = folds[k]
            fit_k = np.zeros(len(train), bool)
            for j in range(4):
                if j != k:
                    fit_k[folds[j]] = True
            mk = fit_hurdle_model("d", "logistic", 72, train, W.FEAT, fit_k)
            sub = train.iloc[hold]
            a, b = mk.predict_raw(sub)
            oof_up.append(a); oof_tr.append(b)
            oof_y.append(sub["direction_72"].to_numpy())
            oof_t.append(sub["trendable_72"].to_numpy())
        ou = np.concatenate(oof_up); ot = np.concatenate(oof_tr)
        oy = np.concatenate(oof_y); oq = np.concatenate(oof_t)
        full = fit_hurdle_model("d", "logistic", 72, train, W.FEAT, mask)
        fr_up, fr_tr = full.predict_raw(te)
        pdc = PlattCalibrator.fit(ou[oq == 1], oy[oq == 1])
        ptc = PlattCalibrator.fit(ot, oq)
        print(f"    crossfit 校准器 dir(coef={pdc.coefficient:.3f}, int={pdc.intercept:+.3f})")
        describe("crossfit", pdc.predict(fr_up), ptc.predict(fr_tr), truth)

        # (c) vendor's shipped calibrator constants applied to our raw scores
        vd = PlattCalibrator(coefficient=0.42619583024533414, intercept=0.0025198288572048783)
        vt = PlattCalibrator(coefficient=0.4341543486796421, intercept=0.9665521230632719)
        describe("vendor常数", vd.predict(fr_up), vt.predict(fr_tr), truth)
        del train, test, te


if __name__ == "__main__":
    main()
