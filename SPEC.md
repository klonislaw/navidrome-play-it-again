---
title: SPEC
type: note
permalink: play-it-again/spec
---

# Navidrome Play it again — Plugin Specification

## Overview

A Navidrome plugin that maintains two playlists per user:

- **Play Later** — When the user plays enough tracks from an album in this playlist, the plugin automatically removes all of that album's tracks.
- **Forgotten Records** — Automatically populated with random complete albums that haven't been played in the longest time. Supports up to 5 independent playlists, each filterable by genre tags. Rebuilt on a configurable schedule, with the same threshold-based removal as Play Later.

---

## User-facing behaviour

### Adding albums

The user adds albums to their Play Later playlist using any Subsonic-compatible client (Psysonic, Narjo, etc.) as they normally would — by adding all tracks of an album to a playlist named **"Play Later"** (exact name is configurable). The plugin plays no role in the add step; it is purely reactive.

### Automatic removal

When the user plays tracks, the plugin watches for completed plays (scrobbles). After each scrobble it checks:

1. Does the played track belong to an album currently in the user's Play Later playlist?
2. If yes: has the percentage of distinct tracks played from that album (across the _current listening session and all prior sessions_) reached the configured **threshold**?
3. If yes: remove **all tracks of that album** from the playlist.

The threshold is a configurable integer from 1–100 (percentage of the album's total tracks). Default: **30%**.

Play counts accumulate **across sessions** — a track played today counts alongside one played last week. This is simpler to implement than session-scoped tracking (which would require defining and expiring sessions) and is more useful in practice.

### Example

- Album: _Kind of Blue_ — 5 tracks.
- Threshold: 50% → 3 tracks must be scrobbled (⌈5 × 0.5⌉ = 3).
- After the 3rd distinct track from the album is scrobbled, all 5 tracks are removed from Play Later.
- Replaying the same track does **not** increase the count; only distinct tracks count.

### Forgotten Records

The plugin maintains up to 5 independent Forgotten Records playlists — each collects random complete albums the user hasn't played in a long time, optionally filtered by genre tags. All playlists are rebuilt automatically on a shared schedule (default: daily at midnight).

On each rebuild, for each configured playlist:

1. All albums in the library are fetched and sorted by last-played timestamp (never-played first, then least-recently-played).
2. If tags are configured for this playlist, only albums matching at least one tag (case-insensitive) are included.
3. A candidate pool of `forgottenrecords_album_pool_size` albums is taken from the top of the sorted list.
4. `forgottenrecords_album_count` albums are randomly selected from the pool.
5. The playlist is fully replaced with all tracks from the selected albums.

Each playlist can be given a custom name; if left empty, it is auto-numbered as "Forgotten records N".

As the user listens to albums in any of the playlists, the same threshold-based removal logic from Play Later applies: once enough distinct tracks from an album have been played, the album is removed. Fresh albums take its place on the next scheduled rebuild.

Playlists are created automatically if they don't exist — no manual setup needed.

---

## Configuration

Declared in the plugin manifest and editable in Navidrome's plugin UI:

### Play Later

| Key                 | Type    | Default      | Description                                                                                                       |
| ------------------- | ------- | ------------ | ----------------------------------------------------------------------------------------------------------------- |
| `playlater_enabled` | boolean | `true`       | Master switch for the Play Later feature. When `false`, scrobbles are ignored and the playlist is left untouched. |
| `playlist_name`     | string  | `Play Later` | Exact name of the playlist to watch                                                                               |
| `threshold`         | integer | `30`         | % of distinct tracks that must be scrobbled to trigger removal (1–100)                                            |

### Forgotten Records

| Key                                | Type    | Default             | Description                                                                                                                                                                           |
| ---------------------------------- | ------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `forgottenrecords_enabled`         | boolean | `true`              | Master switch for the Forgotten Records feature. When `false`, no scheduled or scrobble-driven rebuilds run and any existing recurring schedule is cancelled on init.                 |
| `forgottenrecords_count`           | integer | `1`                 | Number of Forgotten Records playlists to maintain (1–5).                                                                                                                              |
| `forgottenrecords_playlist_name_N` | string  | (auto-numbered)     | Name of the Nth Forgotten Records playlist (auto-created if missing). Leave empty for "Forgotten records N".                                                                          |
| `forgottenrecords_tags_N`          | string  | (empty)             | Comma-separated genre tags to filter albums for playlist N (case-insensitive). Leave empty to include all genres.                                                                     |
| `forgottenrecords_album_count`     | integer | `8`                 | Number of complete albums to include in each playlist rebuild                                                                                                                          |
| `forgottenrecords_album_pool_size` | integer | `50`                | Directly sets the pool size for random selection. If smaller than `forgottenrecords_album_count`, the album count is used. If larger than the library size, the library size is used. |
| `forgottenrecords_threshold`       | integer | `30`                | % of distinct tracks that must be scrobbled before removal from any Forgotten Records playlist                                                                                         |
| `forgottenrecords_schedule`        | string  | `0 0 * * *`         | Cron expression for rebuild schedule (requires Navidrome restart to change)                                                                                                           |

---

## Technical architecture

### Plugin system

- **Runtime**: Navidrome's native WebAssembly plugin system (Extism).
- **Capabilities**: `Scrobbler` (per-play events), `Scheduler` (periodic rebuild), `Lifecycle` (init-time schedule registration).
- **Language**: Go, compiled to WebAssembly via TinyGo.
- **Package**: `.ndp` file (ZIP containing `manifest.json` + `plugin.wasm`).

### Permissions required

```json
{
  "permissions": {
    "subsonicAPI": {
      "reason": "Read and modify the Play Later and Forgotten Records playlists"
    },
    "users": {
      "reason": "Receive scrobble events per user and iterate users for scheduled rebuilds"
    },
    "kvstore": { "reason": "Track per-user play counts between sessions" },
    "cache": { "reason": "Cache playlist IDs to avoid redundant API calls" },
    "scheduler": {
      "reason": "Schedule periodic rebuild of the Forgotten Records playlist"
    }
  }
}
```

### Scrobbler interface

Navidrome calls these exported functions:

| Function                       | When called        | What the plugin does                                         |
| ------------------------------ | ------------------ | ------------------------------------------------------------ |
| `nd_scrobbler_is_authorized`   | Plugin UI setup    | Always returns `true`                                        |
| `nd_scrobbler_now_playing`     | Track starts       | No-op (ignored)                                              |
| `nd_scrobbler_playback_report` | Playback report    | No-op (ignored)                                              |
| `nd_scrobbler_scrobble`        | Track reaches ~90% | Runs removal check for both Play Later and Forgotten Records |

### Scrobble handler — step by step

```
on Scrobble(username, track):
  1. albumId, totalTracks ← fetchAlbumInfo(username, track.ID)
     if not found → return
  2. checkPlaylistRemoval(username, track, albumId, totalTracks,
       playlist="Play Later", threshold=config.playlater_threshold, kvPrefix="")
  3. for each FR playlist config:
       checkPlaylistRemoval(username, track, albumId, totalTracks,
         playlist=cfg.Name, threshold=config.forgottenrecords_threshold, kvPrefix="fr{i}:")

checkPlaylistRemoval(username, track, albumId, totalTracks, playlist, threshold, kvPrefix):
  1. playlist ← findPlaylist(username, playlistName)
     if not found → return
  2. indexes ← tracks in playlist where albumId matches
     if empty → return
  3. playedKey ← kvPrefix + "played:{username}:{albumId}"
  4. playedSet ← KVStore.Get(playedKey)
  5. playedSet.add(track.ID); KVStore.Set(playedKey, playedSet)
  6. required ← ceil(totalTracks × threshold / 100)
  7. if len(playedSet) >= required:
       updatePlaylist — remove album tracks
       KVStore.Delete(playedKey)
```

### Scheduler interface

| Function                | When called         | What the plugin does                                                |
| ----------------------- | ------------------- | ------------------------------------------------------------------- |
| `nd_scheduler_callback` | Cron schedule fires | If payload is `"rebuild"`, rebuilds Forgotten Records for all users |

### Lifecycle interface

| Function     | When called                       | What the plugin does                                                                                                  |
| ------------ | --------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `nd_on_init` | Plugin loaded (not on hot-reload) | Cancels any existing `fr-rebuild` schedule and registers a new recurring schedule with the configured cron expression |

### Finding a playlist by name

Navidrome does not expose a "find playlist by name" endpoint, so the plugin:

1. Calls `getPlaylists?u={username}` to list all playlists owned by the user.
2. Finds the one whose `name` matches the configured playlist name (case-insensitive).
3. Caches the playlist ID in the in-memory cache with a short TTL (5 minutes).

The cache key includes the playlist name (`playlist:{username}:{playlistName}`) so that multiple playlists (Play Later, Forgotten Records) each get their own cache entry.

### KVStore schema

| Key pattern                          | Value                                                 | TTL                       |
| ------------------------------------ | ----------------------------------------------------- | ------------------------- |
| `playlist:{username}:{playlistName}` | Navidrome playlist ID (string, in-memory cache)       | 5 min                     |
| `played:{username}:{albumId}`        | JSON array of scrobbled track IDs (Play Later)        | None (cleared on removal) |
| `fr{N}:played:{username}:{albumId}`  | JSON array of scrobbled track IDs (FR playlist N)     | None (cleared on removal) |
| `state:last_username`                | Username saved by IsAuthorized                        | None                      |

### Playlist modification

Navidrome's Subsonic API `updatePlaylist` endpoint supports adding and removing individual song IDs:

```
updatePlaylist?playlistId=…&songIndexToRemove=0&songIndexToRemove=1…
```

Because the endpoint uses **indexes** (not IDs) for removal, the plugin must:

1. Fetch the full playlist via `getPlaylist?id=…` to get the ordered song list.
2. Collect the 0-based indexes of all tracks belonging to the target album.
3. Call `updatePlaylist` with all those indexes.

---

## Setup instructions (for the user)

### Play Later

1. Drop `play-it-again.ndp` into Navidrome's plugins folder (e.g. `/data/plugins/`).
2. In Navidrome → Settings → Plugins, enable the plugin.
3. Assign your user account to the plugin (required for scrobble events to fire).
4. Optionally adjust `playlater_enabled`, `playlist_name` and `threshold` in the plugin config.
5. In any client, create a playlist whose name matches the configured `playlist_name`.
6. Add albums to it as normal. Done.

### Forgotten Records

1. After enabling the plugin (steps 1–3 above), the Forgotten Records playlist is created automatically on the first scheduled rebuild.
2. To trigger it immediately, restart Navidrome (the schedule is registered on plugin load).
3. Optionally adjust `forgottenrecords_enabled`, `forgottenrecords_count`, per-playlist names and tags, `forgottenrecords_album_count`, `forgottenrecords_album_pool_size`, `forgottenrecords_threshold`, and `forgottenrecords_schedule` in the plugin config.
4. Changing `forgottenrecords_schedule` requires a Navidrome restart (not hot-reload) to take effect.

---

## Edge cases and constraints

| Scenario                                              | Behaviour                                                                                                                                                           |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Play Later playlist doesn't exist yet                 | Plugin does nothing; no error                                                                                                                                       |
| Forgotten Records playlist doesn't exist              | Created automatically on first scheduled rebuild                                                                                                                    |
| `forgottenrecords_count` > 1                          | Each playlist is rebuilt independently with its own tag filter                                                                                                       |
| Album partially added (not all tracks in playlist)    | `totalInAlbum` comes from Navidrome's album record (full count), so threshold is based on the full album even if only some tracks are in the playlist               |
| User replays a track                                  | Distinct-track set prevents double-counting                                                                                                                         |
| Track removed from playlist manually before threshold | On next scrobble, the plugin re-checks; if no album tracks remain, it skips                                                                                         |
| Plugin assigned to multiple users                     | Each user's state is fully independent (key-namespaced by username)                                                                                                 |
| `threshold` = 1                                       | First scrobble from the album triggers removal (effectively "any single song")                                                                                      |
| `playlater_threshold` = 100                           | All tracks must be scrobbled                                                                                                                                        |
| Album not in Navidrome library (edge)                 | `getAlbum` fails → plugin logs a warning and skips                                                                                                                  |
| Library has fewer albums than pool size               | Pool shrinks to available albums; if fewer than `forgottenrecords_album_count`, all available albums are used                                                       |
| Playlist tags empty                                   | No filtering — all albums are candidates (current default behavior)                                                                                                  |
| Schedule registration fails                           | Plugin loads normally; Play Later still works; error logged                                                                                                         |
| Config change to `forgottenrecords_schedule`          | Requires Navidrome restart (not hot-reload) — `OnInit` re-registers the schedule                                                                                    |
| `playlater_enabled` = false                           | Plugin skips all Play Later processing on scrobble; existing playlist is left as-is                                                                                 |
| `forgottenrecords_enabled` = false                    | `OnInit` cancels the recurring rebuild schedule; `OnCallback` ignores rebuild callbacks; scrobbles skip Forgotten Records removal                                   |
| Plugin enabled / re-enabled / Navidrome restarted     | `OnInit` re-registers the recurring schedule and schedules an immediate `rebuild-now` so the Forgotten Records playlist refreshes without waiting for the cron tick |

---

## Forgotten Records rebuild logic

### Algorithm

```
rebuildForgottenRecords(username):
  1. Fetch all albums via getAlbumList2 (paginated, 500 per page)
  2. Sort by played timestamp ascending (never-played first)
  3. For each configured FR playlist (i = 1..count):
     a. If tags configured: filter albums to only those matching any tag
     b. poolSize ← fr_album_count × fr_pool_multiplier (clamped to library size)
     c. pool ← first poolSize albums from sorted list
     d. selected ← randomSelect(pool, fr_album_count)
     e. For each album in selected:
        - Delete fr{i}:played:{username}:{albumId} (clean slate)
        - Fetch track IDs via getAlbum
        - Collect all track IDs
     f. Find or create the playlist
     g. Clear the playlist (remove all entries in batches of 200)
     h. Add all track IDs to the playlist (in batches of 200)
```

### Random selection

A pool of `forgottenrecords_album_pool_size` least-recently-played albums is built. If tags are configured for a playlist, only albums matching at least one tag are included before the pool is formed. From this pool, `forgottenrecords_album_count` albums are randomly selected (without replacement) using `math/rand` seeded with the current time. This gives variety while ensuring only long-unplayed albums are candidates. If fewer albums match the tag filter than the pool size, the pool shrinks to the available matches. Each playlist uses independent played-track tracking (`fr{N}:played:` prefix).

### Schedule lifecycle

- On `OnInit` (plugin load, not hot-reload): cancels any existing `fr-rebuild` schedule, then registers a new recurring schedule with the configured `forgottenrecords_schedule` cron expression.
- On callback (`payload="rebuild"`): iterates all Navidrome users via `host.UsersGetUsers()` and rebuilds each user's Forgotten Records playlist.
- Changing `forgottenrecords_schedule` requires a Navidrome restart (not hot-reload) to take effect.

---

## Out of scope

- Adding albums _to_ the Play Later playlist via the plugin (users do this manually in their client).
- Notifications when an album is removed.
- Cross-user shared playlists.
- Play-count weighting by track duration.

---

## Build and packaging

```bash
# Prerequisites: TinyGo, Go
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j play-it-again.ndp manifest.json plugin.wasm
```

Minimum Navidrome version: **0.54** (plugin system introduction). Tested target: **0.61.2**.