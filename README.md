# KubeMorph

English | [简体中文](README.zh-CN.md)

KubeMorph is the official open-source implementation of the paper **“KubeMorph: Optimization-as-Code for Multi-Cloud Multi-Objective Scheduling”**.

It turns “multi-cloud + multi-objective optimization” into an **Optimization-as-Code** workflow:

- Describe optimization objectives, weights, and data sources declaratively
- Support custom optimization objective plugins and solver backends
- Compile cluster state and external topology inputs into a solvable optimization problem
- Provide a unified REST API and an operational entry for the frontend
- Offer a Web UI for managing clusters/apps/policies/results in one place

> Note: this repository is a **multi-module Go project**, containing two Go modules (`controller/` and `server/`) and a frontend project (`web/`).

## 🔎 Optimization-as-Code (OaC) paradigm

The following figure illustrates our Optimization-as-Code stages.

> Figure source: **reproduced from our paper** “KubeMorph: Optimization-as-Code for Multi-Cloud Multi-Objective Scheduling”.

![Optimization-as-Code stages](plots/oac_stages.png)

---

## ✨ Features

- **Multi-cluster management**: register and inspect clusters and topology information
- **Global application & distribution objects**: create and query cross-cluster deployments
- **Optimization policy management**: create and view optimization policy(multi-objective, extensible)
- **Optimization & re-orchestration plans**: generate and view optimization results
- **Visualization**: Web UI supports English/Chinese and visualizes multi-objective optimization details

---

## 🧩 Repository layout

```text
.
├── controller/   # Kubernetes controller (kubebuilder/controller-runtime): CRDs + reconciliation logic
├── server/       # Backend service (Go + Gin): REST API + K8s adapters
└── web/          # Frontend (Vue 3 + Vite + TS + Ant Design Vue)
```

---

## 📚 CRDs (Custom Resource Definitions)

KubeMorph models key entities (clusters, global deployments, optimization policies, plans, etc.) using Kubernetes CRDs.

> Figure source: **reproduced from our paper** “KubeMorph: Optimization-as-Code for Multi-Cloud Multi-Objective Scheduling”.

![KubeMorph CRDs](plots/kubemorph_crds.png)

## 🏗️ Architecture (high-level)

Typical workflow:

1. Users create/update CRs (e.g. `OptimizationPolicy`) via **Web UI** or by applying YAML
2. The **Controller** watches CRs and cluster changes, generating/updating optimization and re-orchestration plans
3. The **Server** provides a REST API and a unified data layer (etcd), and talks to Kubernetes (read/apply changes)
4. The Web UI calls the Server API to display status, plans, and results

---

## 🚀 Quickstart (recommended: backend via Docker, frontend locally)

This is the most straightforward way to try KubeMorph:

### 1) Start the server (with etcd)

The `server/` directory provides `docker-compose.yml`, which starts the REST API service (default `:8080`).

From the repo root:

```bash
cd server
docker compose up --build
```

Default endpoint: `http://localhost:8080`


> Config reference: `server/config.example.yaml`

### 2) Start the web UI (dev)

```bash
cd web
npm install
npm run dev
```

After it starts, open the address printed by Vite (usually `http://localhost:5173`).

### 3) Start the controller (connect to your cluster)

The controller lives in `controller/` and reconciles KubeMorph CRDs such as `Cluster`, `GlobalDeployment`, `ReplicaBinding`, and `OptimizationPolicy`.

Run locally (connect to the cluster pointed by your current kubeconfig):

```bash
cd controller
make run
```

Install CRDs and deploy to the cluster (closer to production):

```bash
cd controller
make install
make deploy
```

> Tip: if you only want a compile check, run `go test ./... -run '^$'` (compile-only, no test execution).

---

## 🖥️ Web UI screenshots

The Web UI provides end-to-end management for clusters, applications, and optimization.

### Cluster management

![Cluster management](plots/cluster_management.png)

### Application management

![Application management](plots/application_management.png)

### Optimization management

![Optimization management](plots/optimization_management.png)

## 📦 Production deployment

### Web (Nginx static hosting + reverse proxy)

