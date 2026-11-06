# JVM-aware memory

A JVM sizes its maximum heap when it starts: either from a fixed `-Xmx`, or as a share of the container's memory
limit (`MaxRAMPercentage`, 25 % by default). Memory below what the heap may grow to gets the container OOM-killed,
or the JVM stalls in garbage collection. So Kerf treats the memory of a JVM container differently.

## Detection

The agent detects a JVM container from any of these, and reports its heap settings to the hub:

| Detected by | What it looks at |
|---|---|
| `image` | An image name with a token such as `java`, `openjdk`, `temurin`, `corretto`, `zulu`, `graalvm` or `jre` |
| `command` | A command that runs `java`, or passes a heap option |
| `env` | `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS`, `JAVA_OPTS` or `_JAVA_OPTIONS` |

It reads `-Xmx`, `-XX:MaxHeapSize`, `-Xms`, `-XX:InitialHeapSize`, `-XX:MaxRAMPercentage`, `-XX:MaxRAMFraction` and
`-XX:MaxRAM` from those variables and from the command line. Later sources win, as they do in the JVM. An option
variable set from a ConfigMap or Secret reference is not read and is reported as unresolved. Without an agent
report, the hub falls back to the image name and assumes the default `MaxRAMPercentage` of 25.

Detection is on by default. Turn it off with `spec.jvm.detect: false` in a policy.

## Heap-aware sizing

The hub adds a floor to the usual memory recommendation, so the JVM keeps the heap it needs:

- **Percentage or default heap.** The maximum heap after the change is the percentage times the new memory limit.
  The hub raises the memory until that heap covers the heap need. The heap need is the observed peak working set,
  capped at the current maximum heap.
- **Initial heap.** With `-Xms`, the memory request is at least the initial heap plus a non-heap estimate
  (metaspace, code cache, threads), which is the larger of `64Mi` and 10 % of the heap.
- **Memory limit.** With `memory.limitEqualsRequest` (the default), the limit follows the request, so the heap
  follows too.

`GET /v1/workloads/{id}` returns the JVM explanation of each container: the heap sizing, the maximum heap before
and after, the heap need and the floor ([hub API](../reference/api/README.md)).

## Fixed-heap containers are audit-only

A container with a fixed maximum heap (`-Xmx` or `-XX:MaxHeapSize`) cannot use more memory than its heap, and less
memory needs a smaller `-Xmx`, which Kerf cannot change. So its memory stays in audit:

- The hub flags the recommendation `jvm-fixed-heap` and shows the floor: the heap plus the non-heap estimate.
- The agent refuses a memory change on such a container with the reason `JVMFixedHeap`. It checks the container
  itself, in case the hub's flag is missing. Other resources, such as CPU, change as usual.
- To save memory on that container, lower `-Xmx` together with the memory, in your manifest.

## In-place changes need a restart policy

A JVM binds its heap at start-up, so a new memory size takes effect only after a restart. For a container that looks
like a JVM or Python process, an `in-place` memory change is blocked with the reason `RestartPolicyRequired` unless
the container sets `resizePolicy: RestartContainer` for memory:

```yaml
containers:
  - name: app
    resizePolicy:
      - resourceName: memory
        restartPolicy: RestartContainer
```

Or use the `rollout` strategy, which restarts the pods anyway ([apply strategies](apply-strategies.md)). On a
single-replica workload, a restart is blocked unless you accept the disruption
([single replicas](apply-strategies.md#single-replicas-and-ondelete-statefulsets)). This check, like the live
fixed-heap check, is skipped when `spec.jvm.detect` is `false`.

## Related

- [Policies](policies.md)
- [Guardrails](guardrails.md)
- [Rollback](rollback.md): an OOM kill after a memory change rolls it back.
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
