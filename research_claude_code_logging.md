# Research: Persisting Claude Code Conversation Output

**Date**: 2026-03-23
**Goal**: Searchable, grep-friendly log of all Claude Code interactions that survives crashes, Ctrl-C, and network drops.

---

## 1. Claude Code Internal Storage (JSONL) -- ALREADY EXISTS

Claude Code **already persists every conversation** to disk as JSONL files, written incrementally (line-by-line), so they survive crashes and Ctrl-C.

### Where conversations live

```
~/.claude/projects/<encoded-project-path>/<session-uuid>.jsonl
```

For this project specifically:
```
~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl
```

### JSONL format

Each line is a JSON object with a `type` field. Key types:
- `user` -- Human messages. Content at `.message.content` (string)
- `assistant` -- Claude responses. Content at `.message.content` (array of objects: `text`, `tool_use`, `thinking`)
- `system` -- System prompts
- `progress` -- Streaming progress (can be ignored)
- `queue-operation` -- Enqueue/dequeue ops
- `file-history-snapshot` -- File snapshots

### How to grep them directly

```bash
# Search all conversations for a keyword
grep -l "power button" ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl

# Extract human messages containing a term
cat ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl | \
  python3 -c "import sys,json; [print(json.loads(l).get('message',{}).get('content','')) for l in sys.stdin if '\"type\":\"user\"' in l]" | \
  grep -i "power"
```

### Converter script

See `claude_jsonl_to_markdown.py` (created alongside this document). Usage:
```bash
# Convert a specific session
python3 claude_jsonl_to_markdown.py ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/a8244a53*.jsonl

# Convert all sessions for this project
python3 claude_jsonl_to_markdown.py ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl

# Output to a specific directory
python3 claude_jsonl_to_markdown.py --outdir ~/claude-logs ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl
```

### Resume/browse sessions from CLI

```bash
# Interactive session picker (searches by name/content)
claude --resume

# Resume with search term
claude --resume "power button"

# Continue most recent session in current directory
claude --continue
```

### Verdict: BEST option for structured, searchable logs
- Already happening automatically
- Survives crashes (append-only JSONL)
- Contains full content (user, assistant, tool calls, tool results)
- Grepable with the converter script or direct JSON parsing

---

## 2. VS Code Claude Code Extension

### Storage location

The VS Code extension for Claude Code uses the same `~/.claude/` storage as the CLI. The extension launches Claude Code as a subprocess, so all conversations go to the same JSONL files described above.

There is no separate VS Code-specific export or history browser beyond `claude --resume`.

### VS Code Output Channel

Claude Code in VS Code does NOT write to a standard VS Code Output Channel that could be captured. The conversation is rendered in the terminal panel or a webview, not an output channel.

---

## 3. VS Code Terminal Scrollback/Logging

### Scrollback buffer

```jsonc
// settings.json
{
  "terminal.integrated.scrollback": 10000  // default is 1000 lines
  // maximum is technically unlimited but high values use more memory
}
```

**Problem**: Scrollback is in-memory only. It is lost when the terminal is closed, VS Code restarts, or crashes. Not a persistence solution.

### VS Code extensions for terminal logging

There are no well-maintained, popular VS Code extensions that automatically log terminal output to files. The few that existed (like "Terminal Logger") have been abandoned. This is a dead end.

### VS Code "Send Terminal Output to File"

VS Code does not have a built-in "save terminal output to file" feature. You can select text in the terminal and copy it, but that is manual and incomplete.

---

## 4. iTerm2 Session Logging -- GOOD for raw terminal capture

### How to enable

iTerm2 > Preferences > Profiles > Session:
- Check **"Automatically log session input to"** and/or **"Automatically log session output to"**
- Set a directory (e.g., `~/iterm-logs/`)

Or via `defaults`:
```bash
# Check current state (0 = disabled)
defaults read com.googlecode.iterm2 AutoLog
# Currently: 0 (disabled on your system)
```

### What it captures

- Raw terminal output including ANSI escape codes
- Written in real-time (survives crashes, Ctrl-C, network drops)
- One file per session, named with timestamp and profile name

### Stripping ANSI codes for grep-friendliness

The raw logs contain ANSI escape codes (colors, cursor movement). To make them grep-friendly:

```bash
# Using sed (built-in)
sed 's/\x1b\[[0-9;]*[mGKHJ]//g' logfile.txt

# Using col (built-in on macOS)
col -b < logfile.txt > clean.txt

# Using ansifilter (install via brew)
brew install ansifilter
ansifilter logfile.txt > clean.txt

# Using perl (one-liner, handles most codes)
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' logfile.txt
```

### Auto-cleanup wrapper in .zshrc

```bash
# Add to ~/.zshrc -- auto-strip ANSI from iTerm logs nightly
alias clean-iterm-logs='for f in ~/iterm-logs/*.txt; do
  perl -pe "s/\e\[[0-9;]*[a-zA-Z]//g" "$f" > "${f%.txt}.clean.txt"
done'
```

### Verdict: GOOD but noisy
- Captures everything including ANSI art, progress bars, spinner animations
- Requires post-processing to be grep-friendly
- Does not understand conversation structure (just raw bytes)
- Survives all failures (kernel-level write-through)

---

## 5. `script` Command -- SIMPLE and reliable

The macOS `script` command captures all terminal output to a file.

### Basic usage

```bash
# Start logging, then run claude inside
script -a ~/claude-logs/$(date +%Y%m%d-%H%M%S).log
claude
# When done: exit (or Ctrl-D) to stop logging
```

