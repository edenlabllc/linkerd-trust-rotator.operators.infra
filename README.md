# Linkerd Trust Rotator Operator

[![Release](https://img.shields.io/github/v/release/edenlabllc/linkerd-trust-rotator.operators.infra.operators.infra.svg?style=for-the-badge)](https://github.com/edenlabllc/linkerd-trust-rotator.operators.infra.operators.infra/releases/latest)
[![Software License](https://img.shields.io/github/license/edenlabllc/linkerd-trust-rotator.operators.infra.operators.infra.svg?style=for-the-badge)](LICENSE)
[![Powered By: Edenlab](https://img.shields.io/badge/powered%20by-edenlab-8A2BE2.svg?style=for-the-badge)](https://edenlab.io)

The Linkerd Trust Rotator Operator automates the **zero-downtime** rotation
of [Linkerd](https://linkerd.io/2.14/overview/) [trust anchors](https://linkerd.io/2.14/tasks/generate-certificates/)
(root [CA](https://en.wikipedia.org/wiki/Certificate_authority) entities) and the propagation of new certificates
across
both [control and data planes](https://linkerd.io/2.14/reference/architecture/). It ensures seamless **certificate
renewal** without manual restarts or service disruption.

The operator **continuously inspects** the Linkerd trust bundles, detects changes, and coordinates the rotation process
in a controlled, verifiable manner.

## Description

In standard Linkerd setups, the trust anchor (root CA) must be **rotated periodically** to maintain cluster security.
Manual
rotation requires **careful coordination** between multiple Secrets, ConfigMaps, and workload restarts.

The Linkerd Trust Rotator Operator replaces this manual procedure with an **automated**, deterministic process that:

- **Verifies** the integrity of trust bundles.
- **Synchronizes** `trust-anchor` and `previous-trust-anchor` Secrets.
- **Orchestrates** a phased restart of control-plane and data-plane workloads.
- **Validates** connectivity and proxy health through
  automated [linkerd check](https://linkerd.io/2.14/reference/cli/check/) jobs.

The operator is designed for environments where continuous availability and **minimal human intervention** are required.

## Key Features

- **Automated CA rotation:** Detects trust anchor divergence and initiates rotation automatically.
- **Zero-downtime rollout:** Sequentially restarts workloads with annotation-based selection and resumable progress
  tracking.
- **Safe verification:** Runs `linkerd check --proxy` jobs during rollout to confirm successful proxy reloads.
- **Configurable protection policy:** Supports delay windows, retry limits, and re-trigger after cleanup.
- **Status observability:** Provides detailed `.status` phase, progress, retries, and trust-bundle fingerprints.

## Component Requirements

- **cert-manager:** [v1.14](https://github.com/cert-manager/cert-manager/releases/tag/v1.14.0) or newer – provides CA
  and issuer management
- **trust-manager:** [v0.18](https://github.com/cert-manager/trust-manager/releases/tag/v0.18.0) or newer – distributes
  public trust bundles to ConfigMaps

## Custom Resource Specification

The operator is configured via the `LinkerdTrustRotation` custom resource.

The specification fields are:

| Section        | Description                                                        |
|----------------|--------------------------------------------------------------------|
| **linkerd**    | Defines trust-anchor resources, ConfigMap, and namespace.          |
| **trigger**    | Controls when the rotation starts (on ConfigMap or Secret change). |
| **rollout**    | Selects workloads and strategies for restarts.                     |
| **protection** | Defines safety windows, retry limits, and validation jobs.         |
| **dryRun**     | Enables simulation mode without applying changes.                  |

See the [`sample`](./config/samples/trust-anchor_v1alpha1_linkerdtrustrotation.yaml) for more details.

## Rotation Lifecycle

The rotation process consists of several controlled phases:

1. **Inspection:** Load and parse trust bundle from `linkerd-identity-trust-roots` ConfigMap.
2. **Secret validation:** Verify existence of current and previous trust-anchor Secrets; bootstrap previous if missing.
3. **Control-plane restart:** Sequentially restart all Linkerd control-plane Deployments.
4. **Data-plane rollout:** Restart workloads (Deployments, StatefulSets, DaemonSets, and CRs) annotated with
   `linkerd.io/inject=enabled`.
5. **Verification:** Launch `linkerd check` jobs via `ServiceAccount linkerd-check` to validate proxy readiness.
6. **Cleanup and hold:** Optionally re-trigger rollout after cleanup of old secrets and apply a safety delay.

Each phase updates the CR status, allowing full observability and safe resume on controller restart.

## Architecture

The operator follows a modular, layered architecture:

```text
┌──────────────────────────────────────────┐
│               Reconciler                 │
│  Watches LinkerdTrustRotation CRs        │
│  and orchestrates the full lifecycle     │
└──────────────────────────────────────────┘
              │
              ▼
┌──────────────────────────────────────────┐
│              Secret Manager              │
│  - Loads and validates trust-anchor      │
│  - Bootstraps previous secret if missing │
│  - Computes SHA256 fingerprints          │
└──────────────────────────────────────────┘
              │
              ▼
┌──────────────────────────────────────────┐
│             ConfigMap Manager            │
│  - Loads Linkerd trust-roots bundle      │
│  - Detects overlap (bundle state)        │
│  - Triggers rotation condition           │
└──────────────────────────────────────────┘
              │
              ▼
┌──────────────────────────────────────────┐
│             Rollout Manager              │
│  - Restarts control-plane first          │
│  - Then restarts data-plane workloads    │
│  - Supports resumable progress, retries  │
│  - Validates results via `linkerd check` │
└──────────────────────────────────────────┘
              │
              ▼
┌──────────────────────────────────────────┐
│              Status Manager              │
│  - Tracks .status.phase, trust info,     │
│    rollout cursor, retries, progress     │
│  - Patches CR status atomically          │
└──────────────────────────────────────────┘
```

## Status Fields

The operator updates `.status` with structured progress and diagnostic information.

| Field                              | Description                                                                      |
|------------------------------------|----------------------------------------------------------------------------------|
| **phase**                          | Current phase of rotation (e.g., `Inspecting`, `RollingDataPlane`, `Succeeded`). |
| **trust.bundleState**              | `single` or `overlap` – number of CAs in trust bundle.                           |
| **trust.currentFP / previousFP**   | SHA-256 fingerprints of trust-anchor Secrets.                                    |
| **progress.dataPlanePercent**      | Percentage of workloads updated and ready.                                       |
| **retries.count / lastError**      | Retry counter and last encountered error.                                        |
| **cursor.planHash / next / total** | Internal rollout plan tracking for resumable execution.                          |

See the `status` field of the [`CRD`](./config/crd/bases/trust-anchor.linkerd.edenlab.io_linkerdtrustrotations.yaml) for
more details.

## Verification Jobs and Permissions

The operator spawns **ephemeral** Kubernetes Jobs running `linkerd check` during and after rotation.  
These Jobs use a dedicated ServiceAccount with restricted permissions.

See [`linkerd_check.yaml`](./config/rbac/linkerd_check.yaml) for more details.

## Getting Started

### Prerequisites

- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster

**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/linkerd-trust-rotator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/linkerd-trust-rotator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
> privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

> **NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall

**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/linkerd-trust-rotator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/linkerd-trust-rotator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
   can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025 Edenlab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

