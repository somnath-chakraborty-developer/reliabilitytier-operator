# ReliabilityTier Operator

A small Kubernetes operator built while studying controller/operator internals
for a Senior EM (SRE) interview loop focused on platform-engineering depth.

## What it does

Defines a `ReliabilityTier` custom resource. A user creates one per namespace,
declaring a tier:

```yaml
apiVersion: platform.platform.example.com/v1
kind: ReliabilityTier
metadata:
  name: demo-tier
  namespace: demo-team
spec:
  tier: Silver   # Bronze | Silver | Gold
```

The controller reconciles that into two enforced objects in the same namespace:

| Tier   | CPU (req/limit) | Memory (req/limit) | PDB minAvailable |
|--------|------------------|----------------------|-------------------|
| Bronze | 2 / 4            | 4Gi / 8Gi            | 0                 |
| Silver | 8 / 16           | 16Gi / 32Gi          | 1                 |
| Gold   | 32 / 64          | 64Gi / 128Gi         | 50%               |

- A `ResourceQuota` capping the namespace's resource consumption at the tier's level.
- A `PodDisruptionBudget` protecting the tier's minimum availability during voluntary disruptions (node drains, rolling upgrades).

Both are labeled with an `ownerReference` back to the `ReliabilityTier`, so deleting
the `ReliabilityTier` automatically garbage-collects both child objects — no
explicit cleanup code required.

## What it demonstrates

- **CRD schema validation** — `spec.tier` is constrained by an OpenAPI enum
  (`Bronze;Silver;Gold`) enforced by the API server at admission time, before
  the controller ever sees an invalid value.
- **The reconcile loop pattern** — level-triggered, idempotent: `Reconcile`
  always re-reads current state and re-derives the desired ResourceQuota/PDB,
  rather than reacting to the specific event that woke it up.
- **Finalizers** — a finalizer is added on creation and removed only after the
  object is confirmed to be safe to delete, turning `kubectl delete` into a
  two-phase operation the controller controls.
- **Owner references and garbage collection** — child objects are never
  explicitly deleted by the controller; Kubernetes' garbage collector removes
  them once the owning `ReliabilityTier` is actually deleted.
- **Status conditions** — the controller reports back a standard `Ready`
  condition, the same pattern `kubectl wait` relies on for built-in resources.

Built with [kubebuilder](https://book.kubebuilder.io/) and
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).

## Running it locally

Requires a running cluster (tested against [kind](https://kind.sigs.k8s.io/)).

```bash
make install   # apply the CRD
make run       # run the manager locally against your current kubeconfig
```

In another terminal:

```bash
kubectl create namespace demo-team
kubectl apply -f - <<'YAML'
apiVersion: platform.platform.example.com/v1
kind: ReliabilityTier
metadata:
  name: demo-tier
  namespace: demo-team
spec:
  tier: Silver
YAML

kubectl get resourcequota,pdb -n demo-team
kubectl get reliabilitytier demo-tier -n demo-team -o yaml
```
