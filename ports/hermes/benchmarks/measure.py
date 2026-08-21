import tiktoken
enc = tiktoken.get_encoding("o200k_base")  # GPT-4o/Claude-ish BPE proxy
def t(s): return len(enc.encode(s))

# 8 MECE-spanning prompts. Each: normal (baseline) + caveman full + ultra.
# Responses authored to caveman rules: substance preserved, fluff removed, code/errors verbatim.
cases = [
("React re-render",
 "The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object so the reference stays stable.",
 "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.",
 "Inline obj prop → new ref → re-render. `useMemo`."),
("Auth bug",
 "Sure, I'd be happy to help. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. It looks like the comparison uses a strict less-than operator when it should be less-than-or-equal, which allows tokens to be accepted one tick past their expiry.",
 "Bug in auth middleware. Token expiry check use `<` not `<=`. Allows expired token 1 tick. Fix the comparison.",
 "Auth mw: expiry `<` should be `<=`. Expired token passes 1 tick."),
("Connection pooling",
 "Connection pooling is a technique where a pool of open database connections is maintained and reused across requests, instead of opening a brand new connection for every single request. This avoids the repeated overhead of the TCP and authentication handshake, which significantly improves performance under load.",
 "Pool reuse open DB connections. No new connection per request. Skip handshake overhead. Faster under load.",
 "Pool = reuse DB conn. Skip handshake → fast under load."),
("Git rebase explain",
 "To rebase your feature branch onto the latest main, you'll first want to make sure your main branch is up to date by fetching from the remote. Then you can run git rebase main while on your feature branch. If conflicts come up, resolve them, stage the files, and continue the rebase.",
 "Update main: `git fetch`. On feature branch: `git rebase main`. Conflicts → resolve, `git add`, `git rebase --continue`.",
 "`git fetch` → `git rebase main`. Conflict → fix, `git add`, `--continue`."),
("Python perf",
 "The performance bottleneck in your function is that you're concatenating strings inside a loop using the plus operator, which creates a new string object on every iteration because strings are immutable in Python. A much more efficient approach is to append each piece to a list and then join them all at once at the end.",
 "Bottleneck: string `+` in loop creates new str each iteration (strings immutable). Append to list, `''.join()` at end.",
 "Loop `+` str = new obj/iter. Use list + `''.join()`."),
("Docker layer caching",
 "The reason your Docker build is slow is that you're copying your entire application source code before running the dependency installation step. This means any code change invalidates the cache for the install layer. The fix is to copy only your package manifest first, run the install, then copy the rest of the source.",
 "Slow build: you `COPY` all source before install, so any code change busts the install cache layer. Copy manifest first, install, then copy rest.",
 "`COPY` src before install busts cache. Manifest first → install → src."),
("SQL N+1",
 "What you're seeing is the classic N+1 query problem. Your code runs one query to fetch the list of parent records, and then for each parent it runs an additional query to fetch the related children. With a hundred parents that's a hundred and one queries. You should use a join or eager loading to fetch everything in one or two queries.",
 "Classic N+1. 1 query for parents, then 1 per parent for children. 100 parents = 101 queries. Use join / eager load → 1-2 queries.",
 "N+1: 1 + N child queries. Join/eager load → 1-2."),
("Async race",
 "The bug is a race condition. Because both async tasks read the shared counter, increment it, and write it back without any synchronization, their operations can interleave such that one increment overwrites the other. You need to guard the read-modify-write with a lock or use an atomic operation.",
 "Race condition. Both async tasks read-modify-write shared counter unsynchronized → one increment overwrites other. Guard with lock or atomic op.",
 "Race: unsynced read-mod-write on counter. Lock or atomic."),
]

print(f"{'case':<22}{'norm':>6}{'full':>6}{'ultra':>7}{'full%':>8}{'ultra%':>8}")
print("-"*57)
import statistics
fr, ur = [], []
for name, norm, full, ultra in cases:
    n, f, u = t(norm), t(full), t(ultra)
    fp, up = 100*(1-f/n), 100*(1-u/n)
    fr.append(fp); ur.append(up)
    print(f"{name:<22}{n:>6}{f:>6}{u:>7}{fp:>7.0f}%{up:>7.0f}%")
print("-"*57)
print(f"{'MEAN':<22}{'':>6}{'':>6}{'':>7}{statistics.mean(fr):>7.0f}%{statistics.mean(ur):>7.0f}%")
print(f"{'MEDIAN':<22}{'':>6}{'':>6}{'':>7}{statistics.median(fr):>7.0f}%{statistics.median(ur):>7.0f}%")
print(f"\nfull range: {min(fr):.0f}%-{max(fr):.0f}%   ultra range: {min(ur):.0f}%-{max(ur):.0f}%")
print("upstream claim: ~65-75% (README says 65%, hero says 75%)")
