# Security policy

Agent Reliability Lab is a **development tool**. Traces can contain prompts,
model outputs, tool arguments and other sensitive data from the agents you test.

## Deployment guidance

- The provided compose files bind the lab to `127.0.0.1`. Before exposing it to a
  network, set `ARL_AUTH_TOKEN` and put it behind TLS (see `deploy/nginx.conf.example`).
- Agents authenticate with `LAB_TOKEN` (same value) as a bearer header.
- Replays execute **your agent's own code** with values edited in the Workbench.
  Only grant Workbench access to people you would let run your agent.
- Provider API keys stay in the lab's environment; they are never sent to the
  browser or stored in traces. Fields that look like secrets are redacted.
- Keep the SDKs disabled outside development (`STAGE` other than `dev`).

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/VicoAritonang/lab-of-night-and-flame/security/advisories/new)
rather than public issues. Include steps to reproduce and the affected version.
You can expect an acknowledgement within a few days.
