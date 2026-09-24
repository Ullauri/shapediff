<!-- shapediff:v1 -->
**0 endpoints added, 1 endpoint's reach changed, 1 signature changed (3 callers), 1 package added.**

head does not compile: `acme/svc/broken` (2 changes hidden)

`POST /orders` now reaches `orders`. `(*Service).Create` changed signature (3 callers).

#### Where this lands

At base aaaaaaaa the repo has 3 packages, 2 entry points and 1 sink (1 external module not drawn). This MR changes code in 1 of them and adds 1 new package.

| package | symbols | this MR | imported by | reached from | talks to |
|---|---|---|---|---|---|
| [`orders`](https://gitlab.example.com/acme/svc/-/tree/aaaaaaaaaaaa/orders) | 12 | 1 added, 1 changed | `api`, `billing`, `cron`, `export`, +1 | `POST /orders` (new) | `db orders` |
| [`util`](https://gitlab.example.com/acme/svc/-/tree/bbbbbbbbbbbb/util) | 3 | new package | `orders` | — | — |

```mermaid
flowchart LR
  classDef added fill:#dcfce7,stroke:#16a34a,color:#1f2937
  classDef context fill:#f3f4f6,stroke:#9ca3af,color:#1f2937
  classDef touched fill:#ede9fe,stroke:#7c3aed,stroke-width:2px,color:#1f2937
  n30b7924f(["2 http routes"])
  n7309427a[["orders"]]
  n9cc466a4[["store"]]
  na7fef5cf[["util"]]
  n53798808[("db")]
  n30b7924f --> n7309427a
  n7309427a --> n9cc466a4
  n7309427a --> na7fef5cf
  n9cc466a4 --> n53798808
  linkStyle 2 stroke:#16a34a,stroke-width:2px
  class na7fef5cf added
  class n30b7924f,n9cc466a4,n53798808 context
  class n7309427a touched
```
Legend: purple changed by this MR, green added, red dashed removed, grey untouched; rounded entry points, cylinder sinks; arrows are imports; boxes are directories. 4 untouched packages folded into their directory to fit.

**Key.** A rounded box is an entry point, a double-bordered box a package, a cylinder a data store, a plain box a function or definition. Green is added, red dashed removed, amber a changed signature, grey unchanged context drawn for orientation. Arrows take the same colours; a dotted arrow is an inferred call, a dashed one dynamic dispatch. `*` marks a definition whose body also changed, and `name +N more` is a group of N definitions drawn as one box.

#### Dataflow

```mermaid
flowchart LR
  classDef context fill:#f3f4f6,stroke:#9ca3af,color:#1f2937
  ndcb5c3a1(["POST /orders"])
  nc5b8bb09[("orders")]
  ndcb5c3a1 --> nc5b8bb09
  linkStyle 0 stroke:#16a34a,stroke-width:2px
  class ndcb5c3a1,nc5b8bb09 context
```

#### Call paths

```mermaid
flowchart LR
  classDef added fill:#dcfce7,stroke:#16a34a,color:#1f2937
  classDef context fill:#f3f4f6,stroke:#9ca3af,color:#1f2937
  classDef removed fill:#fff1f2,stroke:#dc2626,stroke-dasharray:5 3,color:#1f2937
  classDef signature_changed fill:#fef3c7,stroke:#d97706,color:#1f2937
  subgraph sg2f27a5c6["orders"]
    na3c6eddb["(*Service).Create* (+3 callers)"]
    n7e4a7fe1["Handler"]
    n3fa7fd89["Validate"]
  end
  ndcb5c3a1(["POST /orders"])
  na7fef5cf[["util +3 more"]]
  nc5b8bb09[("orders")]
  n6e4081b5["Old"]
  ndcb5c3a1 --> n7e4a7fe1
  na3c6eddb -.->|3 targets| na7fef5cf
  na3c6eddb --> nc5b8bb09
  na3c6eddb --> n3fa7fd89
  n7e4a7fe1 --> na3c6eddb
  n7e4a7fe1 --> n6e4081b5
  linkStyle 3 stroke:#16a34a,stroke-width:2px
  linkStyle 4 stroke-dasharray:2 2
  linkStyle 5 stroke:#dc2626,stroke-width:2px,stroke-dasharray:5 3
  class n3fa7fd89 added
  class ndcb5c3a1,na7fef5cf,nc5b8bb09,n7e4a7fe1 context
  class n6e4081b5 removed
  class na3c6eddb signature_changed
```

<details><summary>1 more call-path diagram</summary>

**GET /x**

```mermaid
flowchart LR
  classDef added fill:#dcfce7,stroke:#16a34a,color:#1f2937
  subgraph sge40c292c["a"]
    na90b54ed["B"]
  end
  class na90b54ed added
```
</details>

#### Module wiring

```mermaid
flowchart LR
  classDef added fill:#dcfce7,stroke:#16a34a,color:#1f2937
  classDef context fill:#f3f4f6,stroke:#9ca3af,color:#1f2937
  n7309427a[["orders"]]
  na7fef5cf[["util"]]
  n7309427a --> na7fef5cf
  linkStyle 0 stroke:#16a34a,stroke-width:2px
  class na7fef5cf added
  class n7309427a context
```

<details><summary>Details</summary>

| node | status | package | link |
|---|---|---|---|
| `POST /orders` | context |  |  |
| `orders` | context |  |  |
| `B` | added | a |  |
| `(*Service).Create* (+3 callers)` | signature_changed | acme/svc/orders | [orders/s.go:12](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/orders/s.go#L12) |
| `Handler` | context | acme/svc/orders | [orders/h.go:3](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/orders/h.go#L3) |
| `Validate` | added | acme/svc/orders | [orders/v.go:7](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/orders/v.go#L7) |
| `Old` | removed | acme/svc/util | [util/o.go:1](https://gitlab.example.com/acme/svc/-/blob/aaaaaaaaaaaa/util/o.go#L1) |

**Renames**

- `Old` → `Older` ? (similarity)

**Body-only changes**

- `acme/svc/orders.Service`

**Wide-reach changes** (through dispatch sites with more targets than the narrow limit; listed, not drawn)

- `POST /orders` now reaches `audit`

**Suppressed packages**

- `acme/svc/broken`: base ok, head partial, 2 removals and 0 additions hidden

2 nodes hidden by the budget, 0 on no drawn path, 3 definitions collapsed into packages, 0 generated and 0 test definitions not drawn, 0 test packages left out of the wiring.

**Warnings**

- vta took 41s

extractor go-xtools 0.1.0 (typed, go1.26.0), base aaaaaaaa, head bbbbbbbb, 1.234s
</details>
