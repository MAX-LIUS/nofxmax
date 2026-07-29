"""拉取 OI / 多空比 metrics(日档,5min 粒度),落盘前先聚合到小时。

**对齐口径(防前视的关键)**:第 i 根 1h bar 的取值 = 落在 [T_i, T_i+1h) 内的
**最后一条** 5min 观测。该值在第 i 根收盘时已经可得,而决策就发生在第 i 根收盘、
成交在 i+1 开盘,所以不含未来信息。用 resample(label=left, closed=left).last() 实现。
"""
import io,os,sys,zipfile,urllib.request,urllib.error,time,threading,queue
import pandas as pd
OUT="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/metrics"
KL="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/klines"
U="https://data.binance.vision/data/futures/um/daily/metrics/{s}/{s}-metrics-{d}.zip"
START,END="2024-06-01","2026-07-27"
COLS=["sum_open_interest","sum_open_interest_value","count_toptrader_long_short_ratio",
      "sum_toptrader_long_short_ratio","count_long_short_ratio","sum_taker_long_short_vol_ratio"]
syms=sorted({f.split('-',1)[0] for f in os.listdir(KL) if f.endswith('.csv')})
days=[d.strftime("%Y-%m-%d") for d in pd.date_range(START,END,freq="D")]
os.makedirs(OUT,exist_ok=True)
lock=threading.Lock(); done=[0]

def fetch_day(s,d):
    try:
        raw=urllib.request.urlopen(U.format(s=s,d=d),timeout=60).read()
        z=zipfile.ZipFile(io.BytesIO(raw))
        return pd.read_csv(io.BytesIO(z.read(z.namelist()[0])))
    except urllib.error.HTTPError as e:
        return None
    except Exception:
        return None

def worker(q):
    while True:
        try: s=q.get_nowait()
        except queue.Empty: return
        p=f"{OUT}/{s}.pkl"
        if os.path.exists(p):
            with lock: done[0]+=1; print(f"[{done[0]}/{len(syms)}] {s} skip",flush=True)
            q.task_done(); continue
        parts=[]
        for d in days:
            df=fetch_day(s,d)
            if df is not None and len(df): parts.append(df)
        if parts:
            a=pd.concat(parts,ignore_index=True)
            a["ts"]=pd.to_datetime(a["create_time"],utc=True)
            a=a.drop_duplicates(subset="ts").set_index("ts").sort_index()
            keep=[c for c in COLS if c in a.columns]
            h=a[keep].resample("1h",label="left",closed="left").last()
            h.to_pickle(p)
            with lock: done[0]+=1; print(f"[{done[0]}/{len(syms)}] {s} {len(h)} hourly rows",flush=True)
        else:
            with lock: done[0]+=1; print(f"[{done[0]}/{len(syms)}] {s} NO DATA",flush=True)
        q.task_done()

q=queue.Queue()
for s in syms: q.put(s)
ths=[threading.Thread(target=worker,args=(q,),daemon=True) for _ in range(4)]
[t.start() for t in ths]
[t.join() for t in ths]
print("METRICS DONE")
