# Painpoints krel solves that k9s doesn't

k9s = fast resource browser: list, logs, exec, edit, per-namespace. Strong at
"show me the object." Weak at "why is this object related to that one" and
"what breaks if I touch this." krel's job is the second half.

## Gaps (not covered by k9s today)

1. **Cross-namespace relations.** k9s is namespace-scoped per view. No way to
   see that a NetworkPolicy in `ns-a` selects pods that call a Service in
   `ns-b`, or that a Secret is referenced by ServiceAccounts across
   namespaces (imagePullSecrets, cross-ns RBAC). krel's graph already spans
   objects; it currently loads one namespace at a time — needs multi-ns
   graph build + edges that cross namespace boundaries.

2. **Blast radius / impact.** No tool answers "if I delete/restart/scale this
   object, what else is affected?" k9s shows the object; it doesn't walk
   reverse dependencies (Service → all consuming Ingresses/Routes/HPAs,
   ConfigMap → all Pods mounting it, PVC → all StatefulSets). krel has the
   graph to compute this — needs a reverse-edge walk + a dedicated view.

3. **Root-cause chains for failures.** CrashLoopBackOff in k9s means: open
   pod, read events, open logs, guess. krel already detects some problems
   (missing refs, zero-selector Services, unbound PVCs) but doesn't chain
   them — e.g. Pod crash ← ConfigMap key removed ← ConfigMap last-changed.
   Needs the problem detector to walk the graph one hop from a failing pod
   and surface the most likely upstream cause, not just the symptom.

## UX fixes shipped

- Log/detail split was 1/3 top, 2/3 log — flipped to 50/50 so logs get more
  room without shrinking context panel to nothing.
- Details view printed `apiVersion` and `uid` — pure noise for relationship
  work, dropped.
- Resource list was two lines/item with a spacer line — tightened to compact
  k9s-style rows.
- Logs pane was not focusable/scrollable — now a tab-able pane with j/k or
  arrow scroll, `G` to jump back to live tail (autoscroll), and `/` full-text
  search with `n`/`N` to step between matches. Log lines no longer repeat the
  pod/container name on every line; the leading timestamp renders in its own
  color instead.
- Single Summary panel (status + pods + relations + problems + events all
  mixed together) split into a 4-pane layout: resource list, a Status pane
  (health, problems, recent events, env values), a Relations pane (Services,
  ConfigMaps, Secrets, ServiceAccounts, PVCs — `j`/`k`/`enter` to open one and
  see its values), and an Owner Chain pane.
- Top crumb now shows `config: <kubeconfig> ctx: <context> ns: <namespace>`
  instead of just context/namespace, matching k9s' style.
- Logs moved out of the permanent grid: `l` opens a fullscreen log view for
  the selected resource (k9s-style), `esc` returns to the 4-pane layout. Log
  scroll direction fixed — `k`/`up` moves into history, `j`/`down` moves back
  toward the live tail, `G` resumes following.
- The freed pane now shows the Owner Chain: `metadata.ownerReferences`
  walked generically (Pod -> ReplicaSet -> Deployment, Job -> CronJob, ...),
  extended with the OLM Subscription -> InstallPlan -> CSV chain when those
  objects are loaded, plus a `managed-by:` line when ArgoCD, Flux, or Helm
  labels are present anywhere in the chain. `j`/`k`/`enter` to jump, same as
  Relations. Subscription/InstallPlan/ClusterServiceVersion are now fetched
  by the snapshot loader (best-effort — clusters without OLM's CRDs just get
  an empty OLM segment, no load error), so the OLM chain populates on
  OpenShift/OLM clusters.
- ArgoCD `Application` is now a real chain node too, not just the
  `managed-by:` label line — fetched best-effort (same benign-skip as OLM on
  clusters without ArgoCD's CRDs) and linked via the
  `argocd.argoproj.io/instance` label every object it manages already
  carries. Shows as `application: <name> (sync:... health:...)` prepended
  to the chain.

## Non-goals (unchanged)

Not a general dashboard, not a resource editor. exec and port-forward are
planned (see roadmap) so krel can double as a daily-driver terminal tool —
that's an attach/stream capability, not resource mutation.
