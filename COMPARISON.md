# CVMN/Caveman Skill Rewrite

This is a rewrite of the caveman skill. Rewrite was done via Claude Sonnet 4.6 Extended in the web app and consolidated via manual reduction and word substitution. Revision is codenamed CVMN in the comparison. Benchmark was done in opencode, via OpenCode Zen's Big Pickle model. 

Also, run.py was vibe-coded w/ Big Pickle.

| Task | Normal | Caveman | CVMN | Caveman Saved | CVMN Saved |
|------|-------:|--------:|-----:|-------------:|----------:|
| Explain React re-render bug | 731 | 305 | 251 | 58% | 66% |
| Fix auth middleware token expiry | 1258 | 440 | 336 | 65% | 73% |
| Set up PostgreSQL connection pool | 1858 | 618 | 830 | 67% | 55% |
| Explain git rebase vs merge | 810 | 530 | 489 | 35% | 40% |
| Refactor callback to async/await | 658 | 319 | 249 | 52% | 62% |
| Architecture: microservices vs monolith | 1366 | 664 | 758 | 51% | 45% |
| Review PR for security issues | 734 | 658 | 325 | 10% | 56% |
| Docker multi-stage build | 2441 | 661 | 661 | 73% | 73% |
| Debug PostgreSQL race condition | 1157 | 641 | 523 | 45% | 55% |
| Implement React error boundary | 3325 | 777 | 747 | 77% | 78% |
| **Average** | **1434** | **561** | **517** | **53%** | **60%** |

Caveman savings: 10%–77% 
CVMN savings: 40%–78%

Needs further testing in more models, but tokens are expensive lmao
