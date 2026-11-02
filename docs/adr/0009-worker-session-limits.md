# WorkerSession Lifetime And Concurrency Limits

WorkerSession has a 48-hour idle timeout, an absolute maximum lifetime of 14 days, and a default concurrency limit of 10 active sessions per user. Valid user interaction renews the idle deadline, but no renewal may extend the 14-day absolute deadline. `waiting_user` pauses the idle countdown so approval latency does not reclaim the runtime, while the absolute deadline continues. A session exceeding either deadline must drain trace and artifacts before its runtime is reclaimed.

## Status

Accepted as draft baseline.
