#!/usr/bin/env python3
# Sample zhdata tunnel session TCP receive rates on the server, 5s apart.
import re, subprocess, time

def sample():
    out = subprocess.run(["ss", "-tin", "state", "established",
                          "( sport >= :49888 and sport <= :49919 )"],
                         capture_output=True, text=True).stdout
    if not out.strip():
        out = subprocess.run(["ss", "-tin", "state", "established"],
                             capture_output=True, text=True).stdout
    rows = {}
    cur = None
    for line in out.splitlines():
        m = re.search(r"\s\S+:(\d+)\s+\S+:\d+", line)
        if m:
            cur = m.group(1)
            rows[cur] = {"line": line}
            continue
        if cur:
            rows[cur]["line"] += " " + line
    d = {}
    for port, v in rows.items():
        s = v["line"]
        def num(k):
            mm = re.search(k + r":([0-9.]+)", s)
            return float(mm.group(1)) if mm else 0.0
        mm = re.search(r"bytes_received:(\d+)", s)
        d[port] = {
            "br": int(mm.group(1)) if mm else 0,
            "rtt": num("rtt"), "ooop": num("rcv_ooopack"),
            "snd_wnd": num("snd_wnd"), "rcv_space": num("rcv_space"),
            "retrans": num("bytes_retrans"),
        }
    return d

a = sample()
time.sleep(5)
b = sample()
rows = []
for p in b:
    if p not in a:
        continue
    delta = b[p]["br"] - a[p]["br"]
    rows.append((delta, p, b[p]))
rows.sort(reverse=True)
print(f"active sessions with rx>0.05MB/s over 5s:")
for delta, p, v in rows[:10]:
    if delta < 250000:
        continue
    print(f"  port {p}: rx={delta/5/1e6:.2f} MB/s ({delta*8/5/1e6:.1f} Mbps) rtt={v['rtt']:.0f}ms "
          f"ooop_delta={v['ooop']-a[p]['ooop']:.0f} retrans_delta={v['retrans']-a[p]['retrans']:.0f} "
          f"snd_wnd={v['snd_wnd']:.0f} rcv_space={v['rcv_space']:.0f}")
tot = sum(max(0, d) for d, _, _ in rows)
print(f"total rx over active sessions: {tot/5/1e6:.2f} MB/s = {tot*8/5/1e6:.1f} Mbps")