`web/` includes a production Dockerfile. It serves static assets via Nginx and can proxy `/api/*` to the backend server.

Build:

```bash
cd web
docker build -t kubemorph-web:latest .
```

Run (example):

```bash
docker run --rm -p 8080:80 \
  -e BACKEND_SERVICE_HOST=kubemorph-server \
  -e BACKEND_SERVICE_PORT=8080 \
  -e BACKEND_API_PREFIX=/api \
  -e BACKEND_API_VERSION_PREFIX=/api/v1 \
  kubemorph-web:latest
```

> See `web/README.md` for details.

### Controller (deploy to Kubernetes)

The controller in `controller/` is based on Kubebuilder scaffolding. It supports:

- Install CRDs: `make install`
- Deploy the controller: `make deploy IMG=<your-registry>/kubemorph-controller:<tag>`

Example (push the image to a registry your cluster can pull from):

```bash
cd controller
make docker-build docker-push IMG=<your-registry>/kubemorph-controller:latest
make install
make deploy IMG=<your-registry>/kubemorph-controller:latest
```

### Server (production)

The server in `server/` provides the REST API. This repo includes `server/docker-compose.yml` as a minimal production/demo deployment template.

Start (example):

```bash
cd server
docker compose up -d
```

Default endpoint:

- Server：`http://localhost:8080`

Key config references:

- `server/config.example.yaml`
- `server/docker-compose.yml`

---

## 🔌 Extensibility (objective plugins / solver backends)

This section describes how to extend optimization objectives and solver backends based on the current implementation. Related code is under `controller/internal/optimizer/`.

### 1) Custom objective plugins

Objective scores are computed by plugins and then aggregated with weights.

Plugin interfaces are defined in `controller/internal/optimizer/plugins.go`:

- `LinearScorePlugin`: returns linear placement scores per replica per candidate node (usually normalized to 0~100)
- `MigrationScorePlugin`: returns migration penalty per replica

Reference implementation: `controller/internal/optimizer/plugin_cost.go` (CostPlugin). It expects nodes to have the `node.kubex.io/type` label and calculates cost scores based on an instance price table.

How to add your own plugin:

1. Add `plugin_<name>.go` under `controller/internal/optimizer/` and implement `LinearScorePlugin`
2. If you need external inputs, extend `ObjectiveInputs` in `controller/internal/optimizer/objective.go`
3. Add a new branch in `BuildObjective(...)` (same file) for your new `WeightedGoal.Type`, then merge scores with `Weight`

Aggregation rules (as implemented in the code):

- Placement objectives are merged into a single `placementScore[replica][node]` matrix (linear terms)
- Migration objectives produce a separate `migrationPenalty[replica]` vector, which is introduced as extra linear terms during solving

### 2) Solver backends

Solvers are abstracted by the `Solver` interface in `controller/internal/optimizer/ilp.go`:

- `Solve(ctx context.Context, p Problem) (*SolveResult, error)`

The main entry is `SolveProblem(...)`, which builds the ILP model and calls the solver.

#### OR-Tools (optional)

This repo contains an optional OR-Tools backend, gated by a build tag:

- Default (no tag): `ORToolsSolver` is a stub (`ortools_solver_stub.go`) and instructs you to build with `-tags=kubex_ortools`
- With tag: builds `ortools_solver_cgo.go` (requires cgo + OR-Tools installed, and valid include/lib paths)

To add a new solver (heuristics / other ILP solvers), implement `Solver` and inject it at the optimization entry point.

---

## 📄 Citation

If you use this project in research, please cite the paper:

> KubeMorph: Optimization-as-Code for Multi-Cloud Multi-Objective Scheduling

(BibTeX to be added.)

---

## 🤝 Contributing

Issues and PRs are welcome.

Suggested workflow:

1. Fork the repo and create a feature branch
2. Formatting & static checks:
  - `controller/`: `make fmt vet`
  - `web/`: `npm run lint` / `npm run format`
3. Run basic tests:
  - `server/`: `go test ./...`
  - `controller/`: `make test`

---

## License

See `LICENSE` in the repository root.
