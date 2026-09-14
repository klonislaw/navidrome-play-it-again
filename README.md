---
title: README
type: note
permalink: play-it-again/readme
---

# Navidrome Play it again

A simple [Navidrome](https://www.navidrome.org) plugin that I made to scratch my own itch and help me rediscover albums in dusty corners of my collection. It focuses on complete albums since this is how I prefer to listen to music.

It revolves around 2 playlists.

- **Play Later** — Playlist to track albums you want listen to later. As you listen, the plugin tracks which tracks you've played. Once you've played enough of an album, it's automatically removed. I use it as a music to-do list of sorts. From the playlist I go to the album and play it from there.
- **Forgotten Records** — A rotating collection of random complete albums you haven't played in a while. It is refreshed on a regular schedule and albums get removed as you play them.

## AI coding alert

For full transparency: all the code, as well as the readme from the next section onward, was written using AI. Given that Navidrom plugins use Go (which I never used before) and plugins are explicitly limited in what they are allowed to do, this was a suitable solution for me.

## How it works

### Play Later

Add full albums to a playlist called **Play Later** or what other name you set using Navidrome or a Subsonic-compatible client. As you listen, the plugin tracks which tracks from each album you've actually played (via scrobble events). Once the number of tracks you've played from an album reaches a configurable percentage of the album's total tracks, all of that album's tracks are removed from the playlist automatically.

### Forgotten Records

The plugin maintains a **Forgotten Records** playlist: a rotating collection of random complete albums you haven't played in a while. The playlist is rebuilt automatically on a schedule (default: daily). On each rebuild, the plugin:

1. Fetches all albums and sorts them by last-played timestamp (never-played first, then least-recently-played).
2. Takes a candidate pool of `Album pool size` albums from the top of that list.
3. Randomly selects `Albums per Rebuild` albums from the pool.
4. Fully replaces the playlist with all tracks from the selected albums.

As you listen, albums are removed once you've played enough tracks (same threshold as Play Later), so fresh albums take their place on the next rebuild.

The playlist is created automatically if it doesn't exist.

When Forgotten Records is enabled, the playlist is rebuilt every time the plugin is turned on (including a Navidrome restart). So a quick way to refresh it is to toggle the plugin off and back on in Navidrome → Settings → Plugins — no need to wait for the next scheduled rebuild.

### Configuration

All options are adjustable in Navidrome → Settings → Plugins after installation.

| Option                                  | Default             | Description                                                                                          |
| --------------------------------------- | ------------------- | ---------------------------------------------------------------------------------------------------- |
| Enable Play Later                       | on                  | Switch for the Play Later feature. Turn off to keep played albums in the playlist                    |
| Play Later Playlist Name                | `Play Later`        | Name of the Play Later playlist to watch (case-insensitive)                                          |
| Album Removal Threshold (%)             | `30`                | Percentage of distinct tracks that must be scrobbled before an album is removed from Play Later      |
| Enable Forgotten Records                | on                  | Switch for the Forgotten Records feature. Turn off to disable scheduled and scrobble-driven rebuilds |
| Forgotten Records Playlist Name         | `Forgotten records` | Name of the Forgotten Records playlist (auto-created)                                                |
| Albums per Rebuild                      | `8`                 | Number of complete albums to include in each rebuild                                                 |
| Album pool size                         | `50`                | Number of least-recently-played albums the rebuild randomly selects from                             |
| Forgotten Records Removal Threshold (%) | `30`                | Percentage of tracks that must be scrobbled before removal from Forgotten Records                    |
| Rebuild Schedule (Cron)                 | `0 0 * * *`         | Cron expression for rebuild (requires Navidrome restart to change)                                   |

A threshold of 30% means: for a 10-track album, playing any 3 distinct tracks removes it. Setting it to 1 removes the album after the very first track; 100 requires every track.

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
3. Optionally adjust the Forgotten Records config options (playlist name, album count, album pool size, threshold, cron schedule) in Navidrome → Settings → Plugins.
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

```
cd /tmp
curl -L -o tinygo.tar.gz https://github.com/tinygo-org/tinygo/releases/download/v0.41.1/tinygo0.41.1.darwin-arm64.tar.gz
sudo tar -C /usr/local -xzf tinygo.tar.gz
```

Then build:

```
# First time only — fetch dependencies
go mod tidy

# Build plugin.wasm and package play-it-again.ndp
make
```

The Makefile compiles `main.go` using TinyGo (`-target wasip1 -buildmode=c-shared`) and then zips `manifest.json` and `plugin.wasm` into `play-it-again.ndp`. The resulting `.ndp` is around 440 KB compressed.

To clean build artefacts:

```
make clean
```

## README
---
title: README
type: note
permalink: play-it-again/readme
---

# Navidrome Play it again

A simple [Navidrome](https://www.navidrome.org) plugin that I made to scratch my own itch and help me rediscover albums in dusty corners of my collection. It focuses on complete albums since this is how I prefer to listen to music.

It revolves around 2 playlists.

- **Play Later** — Playlist to track albums you want listen to later. As you listen, the plugin tracks which tracks you've played. Once you've played enough of an album, it's automatically removed. I use it as a music to-do list of sorts. From the playlist I go to the album and play it from there.
- **Forgotten Records** — A rotating collection of random complete albums you haven't played in a while. It is refreshed on a regular schedule and albums get removed as you play them.

## AI coding alert

For full transparency: all the code, as well as the readme from the next section onward, was written using AI. Given that Navidrom plugins use Go (which I never used before) and plugins are explicitly limited in what they are allowed to do, this was a suitable solution for me.

## How it works

### Play Later

Add full albums to a playlist called **Play Later** or what other name you set using Navidrome or a Subsonic-compatible client. As you listen, the plugin tracks which tracks from each album you've actually played (via scrobble events). Once the number of tracks you've played from an album reaches a configurable percentage of the album's total tracks, all of that album's tracks are removed from the playlist automatically.

### Forgotten Records

The plugin maintains a **Forgotten Records** playlist: a rotating collection of random complete albums you haven't played in a while. The playlist is rebuilt automatically on a schedule (default: daily). On each rebuild, the plugin:

1. Fetches all albums and sorts them by last-played timestamp (never-played first, then least-recently-played).
2. If genre filters are configured, only albums matching at least one of the selected genres are included.
3. Takes a candidate pool of `Album pool size` albums from the top of that list.
4. Randomly selects `Albums per Rebuild` albums from the pool.
5. Fully replaces the playlist with all tracks from the selected albums.

You can filter albums by up to 5 genres in the plugin config. Leave all empty to include all genres (default behavior). Genre matching is case-insensitive.

As you listen, albums are removed once you've played enough tracks (same threshold as Play Later), so fresh albums take its place on the next rebuild.

The playlist is created automatically if it doesn't exist.

When Forgotten Records is enabled, the playlist is rebuilt every time the plugin is turned on (including a Navidrome restart). So a quick way to refresh it is to toggle the plugin off and back on in Navidrome → Settings → Plugins — no need to wait for the next scheduled rebuild.

### Configuration

All options are adjustable in Navidrome → Settings → Plugins after installation.

| Option                                  | Default             | Description                                                                                          |
| --------------------------------------- | ------------------- | ---------------------------------------------------------------------------------------------------- |
| Enable Play Later                       | on                  | Switch for the Play Later feature. Turn off to keep played albums in the playlist                    |
| Play Later Playlist Name                | `Play Later`        | Name of the Play Later playlist to watch (case-insensitive)                                          |
| Album Removal Threshold (%)             | `30`                | Percentage of distinct tracks that must be scrobbled before an album is removed from Play Later      |
| Enable Forgotten Records                | on                  | Switch for the Forgotten Records feature. Turn off to disable scheduled and scrobble-driven rebuilds |
| Forgotten Records Playlist Name         | `Forgotten records` | Name of the Forgotten Records playlist (auto-created)                                                |
| Albums per Rebuild                      | `8`                 | Number of complete albums to include in each rebuild                                                 |
| Album pool size                         | `50`                | Number of least-recently-played albums the rebuild randomly selects from                             |
| Forgotten Records Removal Threshold (%) | `30`                | Percentage of tracks that must be scrobbled before removal from Forgotten Records                    |
| Genre Filter 1–5                        | (empty)             | Filter albums by genre (case-insensitive). Leave empty to include all genres.                        |
| Rebuild Schedule (Cron)                 | `0 0 * * *`         | Cron expression for rebuild (requires Navidrome restart to change)                                   |

A threshold of 30% means: for a 10-track album, playing any 3 distinct tracks removes it. Setting it to 1 removes the album after the very first track; 100 requires every track.

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
3. Optionally adjust the Forgotten Records config options (playlist name, album count, album pool size, threshold, cron schedule) in Navidrome → Settings → Plugins.
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

```
cd /tmp
curl -L -o tinygo.tar.gz https://github.com/tinygo-org/tinygo/releases/download/v0.41.1/tinygo0.41.1.darwin-arm64.tar.gz
sudo tar -C /usr/local -xzf tinygo.tar.gz
```

Then build:

```
# First time only — fetch dependencies
go mod tidy

# Build plugin.wasm and package play-it-again.ndp
make
```

The Makefile compiles `main.go` using TinyGo (`-target wasip1 -buildmode=c-shared`) and then zips `manifest.json` and `plugin.wasm` into `play-it-again.ndp`. The resulting `.ndp` is around 440 KB compressed.

To clean build artefacts:

```
make clean
```