### Wrapper alias

```bash
# Add to ~/.zshrc
claude-logged() {
  local logfile=~/claude-logs/$(date +%Y%m%d-%H%M%S)-claude.log
  mkdir -p ~/claude-logs
  echo "Logging to: $logfile"
  script -a "$logfile" claude "$@"
}
```

### Crash behavior

- `script` writes through a pty -- output is flushed to disk frequently
- If Claude Code crashes or you Ctrl-C, the log file up to that point is preserved
- If the parent shell is killed (kill -9), the log survives because `script` uses a child process with its own pty
- Network drops: survives (local logging, no network dependency)

### Stripping ANSI

Same as iTerm2 -- the log contains raw terminal codes. Use `col -b` or `perl` to strip.

### Verdict: GOOD, simple, requires wrapper command
- Must remember to use `claude-logged` instead of `claude`
- Or use a shell function that always wraps claude in script

---

## 6. tmux Logging -- BEST for "always on" terminal capture

tmux is already installed on your system (v3.6a).

### Method 1: pipe-pane (built-in, no plugin needed)

```bash
# Inside a tmux session, start logging the current pane:
tmux pipe-pane -o 'cat >> ~/claude-logs/tmux-$(date +%Y%m%d-%H%M%S).log'

# Stop logging:
tmux pipe-pane
```

### Method 2: Auto-log via .tmux.conf

```bash
# Add to ~/.tmux.conf
# Auto-start logging for every new pane
set-hook -g after-new-window 'pipe-pane -o "cat >> ~/claude-logs/tmux-%Y%m%d-%H%M%S-#{session_name}-#{window_index}-#{pane_index}.log"'
set-hook -g after-split-window 'pipe-pane -o "cat >> ~/claude-logs/tmux-%Y%m%d-%H%M%S-#{session_name}-#{window_index}-#{pane_index}.log"'
```

### Method 3: tmux-logging plugin

```bash
# Install TPM (Tmux Plugin Manager) if not already installed
git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm

# Add to ~/.tmux.conf
set -g @plugin 'tmux-plugins/tmux-logging'
set -g @logging-path "$HOME/claude-logs"
set -g @logging-filename "tmux-%Y%m%d-%H%M%S.log"

# Key bindings (defaults):
# prefix + shift-p  : Toggle logging of current pane
# prefix + alt-p    : Save complete pane history (screen capture)
# prefix + alt-shift-p : Save complete history + screen capture
```

### Crash behavior

- `pipe-pane` writes continuously to the file -- survives Claude crashes, Ctrl-C, network drops
- If tmux server itself is killed, the log up to that point is preserved
- tmux server survives terminal emulator crashes (it runs as a daemon)
- Even SSH disconnects are survived -- tmux keeps running

### Verdict: EXCELLENT
- tmux survives everything except system reboot
- Logging is automatic once configured
- Can be combined with the JSONL approach for both raw and structured logs

---

## 7. Recommended Setup (Layered Approach)

### Layer 1: Claude Code JSONL (structured, already free)

The JSONL files are already being written. Use the converter script to export them to readable markdown when needed:

```bash
# One-time: add alias to .zshrc
alias claude-export='python3 ~/git/VOL20toGenelecGLM/claude_jsonl_to_markdown.py'

# Usage
claude-export ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl --outdir ~/claude-logs/markdown/
```

### Layer 2: tmux + pipe-pane (raw terminal, crash-proof)

```bash
# Add to ~/.zshrc -- auto-log when inside tmux
if [ -n "$TMUX" ]; then
  _claude_log_dir="$HOME/claude-logs/raw"
  mkdir -p "$_claude_log_dir"
  # Auto-enable pipe-pane logging
  tmux pipe-pane -o "cat >> $_claude_log_dir/tmux-$(date +%Y%m%d-%H%M%S)-#{pane_id}.log"
fi
```

### Layer 3: Periodic ANSI-stripping cron job

```bash
# Crontab entry: strip ANSI codes from raw logs every hour
0 * * * * find ~/claude-logs/raw -name '*.log' -newer ~/claude-logs/.last-clean -exec sh -c 'perl -pe "s/\e\[[0-9;]*[a-zA-Z]//g" "$1" > "${1%.log}.clean.txt"' _ {} \; && touch ~/claude-logs/.last-clean
```

### Searching across everything

```bash
# Search structured conversation content
grep -rl "power button" ~/.claude/projects/

# Search raw terminal logs
grep -rl "power button" ~/claude-logs/raw/*.clean.txt

# Search exported markdown
grep -rl "power button" ~/claude-logs/markdown/
```

---

## Summary Comparison

| Approach | Survives Crash | Grep-friendly | Auto | Structured | Setup |
|----------|---------------|---------------|------|------------|-------|
| **JSONL (built-in)** | Yes | With script | Yes | Yes | None needed |
| VS Code scrollback | No | No | N/A | No | N/A |
| VS Code extensions | N/A | N/A | N/A | N/A | None exist |
| **iTerm2 auto-log** | Yes | After strip | Yes | No | Toggle in prefs |
| **`script` command** | Yes | After strip | No (wrapper) | No | Alias |
| **tmux pipe-pane** | Yes | After strip | Yes | No | .tmux.conf |
| **JSONL + converter** | Yes | Yes | Yes | Yes | One script |

**Winner**: JSONL converter (Layer 1) for structured searchable content + tmux pipe-pane (Layer 2) for raw crash-proof capture.
