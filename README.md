# Navidrome Play it again

A [Navidrome](https://www.navidrome.org) plugin that manages two playlists for you:

- **Play Later** — Add albums you want to hear. As you listen, the plugin tracks which tracks you've played. Once you've played enough of an album, it's automatically removed so the playlist stays fresh.
- **Forgotten Records** — A rotating collection of random complete albums you haven't played in a while. Rebuilt automatically on a schedule, with the same threshold-based removal as Play Later.

## How it works

### Play Later

Add full albums to a playlist called **Play Later** using any Subsonic-compatible client (Psysonic, Narjo, etc.). As you listen, the plugin tracks which tracks from each album you've actually played (via scrobble events, which fire after ~90% of a track is completed). Once the number of distinct tracks you've played from an album reaches a configurable percentage of the album's total tracks, all of that album's tracks are removed from the playlist automatically.

Play counts accumulate across sessions — tracks played on different days all count toward the threshold.

### Forgotten Records

The plugin maintains a **Forgotten Records** playlist — a rotating collection of random complete albums you haven't played in a while. The playlist is rebuilt automatically on a schedule (default: daily). On each rebuild, the plugin:

1. Fetches all albums and sorts them by last-played timestamp (never-played first, then least-recently-played).
2. Takes a candidate pool of `Albums per Rebuild × Pool Multiplier` albums from the top of that list.
3. Randomly selects `Albums per Rebuild` albums from the pool.
4. Fully replaces the playlist with all tracks from the selected albums.

As you listen, albums are removed once you've played enough tracks (same threshold as Play Later), so fresh albums take their place on the next rebuild.

The playlist is created automatically if it doesn't exist — no manual setup needed.

### Configuration

All options are adjustable in Navidrome → Settings → Plugins after installation.

| Option                                  | Default             | Description                                                                                                 |
| --------------------------------------- | ------------------- | ----------------------------------------------------------------------------------------------------------- |
| Enable Play Later                       | on                  | Master switch for the Play Later feature. Turn off to leave the playlist untouched                          |
| Play Later Playlist Name                | `Play Later`        | Name of the Play Later playlist to watch (case-insensitive)                                                 |
| Album Removal Threshold (%)             | `30`                | Percentage of distinct tracks that must be scrobbled before an album is removed from Play Later             |
| Enable Forgotten Records                | on                  | Master switch for the Forgotten Records feature. Turn off to disable scheduled and scrobble-driven rebuilds |
| Forgotten Records Playlist Name         | `Forgotten records` | Name of the Forgotten Records playlist (auto-created)                                                       |
| Albums per Rebuild                      | `8`                 | Number of complete albums to include in each rebuild                                                        |
| Pool Multiplier                         | `3`                 | Pool size = Albums per Rebuild × multiplier; random selection picks from this pool                          |
| Forgotten Records Removal Threshold (%) | `30`                | Percentage of tracks that must be scrobbled before removal from Forgotten Records                           |
| Rebuild Schedule (Cron)                 | `0 0 * * *`         | Cron expression for rebuild (requires Navidrome restart to change)                                          |

A threshold of 30% means: for a 10-track album, playing any 3 distinct tracks removes it. Setting it to 1 removes the album after the very first track; 100 requires every track.

## Logging

The plugin logs through Navidrome's native plugin logging (the Extism `pdk.Log` host function), so output goes to the standard Navidrome log stream — the same place as the rest of Navidrome's logs (e.g. `docker logs`). There is no per-plugin logging toggle.

Verbosity is controlled by the server's `[Plugins] LogLevel` setting (for example `LogLevel = "debug"` in `navidrome.toml` under `[Plugins]` to see everything). Lower the level (`info`/`warn`) to reduce output.

What the plugin logs:

- **Progress** — each time a track from a watched album is scrobbled, showing how many distinct tracks have been played versus how many are needed.
- **Removal** — when the threshold is reached and an album is removed.
- **Rebuild** — when the Forgotten Records playlist is rebuilt, showing how many albums and tracks were added.
- **Errors** — any API or storage failure.

Example entries (the host adds the timestamp and plugin context):

```
scrobble received: user=bob track="So What" album="Kind of Blue"
user=bob album="Kind of Blue": 2/6 tracks played, need 2 to remove (Play Later)
user=bob album="Kind of Blue": threshold reached, removing 6 tracks from "Play Later"
user=bob album="Kind of Blue": successfully removed from "Play Later"
fr: rebuild callback received
fr: rebuilt playlist "Forgotten records" for user bob with 8 albums (87 tracks)
```

## Installation

### Play Later

1. Copy `play-it-again.ndp` to your Navidrome plugins folder (e.g. `/data/plugins/`).
2. In Navidrome → Settings → Plugins, enable the **Play it again** plugin.
3. Assign your user account to the plugin (required so Navidrome routes your scrobble events to it).
4. In your client, create a playlist whose name matches the configured **Play Later Playlist Name**.
5. Add albums to the playlist as you normally would.

### Forgotten Records

1. After enabling the plugin (steps 1–3 above), the Forgotten Records playlist is created automatically on the first scheduled rebuild.
2. The first rebuild also runs immediately when the plugin is enabled (or Navidrome is restarted), so you don't have to wait for the cron schedule.
3. Optionally adjust the Forgotten Records config options (playlist name, album count, pool multiplier, threshold, cron schedule) in Navidrome → Settings → Plugins.
4. Changing the **Rebuild Schedule** requires a Navidrome restart (not hot-reload) to take effect.
5. To manually refresh the playlist at any time, disable and re-enable the plugin in Navidrome → Settings → Plugins. This triggers an immediate rebuild.

## Repository files

| File                | Purpose                                                                |
| ------------------- | ---------------------------------------------------------------------- |
| `main.go`           | Plugin source code — all logic lives here                              |
| `manifest.json`     | Plugin metadata: name, permissions, and configuration schema           |
| `go.mod` / `go.sum` | Go module files, pinning the Navidrome plugin PDK and its dependencies |
| `Makefile`          | Build and packaging rules                                              |
| `plugin.wasm`       | Compiled WebAssembly binary (build artefact, not checked in)           |
| `play-it-again.ndp` | Packaged plugin ready for deployment (build artefact, not checked in)  |
| `SPEC.md`           | Design specification written before implementation                     |

## Building

**Prerequisites:**

| Tool                  | Version         | Notes                                                   |
| --------------------- | --------------- | ------------------------------------------------------- |
| Go                    | 1.25 or later   | For dependency management only (`go mod tidy`)          |
| TinyGo                | 0.41.1 or later | Required for the actual WASM build                      |
| Binaryen (`wasm-opt`) | any             | Required by TinyGo; install via `brew install binaryen` |

TinyGo is required because Navidrome's plugin runtime (Extism/wazero) expects a WASM **reactor module** — one that exports `_initialize` and never calls `proc_exit`. Standard Go's `wasip1` target produces a command module (`_start`, calls `proc_exit`), which causes the module instance to be closed before any plugin function can be invoked. TinyGo with `-buildmode=c-shared` produces the correct reactor format.

Install TinyGo 0.41.1 for macOS (Apple Silicon):

```bash
cd /tmp
curl -L -o tinygo.tar.gz https://github.com/tinygo-org/tinygo/releases/download/v0.41.1/tinygo0.41.1.darwin-arm64.tar.gz
sudo tar -C /usr/local -xzf tinygo.tar.gz
```

Then build:

```bash
# First time only — fetch dependencies
go mod tidy

# Build plugin.wasm and package play-it-again.ndp
make
```

The Makefile compiles `main.go` using TinyGo (`-target wasip1 -buildmode=c-shared`) and then zips `manifest.json` and `plugin.wasm` into `play-it-again.ndp`. The resulting `.ndp` is around 440 KB compressed.

To clean build artefacts:

```bash
make clean
```

## Technical notes

The plugin uses three Navidrome plugin capabilities:

- **Scrobbler** — receives per-play events to drive the threshold-based removal logic for both Play Later and Forgotten Records.
- **Scheduler** — fires the periodic callback that rebuilds the Forgotten Records playlist.
- **Lifecycle** — registers the recurring schedule when the plugin loads.

It calls back into Navidrome via the internal Subsonic API to read and modify playlists, and stores per-user play-count state in the plugin's persistent key-value store. Play Later and Forgotten Records each maintain independent play-count state using separate KVStore key prefixes (`played:` vs `fr:played:`).
