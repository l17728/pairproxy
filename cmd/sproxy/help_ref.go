package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	adminCmd.AddCommand(adminHelpAllCmd)
}

// adminHelpAllCmd 打印所有 admin 命令的完整参考文档（Markdown 格式）。
// 用途：供 Claude 等 AI 助手一次性读取全部命令语法，将自然语言指令转换为
// 具体的 shell 命令。
var adminHelpAllCmd = &cobra.Command{
	Use:   "help-all",
	Short: "Print a complete command reference for all admin subcommands (AI-friendly Markdown)",
	Long: "Print every admin command with its full syntax, flags, and examples in\n" +
		"Markdown format. This is designed for AI assistants (e.g. Claude) to load\n" +
		"once and then translate natural-language instructions into exact shell commands.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Print(adminCommandReference)
		return nil
	},
}

// bq 是反引号字符常量，用于在 const 字符串拼接中嵌入反引号。
const bq = "`"

// adminCommandReference 是全量命令参考文档（Markdown）。
// 维护原则：每新增或修改一条 CLI 命令时，同步更新此文档。
const adminCommandReference = `# PairProxy sproxy Admin Command Reference

Usage for AI assistants: Run ` + bq + `sproxy admin help-all` + bq + ` to load this
reference, then map the user's natural-language request to the matching
command. All commands require a valid ` + bq + `sproxy.yaml` + bq + ` in the current
directory, or pass ` + bq + `--config <path>` + bq + `.

---

## Global Flag

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--config <path>` + bq + ` | ` + bq + `sproxy.yaml` + bq + ` in CWD | Path to sproxy configuration file |

All ` + bq + `sproxy admin *` + bq + ` commands accept this flag.

---

## Cluster Mode & Primary-Only Commands

PairProxy supports three deployment modes (` + bq + `cluster.role` + bq + ` in sproxy.yaml):

- **standalone** (` + bq + `role: ""` + bq + ` / default for SQLite): single-node, no cluster features
- **primary + worker** (SQLite): classic primary/worker split — workers are read-only, synced every 30s
- **peer** (PostgreSQL): all nodes are equal, share the same PG database, any node accepts writes

> ⚠️ **Worker Node Restriction** (` + bq + `role: worker` + bq + ` only): All **write commands** listed below (marked **[primary-only]**)
> **must be run against the Primary node**. Worker nodes are read-only: their databases are
> automatically synced from the Primary every 30 seconds. Running write commands on a Worker
> will return HTTP 403 (worker_read_only).
>
> **Peer mode** (` + bq + `role: peer` + bq + `): No such restriction — all nodes accept writes. Auto-set when
> ` + bq + `database.driver: postgres` + bq + ` is configured without an explicit role.
>
> **Read commands** (list, status, stats, audit) work on all nodes.

**How to target the Primary node:**
` + bq + `sproxy admin --config /path/to/primary-sproxy.yaml user add alice --password X` + bq + `

Or configure your ` + bq + `sproxy.yaml` + bq + ` ` + bq + `admin.host` + bq + ` / run the CLI on the Primary host directly.

**Primary-only commands** include:
- All ` + bq + `user add/disable/enable/reset-password/set-group` + bq + ` (§1)
- All ` + bq + `group add/set-quota/delete` + bq + ` (§2)
- ` + bq + `token revoke` + bq + ` (§3)
- ` + bq + `llm target add/update/delete/enable/disable` + bq + ` + ` + bq + `llm bind/unbind/distribute` + bq + ` (§8)
- ` + bq + `apikey add/assign/revoke` + bq + ` (§9)
- ` + bq + `backup` + bq + ` / ` + bq + `restore` + bq + ` (§10)
- ` + bq + `drain enter/exit` + bq + ` (§12)
- ` + bq + `import` + bq + ` (§14)

---

## 1. User Management

### 1.1 Create user

` + bq + `sproxy admin user add <username> [--password <pwd>] [--group <group-name>]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--password` + bq + ` | Password in plain text. Prompted interactively if omitted. |
| ` + bq + `--group` + bq + ` | Name of an existing group to assign the user to. Omit for no group. |

Examples:
  sproxy admin user add alice --password s3cret
  sproxy admin user add bob --password pass123 --group enterprise

Natural language: "create user", "add user", "new user", "register user"

---

### 1.2 List users

` + bq + `sproxy admin user list [--group <group-name>]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--group` + bq + ` | Filter by group name. Omit to list all users. |

Examples:
  sproxy admin user list
  sproxy admin user list --group enterprise

Natural language: "list users", "show users", "all users", "users in group X"

---

### 1.3 Enable / Disable user

` + bq + `sproxy admin user enable <username>` + bq + `
` + bq + `sproxy admin user disable <username>` + bq + `

Examples:
  sproxy admin user enable alice
  sproxy admin user disable bob

Natural language: "enable user", "disable user", "deactivate user", "suspend user", "reactivate user"

---

### 1.4 Reset password

` + bq + `sproxy admin user reset-password <username> [--password <new-pwd>]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--password` + bq + ` | New password. Prompted interactively if omitted. |

Examples:
  sproxy admin user reset-password alice --password newpass
  sproxy admin user reset-password alice          # prompts interactively

Natural language: "reset password", "change password", "set password"

---

### 1.5 Change user group

` + bq + `sproxy admin user set-group <username> --group <group-name>` + bq + `
` + bq + `sproxy admin user set-group <username> --ungroup` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--group` + bq + ` | Name of the target group. |
| ` + bq + `--ungroup` + bq + ` | Remove user from any group (set group to none). |

