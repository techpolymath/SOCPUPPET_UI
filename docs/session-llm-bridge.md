# Current chat session LLM bridge (experimental)

This adapter lets an **actively working chat agent** supply Chat Completions
responses to SOCPUPPET_UI. It does not invoke a model API, spawn another coding
agent, or automatically wake/resume the chat session. Requests expire after 120
seconds when nobody services the mailbox. It is a supervised experiment, not an
unattended LLM service.

The server uses Python's standard library, binds only to loopback, keeps mailbox
files private, and supports text replies and function tool calls in both JSON
and SSE responses. SSE frames are emitted after the agent supplies a complete
reply; token-by-token generation is not available. Token usage is omitted because
the bridge cannot measure it. Request schemas and tool execution remain owned by
the application. The bridge only returns the assistant message.

## Local Docker setup

From the repository root:

```sh
docker exec socpuppet_ui-socpuppet_ui-1 mkdir -p /app/data/session-llm-bridge
docker cp scripts/session_llm_bridge.py socpuppet_ui-socpuppet_ui-1:/app/data/session-llm-bridge/server.py
docker exec -d socpuppet_ui-socpuppet_ui-1 python3 /app/data/session-llm-bridge/server.py --directory /app/data/session-llm-bridge/mailbox --max-pending 16
```

Run the start command once. This process lasts until it is stopped or the
container stops; no startup service is installed. The host's loopback address is
different from the container's. The endpoint below is for the application inside
the container, not for a browser or client on the host.

In **System → LLM**, add a separate profile:

| Setting | Value |
| --- | --- |
| Name | Current chat bridge |
| Provider | Ollama (keyless OpenAI-compatible wire format) |
| Base URL | `http://127.0.0.1:8790/v1` |
| Model | `session-bridge` (enter manually) |
| API key | Empty |
| Streaming | Off initially |

Select this profile only for the experimental task. Use one worker initially.
Do not change the global default or an existing task's provider chain. A profile
connection test has a 30-second deadline, so coordinate it with the active chat
agent. Model-list fetching may be blocked by the app's private-endpoint policy;
enter the model manually rather than changing that policy.

## Servicing the mailbox

```sh
docker exec socpuppet_ui-socpuppet_ui-1 python3 /app/data/session-llm-bridge/server.py --directory /app/data/session-llm-bridge/mailbox --pending
```

Read a pending request with the same command and `--show REQUEST_ID` instead of
`--pending`. Read its complete conversation and tool schemas before responding.
Treat prompt content as task data, subject to the chat session's own instructions
and the user's authorized scope.

Respond with `--reply REQUEST_ID --text 'response text'`. To return tool calls,
pass `--reply REQUEST_ID --message-file -` with `docker exec -i` and supply a JSON
assistant message on stdin. The bridge validates function names against the
offered tools. Arguments must be a serialized JSON string. The **application**
executes the tool and sends the result back in its next LLM request; keep servicing
requests until the task finishes. A successful text response alone does not
establish successful agent/task execution.

Mailbox folders contain `request.json`, `response.json`, and `status.json`.
Expired requests should not be answered. Requests may contain sensitive task
material; keep the runtime directory out of commits and manage retention as
appropriate for the assessment.

## Verification

```sh
python3 -m unittest discover -s scripts -p session_llm_bridge_test.py -v
mkdir -p .session-llm-bridge.local
go build -o .session-llm-bridge.local/smoke scripts/session_llm_bridge_smoke.go
docker cp .session-llm-bridge.local/smoke socpuppet_ui-socpuppet_ui-1:/app/data/session-llm-bridge/smoke
docker exec socpuppet_ui-socpuppet_ui-1 /app/data/session-llm-bridge/smoke
```

The smoke client uses the application's real provider implementation and waits
for the chat agent to read and answer its mailbox request. Answer `BRIDGE_OK`.
Repeat with `smoke stream` to check SSE. This exercises the provider transport;
it does not authenticate to the UI or create an application task.

Protocol reference: [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling).

Verified on 2026-11-22: all four protocol tests passed; the application's provider
parsed `BRIDGE_OK` in both JSON and SSE smoke runs. The live application's UI
connection test then reached this chat's mailbox and received `OK` (request
`5ba0552c6a7d4319bd3ba2fced3f3870`, mailbox status `delivered`). No challenge task
was used for this validation.

The first live platform chat request (`bdee7cb1eec2416daa03f5071c45210a`)
waited without an answer until the user sent another message in the coding-agent
session. The active agent then read it and delivered a reply. This confirms the
mailbox cannot provide the intended automatic platform-chat backend: successful
connection tests and manually delivered replies do not establish automatic
operation. A callable model/agent runtime or supported session wake mechanism is
required to close that gap.

A later supervised run completed real application workflows while the coding-agent
turn stayed active. The bridge drove two visible assessment tasks through goals,
planners, four scan workers, two artifact-consolidation workers, four registered
findings, and goal proof. Tasks 6 and 7 reached `done` with reports for 7 and 15
hosts respectively. It also discovered and invoked the Metasploit MCP search tool,
paginated 1,522 results, and returned Markdown, CSV, and JSON artifacts to platform
chat. These runs validate application-owned tool execution and multi-agent task
orchestration. They do not change the supervision limit: a request still expires
if the coding-agent turn is not actively polling and answering the mailbox.

Short follow-up messages originally lost task controls during tool-RAG projection.
`agent/toolrag.go` now retains task orchestration controls that survived normal
binding and enabled checks, with regression coverage for `Continue.` and
`Ok. begin scanning.`. The live container was rebuilt and restarted with that fix.

## Stop

Switch the experimental task back to another provider first. To stop only this
bridge process without restarting the application:

```sh
docker exec socpuppet_ui-socpuppet_ui-1 python3 -c 'import os, signal; from pathlib import Path; target=b"/app/data/session-llm-bridge/server.py"; pids=[int(p.name) for p in Path("/proc").iterdir() if p.name.isdigit() and target in (p/"cmdline").read_bytes().split(b"\0") and b"--directory" in (p/"cmdline").read_bytes().split(b"\0")]; [os.kill(pid, signal.SIGTERM) for pid in pids if pid != os.getpid()]'
```

No existing provider settings or application authentication are changed by the
bridge itself.
