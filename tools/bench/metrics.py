"""Small development-only monitor for the isolated service. Commands are JSON lines."""
import ctypes
import json
import os
import sys
import threading
import time
from pathlib import Path

pid=int(sys.argv[1]); root=Path(sys.argv[2])
if os.name=="nt":
    from ctypes import wintypes as W
    kernel=ctypes.WinDLL("kernel32",use_last_error=True);ps=ctypes.WinDLL("psapi",use_last_error=True)
    SIZE=ctypes.c_size_t
    class Counters(ctypes.Structure):
        _fields_=[("cb",W.DWORD),("faults",W.DWORD)]+[(n,SIZE) for n in ("peak","rss","peakPaged","paged","peakNonPaged","nonPaged","pageFile","peakPageFile","private")]
    kernel.OpenProcess.restype=W.HANDLE
    handle=kernel.OpenProcess(0x0410,False,pid)
    if not handle: raise ctypes.WinError(ctypes.get_last_error())
    def read_handle(handle):
        info=Counters();info.cb=ctypes.sizeof(info); count=W.DWORD()
        if not ps.GetProcessMemoryInfo(W.HANDLE(handle),ctypes.byref(info),info.cb) or not kernel.GetProcessHandleCount(W.HANDLE(handle),ctypes.byref(count)): raise ctypes.WinError(ctypes.get_last_error())
        return {"rssBytes":info.rss,"processPeakRSSBytes":info.peak,"handles":count.value}
    def sample():
        return read_handle(handle)
    def read_pid(other):
        current=kernel.OpenProcess(0x0410,False,other)
        if not current: return None
        try: return read_handle(current)
        finally: kernel.CloseHandle(W.HANDLE(current))
else:
    def read_pid(pid):
        status=dict(line.split(":",1) for line in Path(f"/proc/{pid}/status").read_text().splitlines() if ":" in line)
        return {"rssBytes":int(status["VmRSS"].split()[0])*1024,"processPeakRSSBytes":int(status["VmHWM"].split()[0])*1024,"handles":len(os.listdir(f"/proc/{pid}/fd"))}

    def sample(): return read_pid(pid)

def browser_stats(pids):
    rss=0;handles=0;count=0
    for pid in set(pids):
        try: row=read_pid(pid)
        except (OSError,FileNotFoundError): continue
        if row is None:continue
        rss+=row["rssBytes"];handles+=row["handles"];count+=1
    if not count:raise RuntimeError("unable to sample browser processes")
    return {"reportedProcesses":len(set(pids)),"sampledProcesses":count,"sumWorkingSetBytes":rss,"sumHandles":handles}

def disk():
    total=0
    for base,dirs,files in os.walk(root):
        for name in files:
            try: total+=(Path(base)/name).stat().st_size
            except FileNotFoundError: pass
    return total

lock=threading.Lock();stop=threading.Event();peak={};track_disk=False;failure=None
def take():
    current=sample()
    if track_disk: current["dataBytes"]=disk()
    for k,v in current.items():peak[k]=max(peak.get(k,0),v)
    return current
def watch():
    global failure
    while not stop.wait(.2):
        try:
            with lock:take()
        except Exception as e:
            failure=str(e);return
thread=threading.Thread(target=watch,daemon=True);thread.start()
try:
    print(json.dumps({"ready":True}),flush=True)
    for line in sys.stdin:
        cmd=json.loads(line)
        with lock:
            if failure:raise RuntimeError(failure)
            if cmd["action"]=="start":
                peak.clear();track_disk=cmd.get("disk",False)
            current=take()
            result={"current":current,"peak":dict(peak)}
            if cmd["action"]=="browser":result["browser"]=browser_stats(cmd["pids"])
            print(json.dumps(result),flush=True)
finally:
    stop.set();thread.join()
    if os.name=="nt": kernel.CloseHandle(W.HANDLE(handle))