Examples:
  sproxy admin user set-group alice --group premium
  sproxy admin user set-group alice --ungroup

Natural language: "move user to group", "assign group", "change group", "remove from group", "ungroup user"

---

## 2. Group Management

### 2.1 Create group

` + bq + `sproxy admin group add <name> [quota flags]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--daily-limit <n>` + bq + ` | unlimited | Max tokens per day across all users in group |
| ` + bq + `--monthly-limit <n>` + bq + ` | unlimited | Max tokens per month |
| ` + bq + `--rpm <n>` + bq + ` | unlimited | Max requests per minute per user |
| ` + bq + `--max-tokens-per-request <n>` + bq + ` | unlimited | Max ` + bq + `max_tokens` + bq + ` per API call |
| ` + bq + `--concurrent-requests <n>` + bq + ` | unlimited | Max simultaneous in-flight requests per user |

Use 0 for any quota flag to mean "no limit".

Examples:
  sproxy admin group add free --daily-limit 50000 --rpm 10
  sproxy admin group add enterprise --monthly-limit 5000000
  sproxy admin group add trial --daily-limit 10000 --max-tokens-per-request 2048

Natural language: "create group", "add group", "new group"

---

### 2.2 List groups

` + bq + `sproxy admin group list` + bq + `

Natural language: "list groups", "show groups", "all groups"

---

### 2.3 Update group quota

` + bq + `sproxy admin group set-quota <name> [quota flags]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--daily <n>` + bq + ` | Daily token limit (0 = remove limit) |
| ` + bq + `--monthly <n>` + bq + ` | Monthly token limit (0 = remove limit) |
| ` + bq + `--rpm <n>` + bq + ` | Requests-per-minute limit (0 = remove limit) |
| ` + bq + `--max-tokens-per-request <n>` + bq + ` | Per-request max_tokens cap (0 = remove limit) |
| ` + bq + `--concurrent-requests <n>` + bq + ` | Concurrent requests per user (0 = remove limit) |

Examples:
  sproxy admin group set-quota enterprise --daily 1000000 --monthly 20000000
  sproxy admin group set-quota free --rpm 5
  sproxy admin group set-quota trial --daily 0          # remove daily limit

Natural language: "set quota", "update quota", "change limit", "set rate limit", "adjust limits"

---

### 2.4 Delete group

` + bq + `sproxy admin group delete <name> [--force]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--force` + bq + ` | Ungroup all members first, then delete. Without this flag deletion fails if group has users. |

Examples:
  sproxy admin group delete trial
  sproxy admin group delete old-group --force

Natural language: "delete group", "remove group", "drop group"

---

## 3. Token Management

### 3.1 Revoke all tokens for a user

` + bq + `sproxy admin token revoke <username>` + bq + `

Forces the user to log in again. All existing refresh tokens are invalidated.

Examples:
  sproxy admin token revoke alice

Natural language: "revoke tokens", "force logout", "invalidate session", "kick user out", "log out user"

