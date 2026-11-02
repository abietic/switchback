# User-Scoped Interactive WorkerSession

WorkerSession is a user-level interactive execution session with a 48-hour idle timeout. Before that timeout, follow-up interactions resume the existing session by default like a normal Coding Agent; a user may open its detail view and submit prompt, queue, interrupt, or steer AgentTurns directly. Valid activity renews the deadline without extending the separate 14-day absolute lifetime, while `waiting_user` pauses only the idle countdown. A completed WorkerRun produces a stage result but leaves WorkerSession interactive. This trades higher runtime lifecycle complexity for continuity of agent context.

## Status

Accepted as draft baseline.
