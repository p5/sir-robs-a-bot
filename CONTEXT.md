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

**Request**:
A statement of desired work, its inputs, acceptance criteria, capabilities, and limits.
_Avoid_: Run as a synonym for request.

**Candidate**:
An immutable proposed output from a run, submitted for verification.
_Avoid_: Workspace as a synonym for candidate.

**Evidence**:
Recorded observations that support a task outcome for a specific candidate and execution configuration.
_Avoid_: Agent self-report as a synonym for verified evidence.

**Reconciler**:
Workflow policy that compares the desired result with observed state and determines the next action.
_Avoid_: Agent as a synonym for reconciler.

**Workload**:
The program or agent activity that a job executes.
_Avoid_: Reconciler as a synonym for workload.

**Coordination store**:
The authority for queue schedules, pending keys, failure diagnostics, and controller ownership.
Consuming services own desired state and observations.
_Avoid_: Application datastore as a synonym for the queue coordination store.

**Claim**:
Temporary permission to reconcile a resource and complete its queue work.
_Avoid_: Lock as proof that a former controller has stopped.

**Fence**:
An ownership generation that lets a state owner reject writes from a former claimant.
_Avoid_: Exactly-once execution as a synonym for fencing.

**Abandoned claim**:
An active claim recovered after its lease expires without acknowledged completion or release.
_Avoid_: Reported reconciliation failure as a synonym for controller loss.

**Redrive**:
An explicit reset of retry budgets and diagnostics that requests reconciliation of a known, unowned key.
_Avoid_: Enqueue as a synonym for a budget reset.
