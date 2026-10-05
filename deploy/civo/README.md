# DOW Mind on Civo VPS

Run DOW Mind 24/7 on your Civo VPS (the same box as the Context Arena bots):
Qdrant + `dow-mind --serve` as systemd services. No Docker needed — both are
static binaries. DOW Mind binds `127.0.0.1:7777` by default, so nothing is
exposed to the internet; you reach it through an SSH tunnel.

## 0. Prerequisites

- Civo VPS (this was built for the existing `jkt1` box; any Ubuntu 22.04+
  works), root SSH access.
- This repo pushed to `RivaldiMurpia/dow-mind` (the setup script clones it).
- A Voyage AI key (free, 200M tokens at [voyageai.com](https://voyageai.com))
  — put it in `/etc/dow-mind/env` yourself, never paste it in chat.

## 1. One-shot setup

```bash
curl -fsSL https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/deploy/civo/setup.sh | sudo bash
```

This installs Go, Qdrant v1.19.2, builds `dow-mind` from source, creates
`/etc/dow-mind/env` (with a generated admin token — **save the printed
token**), and registers/enables `qdrant` + `dow-mind` systemd services.
It also creates `/opt/dow-mind/repos/` plus a cron job that `git pull`s every
mirror in it every 10 minutes.

## 2. Configure

```bash
sudo nano /etc/dow-mind/env
# set DOWMIND_VOYAGE_API_KEY=<your key>
sudo systemctl restart dow-mind
```

## 3. Add repos to index

Clone the repos you want searchable into the mirror dir (public repos are
fine; for private ones add a deploy key first):

```bash
git clone https://github.com/RivaldiMurpia/dow-oracle.git /opt/dow-mind/repos/dow-oracle
# ... etc
```

## 4. Index + watch (run on the VPS)

```bash
TOKEN="$(grep DOWMIND_ADMIN_TOKEN /etc/dow-mind/env | cut -d= -f2)"
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"directory":"/opt/dow-mind/repos/dow-oracle"}' \
  http://127.0.0.1:7777/api/projects/dow-oracle/watch
```

This indexes immediately and then **auto re-indexes (debounced) whenever
files change**. The cron job keeps the mirrors fresh from GitHub, so the
watch picks up your pushes within ~10 minutes. Check status anytime:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:7777/api/projects/dow-oracle/watch
```

## 5. Use it from your laptop

SSH tunnel (nothing exposed publicly):

```bash
ssh -L 7777:127.0.0.1:7777 <user>@<vps-ip>
```

Then open `http://localhost:7777` — the dashboard asks for the admin token
once (Settings). For coding agents (Claude Code, Cursor, …) on the laptop,
point the MCP client at the tunnel:

```json
{
  "mcpServers": {
    "dow-mind": {
      "url": "http://localhost:7777/mcp",
      "headers": { "Authorization": "Bearer <DOWMIND_ADMIN_TOKEN>" }
    }
  }
}
```

Available MCP tools: `mind_create_project`, `mind_index_project`,
`mind_watch_project`, `mind_unwatch_project`, `mind_semantic_search`,
`mind_retrieve_context`, `mind_index`, `mind_list_projects`,
`mind_project_exists`, `mind_list_points`, `mind_delete_points`, `mind_stats`.

## 6. Updates

Push to GitHub, then on the VPS:

```bash
sudo /opt/dow-mind/deploy/civo/update.sh
```

(or `curl` it raw the same way as setup.sh).

## Ops notes

- Logs: `journalctl -u dow-mind -f`, `journalctl -u qdrant -f`
- Restart: `sudo systemctl restart dow-mind`
- Data: Qdrant stores vectors in `/var/lib/qdrant` (survives restarts)
- Resources: the `g4s.small` box (1 vCPU/2 GB) comfortably holds Qdrant +
  dow-mind alongside the arena bots for small collections; watch `free -h`
  and resize the instance if it gets tight
- The admin token is the only auth: keep `/etc/dow-mind/env` (0600) safe,
  rotate with `openssl rand -hex 32` if ever leaked
