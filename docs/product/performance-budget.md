# Performance budget

These are prototype engineering budgets, initially measured on the current Apple Silicon macOS Tahoe 26.6.1 machine with 32 GiB memory, one connected Development Host, and no active client traffic. After the first representative prototype, they may be adjusted once before becoming release gates or public claims.

| Measure | Budget |
|---|---:|
| Local manager plus system `ssh`, idle RSS | ≤ 40 MiB |
| macOS desktop plus manager plus system `ssh`, idle RSS | ≤ 80 MiB |
| Combined idle CPU | ≤ 0.5% |
| New Remote Listener detection latency, p95 | ≤ 3 s |
| Warm CLI response latency, p95 | ≤ 50 ms |
| Forward throughput relative to direct `ssh -L` | ≥ 90% |
| Additional local connection-establishment latency | ≤ 2 ms |
| Remote scanner average CPU | ≤ 0.5% |

Benchmarks must record hardware, OS, Go version, OpenSSH version, network conditions, listener count, active-forward count, and sampling duration. Idle measurements include child processes owned by the product. Public wording should remain “near-zero idle overhead and near-native SSH forwarding performance” until repeated measurements justify stronger claims.
