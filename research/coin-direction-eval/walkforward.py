"""DECISIVE TEST: quarterly walk-forward of the V4 method, retrained by us.

Why this and not the frozen artifact: the shipped 2026Q3 artifact has only ~31
honest out-of-sample days, which cannot reach the vendor's own promotion bar
(>=12 independent 7d blocks). So the frozen artifact cannot answer "is this a
reliable gate" either way. Retraining the same recipe quarterly lets every
scored quarter be genuinely unseen, giving many independent OOS quarters.

Recipe held identical to V4 (their own code does the fitting):
  features add_local_features(86) / labels 72h path score, trendable=30th pct
  model class-balanced L2 logistic hurdle / Platt on a held-out training tail
  state p_trendable>=0.50 & direction prob>=0.55 (their selected policy W2)
  purge 72h between train end and test start (= label horizon)
Scored on the SAME frozen ATR exit sim + 10bps, adverse-first.

Memory: 1GB box. Per-symbol slim caches on disk, streamed per quarter, and
sampling thinned (train 4h, test 6h) since a 72h label makes adjacent hourly
rows near-duplicates.
"""
from __future__ import annotations
import os, sys, gc, warnings, pickle
import numpy as np
import pandas as pd

warnings.simplefilter("ignore")
D = "/root/projects/nofxmax/coin_direction_final_claude_delivery_2026-07-29/coin_direction_final_claude_delivery"
sys.path.insert(0, f"{D}/v4_candidate/src")
sys.path.insert(0, f"{D}/evidence/src")
sys.path.insert(0, "/root/.claude/jobs/5cbb3cf4/tmp/cdeval")

import harness
from coin_trend_classifier_v3 import (
    add_local_features, add_future_labels, calibrate_label_thresholds,
    apply_label_thresholds, state_from_probabilities,
)
from trend_classifier_v3_models import fit_hurdle_model, PlattCalibrator
from atr_counterfactual import AtrExitPolicy, simulate_vectorized

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
CDIR = f"{BASE}/wf_cache"
SDIR = f"{BASE}/wf_scored"
POLICY = AtrExitPolicy()
COST = 10.0 / 10_000.0
P_TRENDABLE_MIN, DIR_PROB_MIN = 0.50, 0.55
TRAIN_STRIDE, TEST_STRIDE = 4, 6
RNG = np.random.default_rng(20260729)
# the shipped 2026Q3 calibrator constants, applied to our own raw scores as a
# second (vendor-faithful) calibration alongside our honestly-fitted one
VENDOR_DIR = PlattCalibrator(coefficient=0.42619583024533414, intercept=0.0025198288572048783)
VENDOR_TR = PlattCalibrator(coefficient=0.4341543486796421, intercept=0.9665521230632719)

import joblib
FEAT = list(joblib.load(f"{D}/v4_candidate/coin_trend_classifier_v4_2026q3.joblib")["model"].transformer.columns)
KEEP = FEAT + ["future_score_24", "future_score_72", "net_long", "net_short", "slope168"]


def build_symbol(sym: str):
    bars = harness.load_symbol(sym)
    if bars is None or len(bars) < 800:
        return None
    v4 = bars.copy()
    v4.index = v4.index + pd.Timedelta(hours=1)   # open-time -> V4 close-time, once
    feats = add_local_features(v4)
    labels = add_future_labels(v4)
    frame = feats.join(labels[["future_score_24", "future_score_72"]], how="left")
    del feats, labels, v4

    c = bars["close"].to_numpy()
    atr = harness.wilder_atr(bars["high"].to_numpy(), bars["low"].to_numpy(), c)
    ohlc = bars[["open", "high", "low", "close"]].to_numpy()
    pos = np.arange(len(bars)) + 1
    for name, side in (("net_long", 1), ("net_short", -1)):
        ex = simulate_vectorized(ohlc, atr, pos, side, "adverse_first", POLICY)
        v = np.full(len(bars), np.nan, dtype="float32")
        g = ex.valid & np.isfinite(ex.gross_return)
        v[g] = ex.gross_return[g] - COST
        frame[name] = v
        del ex
    frame["slope168"] = harness.slope168(c).astype("float32")
    out = frame[KEEP].astype("float32")
    del frame, bars
    gc.collect()
    return out


