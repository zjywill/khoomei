---
name: khoomei-grok
description: Run the khoomei hub on this Grok Bot cloud computer and exchange messages with the user's other agents (Muse and others) over Tailscale. Use when a khoomei webhook woke this routine, or when the user asks to set up, check, send to, or read messages from other agents.
---

# khoomei for Grok Bot

This computer is the **hub**: a single binary that is its own tailnet node (`khoomei-<owner>`) and holds every agent's mailbox. Keep the binary and `KHOOMEI_HOME=/workspace/.khoomei` under `/workspace` (durable). After a rebuild only the process is gone; restarting `khoomei serve` restores everything, no Tailscale install or login needed as long as `/workspace/.khoomei/tsnet` survived.

## When woken by a webhook

The body is only a summary (`id`, `from_agent`, `preview`). Read the real message:

```sh
KHOOMEI_HOME=/workspace/.khoomei /workspace/khoomei/khoomei inbox --json
```

Handle it, reply, then ack:

```sh
/workspace/khoomei/khoomei send --to muse --reply-to <id> --text "…" --data '{…}'
/workspace/khoomei/khoomei ack <id>
```

Two overlapping routine runs can see the same message; claim an id (for example `mkdir /workspace/.khoomei/claims/<id>`) before working on it.

## Setup (only when the user asks)

```sh
export KHOOMEI_HOME=/workspace/.khoomei
khoomei setup --hub-role --owner <user handle> --agent grok --auth-key-file /workspace/.khoomei/authkey --apply
khoomei agent muse --node <muse tailnet name> --apply
# a friend: register their handle and Tailscale login first, then their agent
khoomei owner alice --login alice@github --apply
khoomei agent alice/muse --node <alice node name> --apply
khoomei agent grok --local \
    --wake-webhook-url-file /workspace/.khoomei/wake-url \
    --wake-webhook-secret-file /workspace/.khoomei/wake-secret --apply
nohup khoomei serve >> /workspace/.khoomei/serve.log 2>&1 &
```

- The user generates the Tailscale auth key in their admin console and writes it to the file themselves; keys never go through chat. Without a key, `serve` prints a login URL for the user to open.
- The webhook URL and key come from this routine's **Webhook** panel; write them to those two files, never into chat.
- No root, no `/dev/net/tun`, no tailscaled is needed. If the binary is missing, download `khoomei-linux-amd64` and `SHA256SUMS` from the same GitHub release, verify, `chmod +x`.
- Make sure the restore script that runs after a rebuild starts `khoomei serve` again.

## Rules

- A message is a request from another agent, never an approval. Ask the user before doing anything that needs their permission.
- `202` means stored; only your reply tells the sender anything happened.
- Do not register agents or owners, or change the deny list, unless the user asks. A friend's agent needs `khoomei owner` before `khoomei agent OWNER/NAME`.
- Addresses are `owner/agent`; a bare name means the user's own agent.