---

## 4. Quota Inspection

### 4.1 Check quota status

` + bq + `sproxy admin quota status --user <username>` + bq + `
` + bq + `sproxy admin quota status --group <group-name>` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--user` + bq + ` | Show today + month usage vs limits for this user |
| ` + bq + `--group` + bq + ` | Show aggregate usage for all users in this group |

Output shows used tokens, configured limits, and status: OK / WARNING (>80%) / EXCEEDED.

Examples:
  sproxy admin quota status --user alice
  sproxy admin quota status --group enterprise

Natural language: "check quota", "quota status", "how many tokens used", "usage vs limit", "is user over quota"

---

## 5. Statistics

` + bq + `sproxy admin stats [--user <username>] [--days <n>] [--format text|json|csv]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--user` + bq + ` | all users | Filter statistics to a specific username |
| ` + bq + `--days` + bq + ` | 7 | Number of past days to include |
| ` + bq + `--format` + bq + ` | text | Output format: text (table), json, or csv |

Examples:
  sproxy admin stats
  sproxy admin stats --days 30
  sproxy admin stats --user alice --days 7
  sproxy admin stats --format json --days 1

Natural language: "usage stats", "token statistics", "how much has X used", "usage report", "token consumption"

---

## 6. Usage Log Management

### 6.1 Export logs

` + bq + `sproxy admin export [--format csv|json] [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--output <file>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--format` + bq + ` | csv | Export format: csv or json (NDJSON) |
| ` + bq + `--from` + bq + ` | 30 days ago | Start date inclusive (YYYY-MM-DD) |
| ` + bq + `--to` + bq + ` | today | End date inclusive (YYYY-MM-DD) |
| ` + bq + `--output` + bq + ` | pairproxy-export-<date>.csv | Output file path |

Examples:
  sproxy admin export
  sproxy admin export --format json --from 2025-01-01 --to 2025-01-31
  sproxy admin export --output /tmp/logs.csv

Natural language: "export logs", "download usage data", "export to CSV", "export usage report"

### 6.2 Purge old logs

` + bq + `sproxy admin logs purge --before <YYYY-MM-DD>` + bq + `
` + bq + `sproxy admin logs purge --days <n>` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--before` + bq + ` | Delete records with created_at before this date (exclusive) |
| ` + bq + `--days` + bq + ` | Delete records older than N days |

Examples:
  sproxy admin logs purge --before 2025-01-01
  sproxy admin logs purge --days 90

Natural language: "purge logs", "delete old logs", "clean up logs", "remove logs older than"

---

## 7. Audit Log

