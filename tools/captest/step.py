#!/usr/bin/env python3
# usage: step.py RPS DUR LABEL [extra load args]
import sys, subprocess, time, json, os, threading, urllib.request
rps, dur, label = sys.argv[1], sys.argv[2], sys.argv[3]; extra = sys.argv[4:]
T = "10.201.0.11:8317,10.201.0.12:8317,10.201.0.13:8317,10.201.0.14:8317"
T = os.environ.get("TARGETS", T)
import pwd
PGUID = pwd.getpwnam('postgres').pw_uid
HZ = os.sysconf("SC_CLK_TCK")
def procs():
    out = {}
    for pid in os.listdir("/proc"):
        if not pid.isdigit(): continue
        try:
            cmd = open(f"/proc/{pid}/cmdline","rb").read().split(b"\0")[0].decode()
            st = open(f"/proc/{pid}/stat").read().rsplit(")",1)[1].split()
        except Exception: continue
        name = None
        if cmd.endswith("/cap/bin/home"): name="home"
        elif cmd.endswith("/cap/bin/proxy"): name="proxy"
        elif cmd.endswith("/cap/bin/mock"): name="mock"
        elif cmd.endswith("/cap/bin/load"): name="load"
        elif "postgres" in cmd and os.stat(f"/proc/{pid}").st_uid==PGUID: name="postgres"
        if name:
            out[pid]=(name, int(st[11])+int(st[12])+(int(st[13])+int(st[14]) if name=='postgres' else 0), int(st[21])*os.sysconf("SC_PAGE_SIZE"))
    return out
def psql(q):
    return subprocess.run(["psql","-h","127.0.0.1","-p","56432","-U","postgres","-d","home","-At","-c",q],capture_output=True,text=True).stdout.strip()
def goroutines():
    r=[]
    for p in (6060,6061):
        try: r.append(int(urllib.request.urlopen(f"http://127.0.0.1:{p}/debug/pprof/goroutine?debug=1",timeout=5).readline().split()[3]))
        except Exception: r.append(-1)
    return r
peak = {"pgconn":0,"gor":[0,0]}
stop=False
def sampler():
    while not stop:
        try:
            peak["pgconn"]=max(peak["pgconn"],int(psql("select count(*) from pg_stat_activity where datname='home'") or 0))
            g=goroutines(); peak["gor"]=[max(a,b) for a,b in zip(peak["gor"],g)]
        except Exception: pass
        time.sleep(2)
p0=procs(); t0=time.time()
c0=int(psql("select xact_commit from pg_stat_database where datname='home'")); s0=int(psql("select sessions from pg_stat_database where datname='home'")); w0=psql("select pg_current_wal_lsn()")
a0=psql("select outcome||'='||count(*) from request_affinity group by outcome order by outcome")
th=threading.Thread(target=sampler,daemon=True); th.start()
r=subprocess.run(["bash","-c",f"ulimit -n 500000; /workspace/cap/bin/load -rps {rps} -dur {dur} -targets {T} -label {label} "+" ".join(extra)],capture_output=True,text=True)
stop=True; time.sleep(3)
p1=procs(); el=time.time()-t0
c1=int(psql("select xact_commit from pg_stat_database where datname='home'")); s1=int(psql("select sessions from pg_stat_database where datname='home'")); w1=psql(f"select pg_wal_lsn_diff(pg_current_wal_lsn(),'{w0}')")
cpu={}; rss={}
for pid,(n,t,m) in p1.items():
    prev=p0.get(pid,(n,0,0))[1]
    cpu[n]=round(cpu.get(n,0)+(t-prev)/HZ/el,2); rss[n]=rss.get(n,0)+m//(1<<20)
try: res=json.loads(r.stdout.strip().splitlines()[-1])
except Exception: res={"raw":r.stdout[-500:]+r.stderr[-500:]}
res.update({"cpu_cores":cpu,"rss_mb":rss,"pg_commits_per_s":round((c1-c0)/el),"pg_wal_mb":round(float(w1 or 0)/1e6,1),"pg_peak_conns":peak["pgconn"],"pg_new_sessions_per_s":round((s1-s0)/el,1),"home_peak_goroutines":peak["gor"],"elapsed_s":round(el)})
res["affinity_before"]=a0; res["affinity_after"]=psql("select outcome||'='||count(*) from request_affinity group by outcome order by outcome")
print(json.dumps(res)); open("/workspace/cap/results.jsonl","a").write(json.dumps(res)+"\n")
