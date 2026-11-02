<h1 align="center">EzTest</h1>

<p align="center"><strong>AI-driven, semi-autonomous end-to-end testing for Web and HTTP API systems.</strong></p>

<p align="center">
  <a href="README.zh-CN.md">简体中文</a> | English
</p>

<p align="center">
  <img alt="Version: 0.2" src="https://img.shields.io/badge/version-0.2-blue">
  <img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Go 1.24" src="https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white">
  <img alt="Next.js 16" src="https://img.shields.io/badge/Next.js-16-black">
  <img alt="PostgreSQL 17" src="https://img.shields.io/badge/PostgreSQL-17-4169E1">
  <img alt="Node 22" src="https://img.shields.io/badge/Node-22-339933">
</p>

---

## Table of contents

- [Overview](#overview)
- [How it works](#how-it-works)
- [Features](#features)
- [Getting started](#getting-started)
- [Operations and deployment](#operations-and-deployment)
- [License](#license)

---

## Overview

EzTest takes a release version and its requirements and produces reviewed, executable, evidence-backed
end-to-end coverage. Humans only do the parts where judgment is genuinely required:

> clarify requirements in a conversation → open a design conversation → confirm the resulting cases.

Everything else — designing scenarios and cases, turning confirmations into execution waves, running
them in a real browser and against HTTP APIs, classifying failures, filing and merging issues,
rendering reports, and driving the version to completion — is performed by agent teams on top of a
deterministic platform core.

The control-plane model has five defining properties:

- **The task board is the only execution queue.** Every agent action, including a chat turn, is a
  `work_item` with a team, type, priority, dedupe key, lease and fencing epoch. A reconciler compares
  desired vs actual state and creates missing work; events only wake it up early.
- **Four teams plus a Version Steward.** Clarification, design, execution and delivery teams
  collaborate through persistent version × team channels and artifact handoff; there is no central
  orchestrating agent and teams do not call each other.
- **Version state is a projection, not a gate.** `initial → running → completed`, with automatic
  reopening when a new goal appears. A completed version is still usable.
- **Multi-SUT is native.** One version can bind several systems, each with environments, pages, APIs,
  read-only PostgreSQL targets, accounts/roles and a local Git repository. A case can target a
  different SUT per step, and evidence/assertions stay scoped to `version_sut_id`.
- **Evidence-bound verdicts.** Results are `passed` / `failed` / `blocked` / `inconclusive`; failures
  are classified as application, environment, session, model or case. Screenshots, console, network,
  API responses and visual diffs are content-addressed and immutable.
- **Minimal startup configuration.** The API process reads exactly four startup environment variables;
  all other runtime configuration lives in the database settings center.

## How it works

A normal journey is six steps:

1. **Admin setup.** Register systems, environments, pages, API surfaces, read-only target databases,
   accounts/browser sessions, local Git repositories and the LLM provider.
2. **Version and requirements.** Create a version, bind its SUTs, add requirements and let the
   clarification team turn the conversation into versioned draft requirements.
3. **Design.** Open a design conversation; the design team scans what needs designing, produces
   scenarios → cases → steps → assertions with requirement sources, SUT targets and code anchors, and
   keeps drafting until a human confirms.
4. **Execution.** Confirmation creates execution intents; the planner groups them into waves (one
   active wave per version). Case executors drive real Chrome and HTTP, with optional read-only
   database and visual assertions.
5. **Delivery.** Failures become issue candidates with evidence; wave and version reports are
   append-only revisions (HTML is authoritative, PDF is rendered by a separate worker); the
   exploration chain explains requirement → case → result → issue lineage.
6. **Close and continue.** When all work and closure predicates are satisfied, the Version Steward
   proposes completion. New requirements, reopened requirements or newly confirmed cases reopen the
   version for the next incremental wave.

The UI is organized as **8 global shell items** — Versions, Issues, Reports, Knowledge, Resources,
Quality, Agent center, Settings — and **8 version tabs** — Overview, Requirements & clarification,
Case design, Execution, Exploration chain, Issues, Reports, Code & changes. Global search (`Ctrl/Cmd+K`)
and the todo drawer live in the top bar; knowledge is global, not version-owned.

## Features

| Area | What you get |
|---|---|
| Requirements and clarification | Multi-requirement versions, clarification conversations, immutable requirement versions, single/batch confirmation, relations, attachments, AI-proposed rollback and knowledge injection |
| Case design | Design conversation as the primary UI, scenario/case/step/assertion model, versioned cases, per-case/scenario/requirement confirmation, current+confirmed execution semantics, supplement and gap drafts |
| Control plane and agent teams | Shared task board with leases/fencing, reconciler, version × team channels, 19 task types, 14 agent profiles, context packets and budgeted runs |
| Multi-SUT execution | Several SUTs per version, step-level SUT routing, per-role browser contexts/accounts, environment fingerprints, cross-system scheduling with per-SUT quotas |
| Execution and evidence | Automatic waves, pause/resume/checkpoint recovery, four-state results with failure classification, content-addressed screenshots/console/network/API/visual evidence, real executor registration, heartbeat, capacity, drain and per-executor dispatch |
| Code intelligence and visual assertions | Read-only local Git indexing, tree/file/diff/search/commits, impact analysis with code anchors and fact review; visual selector/region/threshold assertions with approved baselines and diff evidence |
| Delivery | Issue confirmation, merging, reopening, recurrence across waves and dedupe; append-only report revisions with `as_of`/stale/regenerate; HTML + PDF reports; exports |
| Exploration chain | Interactive graph from requirement to case, wave, result, evidence and issue, with filters, deep links, issue sidebar, replay and truncation metadata |
| Global workspace | Version switcher, global search, todo center, mobile navigation, dark/light theme, cross-version read-only issue and report centers |
| Knowledge | Global and system-scoped knowledge, draft → review → approved/revoked lifecycle, promotion from versions, PostgreSQL full-text injection with citation budgets and usage records |
| Quality and Agent center | Deterministic and real-LLM evaluation tracks, design/impact/classification/supplement metrics, baseline regression rules, prompt revision/preview/rollback and run observation |
| Operations | Embedded append-only migrations, readiness/leader election, drain with in-flight counters, per-instance rate limiting (429 + `Retry-After`), event/audit retention cleanup, GC dry-run/apply, backup/restore scripts |

## Getting started

### Prerequisites

| Requirement | Notes |
|---|---|
| PostgreSQL 17 | Local or remote; the development default is `localhost:5433`. |
| Go 1.24 | `backend/go.mod` declares Go `1.24.6`. |
| Node.js 22+ and pnpm | For the web app, executor and PDF renderer. |
| A Chromium-based browser | Google Chrome, Microsoft Edge or Playwright Chromium; real case execution needs it. |

### 1. Configure exactly four startup variables

Copy `.env.example` to `.env` (gitignored) and set the database URL; the three ports have defaults:

| Variable | Purpose | Default |
|---|---|---|
| `EZTEST_DATABASE_URL` | PostgreSQL connection URL | required |
| `EZTEST_API_PORT` | API listen port | `13333` |
| `EZTEST_WEB_PORT` | Web port; the CORS origin is derived from it | `6868` |
| `EZTEST_EXECUTOR_PORT` | Executor port; the default executor URL is derived from it | `13336` |

There is no fifth startup variable. Paths, LLM credentials, executor URLs, limits, retention, rate
limiting and every other runtime knob are stored in the database settings center and managed from
**Settings** in the UI.

### 2. Start the development stack

```powershell
# Copy and edit .env first.
Copy-Item .env.example .env

# First run only, when using the bundled portable PostgreSQL toolchain:
pwsh .tools/setup-toolchain.ps1
pwsh scripts/db.ps1 init

# API + web + executor, health-checked before the URLs are printed.
pwsh scripts/dev-all.ps1
```

`scripts/dev-all.ps1` applies embedded migrations, seeds the administrator, starts the API, web and
executor, and waits for real readiness. Useful variants:

```powershell
pwsh scripts/dev-all.ps1 -Status   # probe the running stack
pwsh scripts/dev-all.ps1 -Stop     # stop API/web/executor (PostgreSQL is left running)
pwsh scripts/dev-renderer.ps1      # optional: PDF renderer on 127.0.0.1:13337
```

Open <http://localhost:6868> and sign in with the administrator seeded on first boot. If you use your
own PostgreSQL 17 server instead of the portable one, point `EZTEST_DATABASE_URL` at it and skip
`db.ps1 init`. The bundled portable toolchain creates the `eztest` / `eztest_test` databases: either
set the URL to `/eztest` or create the database named in your URL (the `.env.example` default is
`eztest_v02`).

| Component | Default URL |
|---|---|
| Web | <http://localhost:6868> |
| API liveness / readiness | <http://localhost:13333/healthz> · <http://localhost:13333/api/health/ready> |
| Executor health | <http://127.0.0.1:13336/healthz> |
| PDF renderer health | <http://127.0.0.1:13337/healthz> |

### 3. Test

```powershell
pwsh scripts/test.ps1              # Go unit + integration, frontend lint/typecheck/vitest
cd executor; pnpm test             # executor node:test suite
cd renderer; node --test           # renderer node:test suite
```

## Operations and deployment

- **Local development:** API `:13333`, web `:6868`, executor `:13336`, optional renderer `:13337`,
  PostgreSQL `:5433`.
- **Docker Compose:** [deploy/README.md](deploy/README.md) describes the delivered six-service stack
  (two API replicas, web, PostgreSQL, executor, renderer).
- **Multi-replica:** API replicas coordinate through PostgreSQL leases and a leader advisory lock;
  readiness exposes `inflight_requests`/`inflight_work`, instances can be drained, and all replicas
  must share one content directory. Rate limiting is per instance: total allowed rate = replicas ×
  `security.rate_limit_per_minute` (default `0` = disabled).
- **Retention:** `retention.event_days` (default 90) and `security.audit_retention_days` are enforced by
  a leader-gated daily cleanup; GC defaults to dry-run and requires explicit confirmation to apply.
- **Backup/restore:** use `scripts/backup.ps1` / `scripts/restore.ps1`; back up PostgreSQL and the
  content directories together.

## License

Licensed under the [Apache License 2.0](LICENSE).