` + bq + `sproxy admin audit [--limit <n>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--limit` + bq + ` | 100 | Max number of recent records to display |

Displays: timestamp, operator, action, target, detail.

Examples:
  sproxy admin audit
  sproxy admin audit --limit 50

Natural language: "audit log", "admin history", "recent actions", "who did what", "operation log"

---

## 8. LLM Target Management

### 8.1 List LLM targets

` + bq + `sproxy admin llm targets` + bq + `

Lists all LLM targets defined in sproxy.yaml (URL, provider, weight, health status).
Shows target configuration and current health state.

Natural language: "list LLM targets", "show LLM backends", "what LLMs are configured", "show all targets"

---

### 8.2 Add LLM target

` + bq + `sproxy admin llm target add <url> --provider <provider> [--name <name>] [--weight <n>] [--api-key <key>] [--supported-models <models>] [--auto-model <model>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--provider` + bq + ` | (required) | Provider type: anthropic, openai, or ollama |
| ` + bq + `--name` + bq + ` | auto-generated | Human-readable name for the target |
| ` + bq + `--weight` + bq + ` | 1 | Load balancing weight (higher = more traffic) |
| ` + bq + `--api-key` + bq + ` | prompted | API key for the target (if required) |
| ` + bq + `--supported-models` + bq + ` | (optional) | Comma-separated list of supported models (e.g., claude-3-*,gpt-4-*) |
| ` + bq + `--auto-model` + bq + ` | (optional) | Model to use when client requests model="auto" |

Adds a new LLM target to the configuration. Changes require restart to take effect.

Examples:
  sproxy admin llm target add https://api.anthropic.com --provider anthropic --name "Anthropic Main" --weight 2 --supported-models "claude-3-*,claude-2.1" --auto-model "claude-3-sonnet-20250219"
  sproxy admin llm target add http://localhost:11434 --provider ollama --name "Local Ollama" --supported-models "mistral,llama2"
  sproxy admin llm target add https://api.openai.com --provider openai --api-key sk-xxx --supported-models "gpt-4-*,gpt-3.5-turbo" --auto-model "gpt-4-turbo"

Natural language: "add LLM target", "register new LLM", "add backend", "configure new LLM endpoint"

---

### 8.3 Update LLM target

` + bq + `sproxy admin llm target update <url> [--name <name>] [--weight <n>] [--api-key <key>] [--supported-models <models>] [--auto-model <model>]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--name` + bq + ` | Update the display name |
| ` + bq + `--weight` + bq + ` | Update load balancing weight |
| ` + bq + `--api-key` + bq + ` | Update API key |
| ` + bq + `--supported-models` + bq + ` | Update supported models list (comma-separated patterns) |
| ` + bq + `--auto-model` + bq + ` | Update model to use for auto mode requests |

Updates configuration for an existing LLM target. Changes require restart to take effect.

Examples:
  sproxy admin llm target update https://api.anthropic.com --weight 5 --supported-models "claude-3-*,claude-2.1"
  sproxy admin llm target update http://localhost:11434 --name "Ollama Production" --auto-model "mistral"
  sproxy admin llm target update https://api.openai.com --api-key sk-new-key --auto-model "gpt-4-turbo"

Natural language: "update LLM target", "change target weight", "update backend config", "modify LLM settings"

---

### 8.4 Delete LLM target

` + bq + `sproxy admin llm target delete <url> [--force]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--force` + bq + ` | Remove target even if users/groups are bound to it |

Removes an LLM target from the configuration. Without --force, deletion fails if
any users or groups are bound to this target. Changes require restart to take effect.

Examples:
  sproxy admin llm target delete https://old-api.example.com
  sproxy admin llm target delete http://localhost:11434 --force

Natural language: "delete LLM target", "remove backend", "unregister LLM", "drop target"

---

### 8.5 Enable LLM target

` + bq + `sproxy admin llm target enable <url>` + bq + `

Re-enables a previously disabled LLM target. The target will start receiving
traffic according to its weight. Takes effect immediately (no restart required).

Examples:
  sproxy admin llm target enable https://api.anthropic.com
  sproxy admin llm target enable http://localhost:11434

Natural language: "enable LLM target", "activate backend", "turn on target", "resume target"

---

### 8.6 Disable LLM target

` + bq + `sproxy admin llm target disable <url>` + bq + `

Temporarily disables an LLM target without removing it from configuration.
Disabled targets do not receive new requests. Existing requests are allowed
to complete. Takes effect immediately (no restart required).

Examples:
  sproxy admin llm target disable https://api.anthropic.com
  sproxy admin llm target disable http://localhost:11434

Natural language: "disable LLM target", "deactivate backend", "turn off target", "pause target"

---

### 8.7 List user/group bindings

` + bq + `sproxy admin llm list` + bq + `

Shows all user-to-LLM and group-to-LLM bindings stored in the database.

Natural language: "list LLM bindings", "show user assignments", "who is bound to what"

---

### 8.8 Bind user or group to an LLM target

` + bq + `sproxy admin llm bind <username> --target <url>` + bq + `
` + bq + `sproxy admin llm bind --group <group-name> --target <url>` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--target` + bq + ` | (required) LLM target URL, must match a URL in sproxy.yaml |
| ` + bq + `--group` + bq + ` | Bind a group instead of a specific user |

User-level bindings take priority over group-level bindings.

Examples:
  sproxy admin llm bind alice --target https://api.anthropic.com
  sproxy admin llm bind --group enterprise --target https://api.openai.com

Natural language: "bind user to LLM", "assign LLM", "route user to LLM", "pin user to backend"

---

### 8.9 Remove user LLM binding

` + bq + `sproxy admin llm unbind <username>` + bq + `

Removes the user-level binding. The user falls back to group binding or load balancer.

Examples:
  sproxy admin llm unbind alice

Natural language: "unbind LLM", "remove LLM binding", "unpin user"

---

### 8.10 Distribute users evenly across LLM targets

` + bq + `sproxy admin llm distribute` + bq + `

Round-robin assigns all active users to the configured LLM targets,
overwriting existing user-level bindings.

Natural language: "distribute users", "balance LLM load", "spread users across LLMs", "even distribution"

---

## 9. API Key Management

### 9.1 List API keys

` + bq + `sproxy admin apikey list` + bq + `

---

### 9.2 Add API key

` + bq + `sproxy admin apikey add <name> --value <key> [--provider anthropic|openai|ollama]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--value` + bq + ` | prompted | Plain-text API key value (encrypted at rest) |
| ` + bq + `--provider` + bq + ` | anthropic | LLM provider the key belongs to |

Examples:
  sproxy admin apikey add prod-key --value sk-ant-xxx --provider anthropic
  sproxy admin apikey add openai-key --provider openai   # prompts for value

Natural language: "add API key", "register API key", "store API key"

---

### 9.3 Assign API key to user or group

` + bq + `sproxy admin apikey assign <name> --user <username>` + bq + `
` + bq + `sproxy admin apikey assign <name> --group <group-name>` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--user` + bq + ` | Username to assign the key to |
| ` + bq + `--group` + bq + ` | Group name to assign the key to |

Examples:
  sproxy admin apikey assign prod-key --user alice
  sproxy admin apikey assign shared-key --group enterprise

Natural language: "assign API key", "give user an API key"

---

### 9.4 Revoke API key

` + bq + `sproxy admin apikey revoke <name>` + bq + `

Deactivates the key so it can no longer be used for upstream requests.

Examples:
  sproxy admin apikey revoke old-key

Natural language: "revoke API key", "deactivate key", "disable API key"

---

## 10. Database Backup & Restore

### 10.1 Backup

` + bq + `sproxy admin backup [--output <file>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--output` + bq + ` | <db-path>.bak | Destination backup file path |

Examples:
  sproxy admin backup
  sproxy admin backup --output /var/backups/pairproxy-2025-02-01.db

Natural language: "backup database", "backup DB", "create backup"

---

### 10.2 Restore

` + bq + `sproxy admin restore <backup-file>` + bq + `

WARNING: Stop sproxy before restoring. This overwrites the live database.
A pre-restore safety backup is created automatically before overwriting.

Examples:
  sproxy admin restore /var/backups/pairproxy-2025-02-01.db

Natural language: "restore database", "restore backup", "roll back database"

---

## 11. Configuration Validation

` + bq + `sproxy admin validate [--config <path>]` + bq + `

Loads and validates sproxy.yaml without starting the server. Reports missing
required fields, invalid values, and common misconfigurations.

Examples:
  sproxy admin validate
  sproxy admin validate --config /etc/pairproxy/sproxy.yaml

Natural language: "validate config", "check config", "test configuration", "is config valid"

---

## 12. Drain Mode Management (Rolling Upgrades)

Drain mode allows graceful shutdown of a node for zero-downtime rolling upgrades.
When a node is in drain mode, it stops accepting new requests but continues
processing existing requests.

### 12.1 Enter drain mode

` + bq + `sproxy admin drain enter` + bq + `

Puts the local node into drain mode. The node will:
- Stop accepting new requests from the load balancer
- Continue processing existing requests
- Update the cluster routing table to notify other nodes

Natural language: "enter drain mode", "drain node", "stop accepting traffic", "prepare for shutdown"

---

### 12.2 Exit drain mode (undrain)

` + bq + `sproxy admin drain exit` + bq + `

Returns the node to normal operation, accepting new traffic.

Natural language: "exit drain mode", "undrain", "resume traffic", "accept requests again"

---

### 12.3 Check drain status

` + bq + `sproxy admin drain status` + bq + `

Shows current drain state and active request count.

Output example:
  Status: DRAINING
  Active requests: 5
  Drain started: 2026-03-01T18:00:00Z

Natural language: "drain status", "check drain", "is node draining", "how many active requests"

---

### 12.4 Wait for drain completion

` + bq + `sproxy admin drain wait [--timeout <duration>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--timeout` + bq + ` | 0 (no limit) | Maximum time to wait (e.g., 60s, 5m) |

Blocks until active requests reach zero, then returns.
Useful for scripting rolling upgrades.

Examples:
  sproxy admin drain wait --timeout 120s
  sproxy admin drain wait --timeout 5m

Natural language: "wait for drain", "wait until no requests", "block until drained"

---

## 13. Conversation Tracking

Track individual users' LLM conversation content server-side.
Tracking is enabled/disabled per user via marker files. No UI or API is
required. Changes take effect immediately — no restart needed.

Tracked conversations are saved as JSON files:
  ` + bq + `<track.dir>/conversations/<username>/<timestamp>-<reqID>.json` + bq + `

track.dir defaults to ./track (relative to sproxy working directory).
In peer mode, set track.dir to a shared storage path (e.g. NFS mount) in
sproxy.yaml so that all nodes write to the same location and admin commands
take effect cluster-wide.

Each JSON file contains: request_id, username, timestamp, provider, model,
messages (input), response (assistant text), input_tokens, output_tokens.

### 13.1 Enable tracking for a user

` + bq + `sproxy admin track enable <username>` + bq + `

Starts recording all subsequent LLM conversations for this user.
Takes effect for new requests immediately (no restart).

Examples:
  sproxy admin track enable alice
  sproxy admin track enable bob

Natural language: "track user alice", "start recording alice", "enable conversation tracking for bob"

---

### 13.2 Disable tracking for a user

` + bq + `sproxy admin track disable <username>` + bq + `

Stops recording conversations. Existing records are preserved.

Examples:
  sproxy admin track disable alice

Natural language: "stop tracking alice", "disable tracking for bob", "stop recording"

---

### 13.3 List tracked users

` + bq + `sproxy admin track list` + bq + `

Shows all users currently with tracking enabled.

Natural language: "who is being tracked", "list tracked users", "show tracked users"

---

### 13.4 Show conversation records for a user

` + bq + `sproxy admin track show <username>` + bq + `

Lists all conversation record files for the user (newest first),
with file names (containing timestamp and request ID) and file sizes.
Also shows whether tracking is currently enabled or disabled.

Examples:
  sproxy admin track show alice

Natural language: "show alice's conversations", "list conversations for alice", "view tracked records"

---

### 13.5 Clear conversation records for a user

` + bq + `sproxy admin track clear <username>` + bq + `

Deletes all conversation JSON files for the user. The marker file
(tracking enable/disable state) is not affected.

Examples:
  sproxy admin track clear alice

Natural language: "clear alice's conversations", "delete tracked records", "wipe conversation history"

---

## 14. Bulk Import

` + bq + `sproxy admin import <file> [--dry-run]` + bq + `

Bulk-create groups and users from a template file. Groups and users that
already exist are silently skipped (idempotent).

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--dry-run` + bq + ` | false | Preview changes without writing to the database |
| ` + bq + `--config` + bq + ` | sproxy.yaml | Configuration file |

Template file format:

` + bq + bq + bq + `
# Comment lines start with #
# [group-name]              — declare a group section
# [group-name llm=URL]      — declare a group section with default LLM binding
# username password         — create user in the current group
# username password llm=URL — create user with individual LLM override
# [-]                       — users with no group
` + bq + bq + bq + `

Example template:

` + bq + bq + bq + `
[engineering llm=https://api.anthropic.com]
alice  Password123
bob    Password456 llm=https://api.openai.com

[marketing]
charlie  Marketing789

[-]
dave  NoGroup_Pass
` + bq + bq + bq + `

Import rules:
- Existing groups/users are skipped (original data preserved)
- Group LLM binding is only set if the group is newly created
- User-level ` + bq + `llm=URL` + bq + ` overrides only that user's binding
- Template file contains plaintext passwords — store or delete it securely after import

Examples:
  sproxy admin import users.txt
  sproxy admin import --dry-run users.txt
  sproxy admin import --config /etc/pairproxy/sproxy.yaml users.txt

Natural language: "import users from file", "bulk create users", "dry-run import",
"preview import", "batch create groups and users"

---

## 15. Semantic Route Management (v2.18.0)

Semantic routing narrows the LLM candidate pool based on request message
intent. Rules consist of a natural language description and target URLs.
The classifier LLM reuses the existing LB. Only applies to unbound users.

### 15.1 Add semantic route

` + bq + `sproxy admin route add <name> --description <text> --targets <url1,url2> [--priority <n>]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--description` + bq + ` | (required) | Natural language description for the classifier |
| ` + bq + `--targets` + bq + ` | (required) | Comma-separated target URLs |
| ` + bq + `--priority` + bq + ` | 0 | Rule priority (higher = more preferred) |

Examples:
  sproxy admin route add code_tasks --description "Code generation and debugging" --targets "https://api.anthropic.com,https://deepseek.example.com" --priority 10
  sproxy admin route add general_chat --description "General conversation" --targets "https://haiku.example.com"

Natural language: "add route", "create routing rule", "add semantic route"

---

### 15.2 List semantic routes

` + bq + `sproxy admin route list` + bq + `

Shows all semantic routing rules (ID, name, priority, active status, target URLs).

Natural language: "list routes", "show routing rules", "list semantic routes"

---

### 15.3 Update semantic route

` + bq + `sproxy admin route update <name> [--description <text>] [--targets <urls>] [--priority <n>]` + bq + `

| Flag | Description |
|------|-------------|
| ` + bq + `--description` + bq + ` | New description |
| ` + bq + `--targets` + bq + ` | New comma-separated target URLs |
| ` + bq + `--priority` + bq + ` | New priority |

Examples:
  sproxy admin route update code_tasks --description "Updated description"
  sproxy admin route update code_tasks --targets "https://api.anthropic.com" --priority 20

Natural language: "update route", "change routing rule", "modify semantic route"

---

### 15.4 Delete semantic route

` + bq + `sproxy admin route delete <name>` + bq + `

Removes the route rule from the database.

Examples:
  sproxy admin route delete code_tasks

Natural language: "delete route", "remove routing rule", "drop semantic route"

---

### 15.5 Enable / Disable semantic route

` + bq + `sproxy admin route enable <name>` + bq + `
` + bq + `sproxy admin route disable <name>` + bq + `

Disabled routes are not sent to the classifier. Existing rules are preserved.

Examples:
  sproxy admin route enable code_tasks
  sproxy admin route disable general_chat

Natural language: "enable route", "disable route", "activate routing rule", "deactivate routing rule"

---

## 16. Other Top-level Commands

### hash-password

` + bq + `sproxy hash-password` + bq + `

Interactively prompts for a password and prints its bcrypt hash.
Paste the output into admin.password_hash in sproxy.yaml.

Natural language: "generate password hash", "hash admin password"

---

### start

` + bq + `sproxy start [--config <path>] [--role primary|worker]` + bq + `

| Flag | Default | Description |
|------|---------|-------------|
| ` + bq + `--config` + bq + ` | sproxy.yaml | Configuration file |
| ` + bq + `--role` + bq + ` | from config | Override node role: primary or worker |

Natural language: "start server", "start sproxy", "run the proxy"

---

## Quick-reference Cheatsheet

| Natural language | Shell command |
|-----------------|---------------|
| Create user alice with password X | sproxy admin user add alice --password X  **[primary-only]** |
| List all users | sproxy admin user list |
| List users in group free | sproxy admin user list --group free |
| Disable user bob | sproxy admin user disable bob  **[primary-only]** |
| Enable user bob | sproxy admin user enable bob  **[primary-only]** |
| Reset alice's password | sproxy admin user reset-password alice --password <new>  **[primary-only]** |
| Move alice to group "premium" | sproxy admin user set-group alice --group premium  **[primary-only]** |
| Remove alice from her group | sproxy admin user set-group alice --ungroup  **[primary-only]** |
| Create group "free" (10k/day, 10 RPM) | sproxy admin group add free --daily-limit 10000 --rpm 10  **[primary-only]** |
| List all groups | sproxy admin group list |
| Update enterprise group to 1M daily | sproxy admin group set-quota enterprise --daily 1000000  **[primary-only]** |
| Delete group "trial" (force) | sproxy admin group delete trial --force  **[primary-only]** |
| Force-logout / revoke tokens for alice | sproxy admin token revoke alice  **[primary-only]** |
| Check alice's quota status | sproxy admin quota status --user alice |
| Check enterprise group quota | sproxy admin quota status --group enterprise |
| Show 30-day stats | sproxy admin stats --days 30 |
| Show alice's 7-day stats as JSON | sproxy admin stats --user alice --days 7 --format json |
| Export January 2025 logs to CSV | sproxy admin export --from 2025-01-01 --to 2025-01-31 |
| Export to specific file | sproxy admin export --output /tmp/logs.csv |
| Purge logs older than 90 days | sproxy admin logs purge --days 90  **[primary-only]** |
| Purge logs before 2025-01-01 | sproxy admin logs purge --before 2025-01-01  **[primary-only]** |
| Show last 50 audit entries | sproxy admin audit --limit 50 |
| List configured LLM targets | sproxy admin llm targets |
| Add new LLM target | sproxy admin llm target add <url> --provider anthropic --name "Main"  **[primary-only]** |
| Update LLM target weight | sproxy admin llm target update <url> --weight 5  **[primary-only]** |
| Delete LLM target | sproxy admin llm target delete <url>  **[primary-only]** |
| Enable LLM target | sproxy admin llm target enable <url>  **[primary-only]** |
| Disable LLM target | sproxy admin llm target disable <url>  **[primary-only]** |
| List LLM bindings | sproxy admin llm list |
| Bind alice to LLM URL | sproxy admin llm bind alice --target <url>  **[primary-only]** |
| Bind group enterprise to LLM | sproxy admin llm bind --group enterprise --target <url>  **[primary-only]** |
| Unbind alice's LLM | sproxy admin llm unbind alice  **[primary-only]** |
| Distribute all users evenly | sproxy admin llm distribute  **[primary-only]** |
| List API keys | sproxy admin apikey list |
| Add Anthropic API key | sproxy admin apikey add prod --value sk-xxx --provider anthropic  **[primary-only]** |
| Assign API key to alice | sproxy admin apikey assign prod --user alice  **[primary-only]** |
| Revoke API key | sproxy admin apikey revoke old-key  **[primary-only]** |
| Backup database | sproxy admin backup --output backup.db  **[primary-only]** |
| Restore database from file | sproxy admin restore backup.db  **[primary-only]** |
| Validate configuration file | sproxy admin validate |
| Enter drain mode | sproxy admin drain enter  **[primary-only]** |
| Exit drain mode | sproxy admin drain exit  **[primary-only]** |
| Check drain status | sproxy admin drain status |
| Wait for drain completion | sproxy admin drain wait --timeout 60s |
| Enable conversation tracking for alice | sproxy admin track enable alice |
| Disable conversation tracking for alice | sproxy admin track disable alice |
| List all tracked users | sproxy admin track list |
| Show alice's conversation records | sproxy admin track show alice |
| Clear alice's conversation records | sproxy admin track clear alice |
| Import groups/users from file | sproxy admin import users.txt  **[primary-only]** |
| Preview import without writing | sproxy admin import --dry-run users.txt |
| Add semantic route | sproxy admin route add code_tasks --description "Code gen" --targets "https://api.anthropic.com" --priority 10  **[primary-only]** |
| List semantic routes | sproxy admin route list |
| Update semantic route | sproxy admin route update code_tasks --description "New desc"  **[primary-only]** |
| Delete semantic route | sproxy admin route delete code_tasks  **[primary-only]** |
| Enable semantic route | sproxy admin route enable code_tasks  **[primary-only]** |
| Disable semantic route | sproxy admin route disable code_tasks  **[primary-only]** |
| Hash a new admin password | sproxy hash-password |
| List all target sets | sproxy admin targetset list |
| Create a target set | sproxy admin targetset create <name> --group <group> --strategy weighted_random |
| Delete a target set | sproxy admin targetset delete <name> |
| Show target set details | sproxy admin targetset show <name> |
| Add target to set | sproxy admin targetset add-target <set_name> --url <url> --weight 2 |
| Remove target from set | sproxy admin targetset remove-target <set_name> --url <url> |
| Set target weight | sproxy admin targetset set-weight <set_name> --url <url> --weight 3 |
| List active alerts | sproxy admin alert list |
| List alerts by target | sproxy admin alert list --target https://api.anthropic.com |
| View alert history | sproxy admin alert history --days 7 |
| Resolve an alert | sproxy admin alert resolve <alert_id> |
| View alert statistics | sproxy admin alert stats --days 30 |
`
