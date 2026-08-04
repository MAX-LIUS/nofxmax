#!/usr/bin/env python3
"""Pin the parser against the two hand-verified trades before trusting it on 1922.
If these two don't come out right, nothing downstream means anything."""
import sqlite3, sys
from extract import parse_symbol_block, extract_geometry

con = sqlite3.connect("file:/opt/webstack/nofx/data/data.db?mode=ro", uri=True)
cur = con.cursor()

cases = [
    # (decision_record_id, symbol, entry, is_long, label, expected_opp_atr_approx)
    (25664, "SKHYNIXUSDT", 1064.91, False, "GPT SHORT 空进支撑", 0.4),
    (25195, "CLUSDT",        81.70,  True, "claude LONG 多头上方有空间", 2.5),
]
fail = 0
for rid, sym, entry, is_long, label, want in cases:
    cur.execute("SELECT input_prompt FROM decision_records WHERE id=?", (rid,))
    prompt = cur.fetchone()[0]
    blk = parse_symbol_block(prompt, sym)
    if not blk:
        print(f"FAIL {label}: symbol block not found"); fail += 1; continue
    g = extract_geometry(blk, entry, is_long)
    if not g:
        print(f"FAIL {label}: geometry None"); fail += 1; continue
    print(f"{label}")
    print(f"  opp_atr={g['opp_atr']:.3f} (期望≈{want}) grade={g['opp_grade']} "
          f"conf={g['opp_conf']} touches={g['opp_touches']}")
    print(f"  dense_atr={g['dense_atr']} period_atr={g['period_atr']} "
          f"range_pos={g['range_pos']} atr_price={g['atr_price']:.4f}")
    if g["opp_atr"] is None or abs(g["opp_atr"] - want) > 0.35:
        print(f"  ^^ FAIL: opp_atr 偏离期望太远"); fail += 1
print("SELFTEST", "FAIL" if fail else "PASS")
if fail:
    sys.exit(1)


def test_causal_join_picks_right_record():
    """Pin the DB join, not just the parser.

    The parser passing on hand-pasted text proves nothing about which record
    the join selects. This asserts end-to-end: for the two hand-verified
    trades, extraction must reproduce the hand-verified opp_atr.
    """
    import json, subprocess, os
    here = os.path.dirname(os.path.abspath(__file__))
    subprocess.run(["/usr/bin/python3", os.path.join(here, "extract.py")],
                   check=True, cwd=here)
    rows = {}
    with open(os.path.join(here, "trades.jsonl")) as fh:
        for line in fh:
            r = json.loads(line)
            rows[r["id"]] = r
    # Tolerance 0.1: loose enough for the median-ATR recovery to differ from
    # the prompt's one-decimal rounding (CLUSDT 2.444 vs 2.5), tight enough to
    # still reject the previous-cycle record, which gave 2.300 and 0.752.
    for pid, want in ((1898, 2.500), (1926, 0.438)):
        got = rows[pid]["opp_atr"]
        assert got is not None and abs(got - want) < 0.1, \
            f"pos {pid}: join resolved to wrong decision record, " \
            f"opp_atr={got} want={want}"
        print(f"  join OK pos={pid} opp_atr={got:.3f}")


test_causal_join_picks_right_record()
print("JOIN SELFTEST PASS")
