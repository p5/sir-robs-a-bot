# Agent software factory

The factory supports software development and agent-powered applications.
Both use a shared execution platform.

## Language

**Factory**:
The software that accepts work, coordinates agent execution, and records results.
_Avoid_: Bot, orchestrator as a name for the whole factory.

**Agent**:
An automated worker that uses a model and tools to complete assigned work.
_Avoid_: Model as a synonym for agent.

**Run**:
One attempt to complete a request, including its jobs and results.
_Avoid_: Job as a synonym for run.

**Job**:
A unit of work that the execution platform assigns to a worker.
_Avoid_: Run as a synonym for job.

**Execution platform**:
The shared capability that executes agent jobs for the factory and its applications.
_Avoid_: Factory as a synonym for execution platform.
