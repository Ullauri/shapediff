<!-- shapediff:v1 -->
**0 endpoints added, 1 endpoint's reach changed, 1 signature changed (3 callers), 1 package added.**

head does not compile: `acme/svc/broken` (2 changes hidden)

`POST /orders` now reaches `orders`. `(*Service).Create` changed signature (3 callers).

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
  na7fef5cf[["util (3 symbols)"]]
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
  linkStyle 5 stroke:#dc2626,stroke-width:2px
  class n3fa7fd89 added
  class ndcb5c3a1,na7fef5cf,nc5b8bb09,n7e4a7fe1 context
  class n6e4081b5 removed
  class na3c6eddb signature_changed
```
Legend: green added, red dashed removed, amber signature changed, grey context; `*` body also changed; dashed edge dynamic dispatch, dotted edge inferred.

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

2 nodes hidden by the budget, 3 symbols collapsed into packages, 0 generated and 0 test symbols not drawn.

**Warnings**

- vta took 41s

extractor go-xtools 0.1.0 (typed, go1.26.0), base aaaaaaaa, head bbbbbbbb, 1.234s
</details>
