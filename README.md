# Model Armor Batch Tester (Go)


## GitHub Setup

### Clone the Repository

```
git clone https://github.com/lislema/model-armor-batch.git
cd model-armor-batch
```

### Initialize

```
go mod tidy
```

---

## Build

```
go build -o model_armor_batch
```

---

## Run

### Local Mode

```
export LOCAL_MODE=true
./model_armor_batch input.txt output.jsonl
```

### GCP Mode

```
gcloud auth login
gcloud config set project <your-project-id>

unset LOCAL_MODE
export MODEL_ARMOR_TEMPLATE=<your-template>

./model_armor_batch input.txt output.jsonl
```

---

## Testing

```
go test -cover ./...
```

## Local Mode Explained

Local Mode runs fully offline using mocked responses.

- No API calls
- No authentication
- Deterministic execution

---

## GCP Mode Explained

GCP Mode executes real calls to Model Armor.

- Requires gcloud auth
- Uses real API
- Produces real metrics
- Subject to quotas and latency

---

## Summary

- Local Mode = system validation
- GCP Mode = real security + performance validation

## Architecture

### High-Level Flow (Mermaid)

```mermaid
flowchart LR
    A[Input File] --> B[Parser]
    B --> C[Worker Pool]
    C --> D[Rate Limiter]
    D --> E[HTTP Client]

    E -->|Local Mode| F[Mock Response]
    E -->|GCP Mode| G[Model Armor API]

    F --> H[Metrics Collector]
    G --> H

    H --> I[JSONL Output]
    H --> J[Console Metrics]
```

---

### Worker Execution Model (Mermaid) 

```mermaid
sequenceDiagram
    participant Main
    participant Worker
    participant API

    Main->>Worker: Send job
    Worker->>Worker: Wait for rate limiter
    Worker->>API: HTTP Request
    API-->>Worker: Response
    Worker->>Main: Latency result
```

---
