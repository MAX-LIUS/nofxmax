"""候选 2:资金费率。8h 结算一次,是持仓成本/多空拥挤度的直接读数,不在价格路径里。

对齐口径(防前视):资金费的 calc_time 是结算时刻。第 i 根 bar 只能用
**calc_time <= 该 bar 收盘**的最后一条 → 用 reindex(ffill) 实现,
且刻意**不做**任何居中/未来平滑。

构造(全部乘 side 的版本带 _dir 后缀,让多空可比):
  fund_last_dir   最近一条资金费 × side(>0 = 持仓方向在付钱)
  fund_ma3_dir    最近 3 条(24h)均值 × side
  fund_chg_dir    最近一条 − 上一条,× side(拥挤度变化)
  fund_abs        |最近一条|(不带方向,纯拥挤度强度)
  fund_z_dir      最近一条相对过去 30 天的 z 分数 × side
"""
import os, numpy as np, pandas as pd
import pmgate as P, screen as S

FD="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/funding"
syms=sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith('.pkl'))
last={},; last={}; ma3={}; chg={}; ab={}; z={}
n_ok=0
for s in syms:
    p=f"{FD}/{s}.csv"
    if not os.path.exists(p): continue
    d=pd.read_csv(p)
    d["ts"]=pd.to_datetime(d.calc_time.astype("int64"),unit="ms",utc=True)
    d=d.drop_duplicates(subset="ts").set_index("ts").sort_index()
    r=d.last_funding_rate.astype("float64")
    last[s]=r
    ma3[s]=r.rolling(3).mean()
    chg[s]=r.diff()
    ab[s]=r.abs()
    mu=r.rolling(90).mean(); sd=r.rolling(90).std()
    z[s]=(r-mu)/sd.replace(0,np.nan)
    n_ok+=1
print(f"资金费可用 {n_ok}/{len(syms)} 币; 样例 {list(last)[0]} {len(last[list(last)[0]])} 条")
extra={"fund_last_dir":last,"fund_ma3_dir":ma3,"fund_chg_dir":chg,
       "fund_abs":ab,"fund_z_dir":z}
df=S.decision_points(extra_cols=extra)
print(f"决策样本 {len(df)}")
S.run("资金费率 (last/ma3/chg/abs/z, 方向归一)", df,
      ["fund_last_dir","fund_ma3_dir","fund_chg_dir","fund_abs","fund_z_dir"])
