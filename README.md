# File Age Analyzer

File Age Analyzer helps you find old files in a folder, review them in a visual list, and delete only the ones you choose.

It opens as a keyboard-based app in your terminal.

## What This Tool Does

- Scans a folder you choose (it does **not** auto-scan your current folder)
- Finds files older than an age you set (days, weeks, months, or years)
- Lets you preview deletions safely with **Dry-Run** mode (on by default)
- Lets you select individual files before deleting
- Supports ignore rules using `.file-age-analyzerignore`

## Safety First

- Default mode is `dry-run=true`, so deletions are preview only.
- Turn off dry-run (`t` key) only when you are ready to permanently delete files.
- The app skips hidden/system-like entries (for example names starting with `.` on non-Windows systems).

## Install / Build

Requirements:

- Go installed (version from `go.mod`)

Build:

```bash
go build -o file-age-analyzer file-age-analyzer.go
```

## Quick Start

1. Run the app:

```bash
./file-age-analyzer
```

2. Type the folder path you want to check (the path field is focused first).
3. Set depth if needed (`0` means only that folder, no subfolders).
4. Review old files in the list.
5. Press `Space` to select files.
6. Press `d` to continue to confirmation.
7. If dry-run is ON, this is a preview. Toggle with `t` when you want real deletion.

## Command Options

```bash
./file-age-analyzer [flags]
```

If `-path` is not provided, the app waits for you to enter a folder path.

| Flag | Default | Meaning |
|------|---------|---------|
| `-path` | `""` | Starting folder to scan |
| `-age` | `1` | Files older than this number |
| `-unit` | `years` | Age unit: `days`, `weeks`, `months`, `years` |
| `-depth` | `3` | Max folder depth (`0` = only the selected folder) |
| `-dry-run` | `true` | Preview only; do not actually delete |
| `-ext` | `""` | File extensions to include (comma-separated, like `log,tmp,bak`) |

## Keyboard Shortcuts

General:

- `Tab`: move focus (`path -> depth -> list`)
- `Shift+Tab`: move focus backward
- `Enter` / `Esc`: leave current input field
- `q` or `Esc` (in list): quit

List navigation:

- `↑` / `k`: move up
- `↓` / `j`: move down
- `g` / `Home`: jump to top
- `G` / `End`: jump to bottom
- `Ctrl+D`: half-page down
- `Ctrl+U`: half-page up

Selection and delete:

- `Space` or `Enter`: select/unselect current file
- `a`: select all
- `A`: toggle all (if any selected, it clears all)
- `d`: delete selected (with confirmation)
- `t`: toggle dry-run mode

Filters and sorting:

- `+` / `-`: increase/decrease age threshold
- `u`: switch unit (`days -> weeks -> months -> years`)
- `s`: change sort field (`age -> size -> name`)
- `S`: reverse sort direction(`ascending / descending`)
- `r`: rescan now

Help and ignore rules:

- `h` or `?`: open help
- `i`: open ignore file editor

## Ignore Rules (`.file-age-analyzerignore`)

Place `.file-age-analyzerignore` inside the folder you scan.

You can also press `i` inside the app to edit and save it.

Rules:

- Empty lines are ignored
- `#` starts a comment
- `*` matches any characters except `/`
- `?` matches one character except `/`
- Pattern without `/`: match by name anywhere under scan folder
- Pattern with `/`: match relative paths under scan folder
- Pattern starting with `/`: match only from scan folder root
- Pattern ending with `/`: ignore that folder and everything inside it

Example:

```text
# Temporary files
*.tmp
*.bak

# Folders to skip anywhere
node_modules/
cache/

# Root-only file
/archive.zip

# Specific nested path
exports/daily/*.csv
```

## Practical Examples

Start and choose a folder in the app:

```bash
./file-age-analyzer
```

Start with a specific folder and deeper scan:

```bash
./file-age-analyzer -path="$HOME/Downloads" -depth=5
```

Find log files older than 6 months:

```bash
./file-age-analyzer -path=/var/log -unit=months -age=6 -ext=log
```

Only scan the selected folder (no subfolders):

```bash
./file-age-analyzer -path="$HOME/Documents" -depth=0
```