def ensure_cache(symbols):
    os.makedirs(CDIR, exist_ok=True)
    for i, s in enumerate(symbols):
        p = f"{CDIR}/{s}.pkl"
        if os.path.exists(p):
            continue
        try:
            fr = build_symbol(s)
        except Exception as e:
            print(f"  {s} FAIL {type(e).__name__}: {str(e)[:70]}", flush=True)
            continue
        if fr is None:
            continue
        fr.to_pickle(p)
        print(f"  [{i+1}/{len(symbols)}] {s}: {len(fr)} rows "
              f"{fr.index.min().date()}~{fr.index.max().date()}", flush=True)
        del fr
        gc.collect()


def load_slice(sym, start, end, stride):
    p = f"{CDIR}/{sym}.pkl"
    if not os.path.exists(p):
        return None
    fr = pd.read_pickle(p)
    sub = fr[(fr.index >= start) & (fr.index < end)]
    if stride > 1:
        sub = sub[sub.index.hour % stride == 0]
    out = sub.copy()
    del fr, sub
    return out


def block_ci(t, v, n_boot=1500, block_days=14):
    if len(v) == 0:
        return (np.nan, np.nan, np.nan)
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.mean(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    m = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        m[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(m, [2.5, 97.5]))


def pct(x):
    return "   n/a  " if not np.isfinite(x) else f"{x*100:+.4f}%"


def edge(sub, opp, fav):
    if len(sub) == 0:
        return (np.nan,) * 6
    tt = sub.index.tz_localize(None).to_numpy("datetime64[ns]")
    o = block_ci(tt, sub["net_" + opp].to_numpy(dtype="float64"))
    a = block_ci(tt, (sub["net_" + fav] - sub["net_" + opp]).to_numpy(dtype="float64"))
    return (*o, *a)


def main():
    syms = sorted({f.split("-", 1)[0] for f in os.listdir(harness.KL) if f.endswith(".csv")})
    os.makedirs(SDIR, exist_ok=True)
    print(f"caching {len(syms)} symbols ...", flush=True)
    ensure_cache(syms)
    have = sorted(f[:-4] for f in os.listdir(CDIR) if f.endswith(".pkl"))
    print(f"cache ready: {len(have)} symbols\n", flush=True)

    results = []
    for qstart in pd.date_range("2023-01-01", "2026-07-01", freq="QS", tz="UTC"):
        qend = qstart + pd.DateOffset(months=3)
        train_end = qstart - pd.Timedelta(hours=72)      # purge the label horizon
        train_start = train_end - pd.Timedelta(days=365)

        tr_parts, te_parts = [], []
        for s in have:
            tr = load_slice(s, train_start, train_end, TRAIN_STRIDE)
            te = load_slice(s, qstart, qend, TEST_STRIDE)
            if tr is not None and te is not None and len(tr) > 200 and len(te) > 50:
                tr_parts.append(tr)
                te_parts.append(te.assign(symbol=s))
            del tr, te
        if len(tr_parts) < 8:
            print(f"{qstart.date()} skip ({len(tr_parts)} symbols)", flush=True)
            continue
        train = pd.concat(tr_parts).sort_index(); del tr_parts
        test = pd.concat(te_parts).sort_index(); del te_parts
        gc.collect()

        try:
            th = calibrate_label_thresholds({"t": train}, str(train_start), str(train_end),
                                            cohort="wf", quantile=0.30)
        except Exception as e:
            print(f"{qstart.date()} label calib failed: {str(e)[:60]}", flush=True)
            del train, test; gc.collect(); continue
        train = apply_label_thresholds(train, th)
        # WinsorStandardizer median-imputes NaN -> use the inference-side coverage gate
        cov = np.isfinite(train[FEAT].to_numpy(dtype="float32")).mean(axis=1)
        mask = (train["direction_72"].notna() & train["trendable_72"].notna()
                & (cov >= 0.80)).to_numpy()
        if mask.sum() < 2000:
            print(f"{qstart.date()} too few labelled rows ({mask.sum()})", flush=True)
            del train, test; gc.collect(); continue
        train["direction_72"] = train["direction_72"].fillna(0).astype("int8")
        train["trendable_72"] = train["trendable_72"].fillna(0).astype("int8")

        # last 20% of the training window (by time) held out for Platt calibration
        cut = train.index[mask][int(mask.sum() * 0.80)]
        fit_mask = mask & np.asarray(train.index < cut)
        cal_mask = mask & np.asarray(train.index >= cut)
        if fit_mask.sum() < 1000 or cal_mask.sum() < 300:
            print(f"{qstart.date()} split too small", flush=True)
            del train, test; gc.collect(); continue

        model = fit_hurdle_model("wf", "logistic", 72, train, FEAT, fit_mask)
        cal = train.loc[cal_mask]
        raw_up, raw_tr = model.predict_raw(cal)
        tr_lbl = cal["trendable_72"].to_numpy()
        pc_dir = PlattCalibrator.fit(raw_up[tr_lbl == 1], cal["direction_72"].to_numpy()[tr_lbl == 1])
        pc_tr = PlattCalibrator.fit(raw_tr, tr_lbl)
        n_fit = int(fit_mask.sum())
        del train, cal, raw_up, raw_tr, tr_lbl; gc.collect()

        te = test[np.isfinite(test[FEAT].to_numpy(dtype="float32")).mean(axis=1) >= 0.80]
        te = te[np.isfinite(te["net_long"]) & np.isfinite(te["net_short"]) & te["slope168"].notna()]
        del test; gc.collect()
        if len(te) < 500:
            print(f"{qstart.date()} too few scorable test rows ({len(te)})", flush=True)
            del te; gc.collect(); continue
        r_up, r_tr = model.predict_raw(te)
        state = state_from_probabilities(pc_dir.predict(r_up), pc_tr.predict(r_tr),
                                        P_TRENDABLE_MIN, DIR_PROB_MIN)
        # Also keep the RAW scores and a vendor-constant calibration. Ranking only
        # needs monotonicity, so Platt is irrelevant to the selection use case;
        # dumping raw lets the selection test avoid depending on any calibrator.
        state_vendor = state_from_probabilities(VENDOR_DIR.predict(r_up),
                                               VENDOR_TR.predict(r_tr),
                                               P_TRENDABLE_MIN, DIR_PROB_MIN)
        te = te.assign(state=state, raw_up=r_up, raw_tr=r_tr, state_vendor=state_vendor)

        # Direction accuracy on the unseen quarter, using the SAME label recipe
        # (thresholds from the training window only). This is the direct test of
        # "is the per-coin direction call reliable", separate from economics.
        te_l = apply_label_thresholds(te, th)
        emitted = te_l.state != 0
        real_trend = emitted & (te_l.trendable_72 == 1)
        ba_all = ba_trend = np.nan
        acc_all = np.nan
        if emitted.sum() > 50:
            pred = (te_l.state[emitted] > 0).to_numpy()
            truth = (te_l["future_score_72"][emitted] > 0).to_numpy()
            ok = np.isfinite(te_l["future_score_72"][emitted].to_numpy())
            pred, truth = pred[ok], truth[ok]
            if len(truth) > 50 and truth.any() and (~truth).any():
                tpr = (pred & truth).sum() / truth.sum()
                tnr = ((~pred) & (~truth)).sum() / (~truth).sum()
                ba_all = 0.5 * (tpr + tnr)
                acc_all = (pred == truth).mean()
        if real_trend.sum() > 50:
            pred = (te_l.state[real_trend] > 0).to_numpy()
            truth = (te_l["future_score_72"][real_trend] > 0).to_numpy()
            ok = np.isfinite(te_l["future_score_72"][real_trend].to_numpy())
            pred, truth = pred[ok], truth[ok]
            if len(truth) > 50 and truth.any() and (~truth).any():
                tpr = (pred & truth).sum() / truth.sum()
                tnr = ((~pred) & (~truth)).sum() / (~truth).sum()
                ba_trend = 0.5 * (tpr + tnr)

        # dump the scored OOS panel so the selection use case can be tested on
        # genuinely unseen quarters (see wf_select.py)
        te_l[["symbol", "state", "state_vendor", "raw_up", "raw_tr", "slope168",
              "net_long", "net_short", "future_score_72"]].to_pickle(
                  f"{SDIR}/{qstart.date()}.pkl")
        del te_l

        v4e = edge(te[te.state < 0], "long", "short")
        oue = edge(te[te.slope168 < 0], "long", "short")
        v4u = edge(te[te.state > 0], "short", "long")
        results.append(dict(q=str(qstart.date()), n_train=n_fit, n=len(te),
                            n_down=int((te.state < 0).sum()), n_up=int((te.state > 0).sum()),
                            v4_opp=v4e[0], v4_lo=v4e[1], v4_hi=v4e[2],
                            v4_adv=v4e[3], v4_alo=v4e[4], v4_ahi=v4e[5],
                            v4up_adv=v4u[3], v4up_alo=v4u[4],
                            ba_all=ba_all, ba_trend=ba_trend, acc_all=acc_all,
                            ou_opp=oue[0], ou_adv=oue[3], ou_alo=oue[4], ou_ahi=oue[5],
                            base_long=float(te.net_long.mean()), base_short=float(te.net_short.mean()),
                            cov=(te.state < 0).mean()))
        r = results[-1]
        ba = lambda x: "  n/a " if not np.isfinite(x) else f"{x*100:5.2f}%"
        print(f"{r['q']} n={r['n']:>6} DOWN={r['cov']*100:4.1f}% BA={ba(r['ba_all'])} "
              f"BA趋势={ba(r['ba_trend'])} | V4压LONG {pct(r['v4_opp'])} 优势 "
              f"{pct(r['v4_adv'])}[{pct(r['v4_alo'])},{pct(r['v4_ahi'])}] | V4压SHORT优势 "
              f"{pct(r['v4up_adv'])} | 我们 {pct(r['ou_adv'])} | 基线L {pct(r['base_long'])} "
              f"S {pct(r['base_short'])}", flush=True)
        pd.DataFrame(results).to_csv(f"{BASE}/walkforward.csv", index=False)
        del te, model, r_up, r_tr; gc.collect()

    df = pd.DataFrame(results)
    print("\n" + "=" * 118)
    print("滚动前向汇总 — 每个季度的数据在训练时都未见过 (前1年训练, 隔72h, 测下一季)")
    print("=" * 118)
    if df.empty:
        print("无结果"); return
    for lbl, adv, lo in (("V4 DOWN 压 LONG", "v4_adv", "v4_alo"),
                         ("V4 UP 压 SHORT", "v4up_adv", "v4up_alo"),
                         ("我们 168h 斜率压 LONG", "ou_adv", "ou_alo")):
        n = int(df[adv].notna().sum())
        print(f"  {lbl:<22} 为正 {int((df[adv]>0).sum())}/{n}  显著(CI下界>0) {int((df[lo]>0).sum())}/{n}  "
              f"均 {pct(df[adv].mean())}  中位 {pct(df[adv].median())}  最差 {pct(df[adv].min())}")
    print(f"\n  方向准确率 BA(已发状态) 均 {df.ba_all.mean()*100:.2f}%  "
          f"中位 {df.ba_all.median()*100:.2f}%  >50%的季度 {int((df.ba_all>0.5).sum())}/{int(df.ba_all.notna().sum())}  "
          f"最差 {df.ba_all.min()*100:.2f}%")
    print(f"  方向准确率 BA(真实趋势型) 均 {df.ba_trend.mean()*100:.2f}%  "
          f">50%的季度 {int((df.ba_trend>0.5).sum())}/{int(df.ba_trend.notna().sum())}")
    print(f"\n  V4被压侧(LONG)为负的季度 {int((df.v4_opp<0).sum())}/{int(df.v4_opp.notna().sum())}   "
          f"基线LONG为负的季度 {int((df.base_long<0).sum())}/{len(df)}")
    same = int(((df.v4_opp < 0) == (df.base_long < 0)).sum())
    print(f"  两者同号的季度 {same}/{len(df)} (高度一致 => V4只是跟随大盘方向, 不是选币能力)")


if __name__ == "__main__":
    main()
