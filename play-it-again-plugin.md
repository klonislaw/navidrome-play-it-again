# Play it again — Navidrome Plugin

A scrobbler plugin that removes albums from a watchlist playlist as you listen to them.

---

## How it works

1. You add full albums to a playlist called **Play Later** (or whatever name you configure).
2. When you play tracks from an album on that list, the plugin counts each completed play (a scrobble event).
3. Once the percentage of played tracks crosses the configured threshold, **all tracks from that album are removed from the playlist**.

The threshold is configurable in the Navidrome UI. Set it to `0` to remove after the very first track. Set it to `50` to remove after half the album.

---

## Project structure

```
play-it-again/
├── main.go        ← Plugin logic
├── manifest.json  ← Plugin metadata and permissions
├── go.mod         ← Go module file
└── Makefile       ← Build helper
```

---

## `manifest.json`

```json
{
  "name": "Play it again",
  "author": "you",
  "version": "1.0.0",
  "description": "Removes albums from a watchlist playlist as you listen to them.",
  "permissions": {
    "users": {
      "reason": "Receive scrobble events and make Subsonic API calls on behalf of the assigned user"
    },
    "subsonicapi": {
      "reason": "Read and modify the watchlist playlist"
    },
    "kvstore": {
      "reason": "Track which tracks from each album have been played",
      "maxSize": "1MB"
    }
  }
}
```

---

## `main.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

// ── configuration keys ──────────────────────────────────────────────────────

const (
	cfgPlaylistName     = "playlist_name"
	cfgThresholdPercent = "threshold_percent"
	defaultPlaylist     = "Play Later"
)

// ── Subsonic API response types ─────────────────────────────────────────────

type apiRoot struct {
	Response apiBody `json:"subsonic-response"`
}

type apiBody struct {
	Status    string          `json:"status"`
	Playlists *playlistsWrap  `json:"playlists,omitempty"`
	Playlist  *playlistDetail `json:"playlist,omitempty"`
}

type playlistsWrap struct {
	Playlist []playlistSummary `json:"playlist"`
}

type playlistSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type playlistDetail struct {
	Entry []entry `json:"entry"`
}

type entry struct {
	ID          string `json:"id"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist"`
}

// ── KVStore state ────────────────────────────────────────────────────────────

type albumState struct {
	PlayedIDs []string `json:"played_ids"`
}

// ── plugin ───────────────────────────────────────────────────────────────────

type playAgain	 struct{}

func (p *playAgain) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) {
	return true, nil
}

func (p *playAgain) NowPlaying(_ scrobbler.NowPlayingRequest) error {
	return nil
}

func (p *playAgain) Scrobble(req scrobbler.ScrobbleRequest) error {
	playlist := cfgStr(cfgPlaylistName, defaultPlaylist)
	threshold := cfgInt(cfgThresholdPercent, 0)

	username := req.Username
	track := req.Track

	// Find the watchlist playlist by name.
	playlistID, ok := findPlaylist(username, playlist)
	if !ok {
		logf(pdk.LogDebug, "playlist %q not found, nothing to do", playlist)
		return nil
	}

	// Get all tracks currently in the playlist.
	entries := getEntries(username, playlistID)

	// Find the playlist indices of tracks belonging to this album.
	var indices []int
	for i, e := range entries {
		if e.Album == track.Album && e.AlbumArtist == track.AlbumArtist {
			indices = append(indices, i)
		}
	}

	if len(indices) == 0 {
		logf(pdk.LogDebug, "album %q not in watchlist, skipping", track.Album)
		return nil
	}

	total := len(indices)

	// Update persistent play state for this album.
	key := stateKey(track.Album, track.AlbumArtist)
	state := loadState(key)
	if !contains(state.PlayedIDs, track.ID) {
		state.PlayedIDs = append(state.PlayedIDs, track.ID)
		saveState(key, state)
	}

	played := len(state.PlayedIDs)
	percent := played * 100 / total

	logf(pdk.LogInfo, "album %q — %d/%d tracks played (%d%%)", track.Album, played, total, percent)

	// Threshold of 0 means: remove after the very first track.
	thresholdMet := threshold == 0 || percent >= threshold
	if !thresholdMet {
		return nil
	}

	logf(pdk.LogInfo, "removing album %q from %q", track.Album, playlist)
	removeEntries(username, playlistID, indices)

	if err := host.KVStoreDelete(key); err != nil {
		logf(pdk.LogWarn, "KVStore delete failed: %v", err)
	}

	return nil
}

// ── Subsonic API helpers ─────────────────────────────────────────────────────

func apiCall(uri string) (*apiRoot, error) {
	resp, err := host.SubsonicAPICall(uri)
	if err != nil {
		return nil, err
	}
	var root apiRoot
	if err := json.Unmarshal([]byte(resp), &root); err != nil {
		return nil, err
	}
	return &root, nil
}

func findPlaylist(username, name string) (string, bool) {
	root, err := apiCall("getPlaylists?u=" + username)
	if err != nil || root.Response.Status != "ok" || root.Response.Playlists == nil {
		return "", false
	}
	for _, p := range root.Response.Playlists.Playlist {
		if p.Name == name {
			return p.ID, true
		}
	}
	return "", false
}

func getEntries(username, playlistID string) []entry {
	root, err := apiCall("getPlaylist?id=" + playlistID + "&u=" + username)
	if err != nil || root.Response.Status != "ok" || root.Response.Playlist == nil {
		return nil
	}
	return root.Response.Playlist.Entry
}

func removeEntries(username, playlistID string, indices []int) {
	// Sort descending: removing a higher index first leaves lower indices unaffected.
	sorted := make([]int, len(indices))
	copy(sorted, indices)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))

	parts := []string{
		"playlistId=" + playlistID,
		"u=" + username,
	}
	for _, idx := range sorted {
		parts = append(parts, "songIndexToRemove="+strconv.Itoa(idx))
	}

	if _, err := host.SubsonicAPICall("updatePlaylist?" + strings.Join(parts, "&")); err != nil {
		logf(pdk.LogError, "updatePlaylist failed: %v", err)
	}
}

// ── KVStore helpers ──────────────────────────────────────────────────────────

// stateKey builds a safe KVStore key from album metadata.
func stateKey(album, albumArtist string) string {
	clean := func(s string) string {
		return strings.NewReplacer(":", "_", "/", "_", `\`, "_").Replace(s)
	}
	return "album:" + clean(albumArtist) + ":" + clean(album)
}

func loadState(key string) albumState {
	result, err := host.KVStoreGet(key)
	if err != nil || !result.Exists {
		return albumState{}
	}
	var s albumState
	if err := json.Unmarshal(result.Value, &s); err != nil {
		return albumState{}
	}
	return s
}

func saveState(key string, s albumState) {
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if _, err := host.KVStoreSet(key, data); err != nil {
		logf(pdk.LogWarn, "KVStore set failed: %v", err)
	}
}

// ── Config helpers ───────────────────────────────────────────────────────────

func cfgStr(key, def string) string {
	v, ok := pdk.GetConfig(key)
	if !ok || v == "" {
		return def
	}
	return v
}

func cfgInt(key, def int) int {
	v, ok := pdk.GetConfig(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > 100 {
		return def
	}
	return n
}

// ── Misc ─────────────────────────────────────────────────────────────────────

func logf(level pdk.LogLevel, format string, args ...any) {
	pdk.Log(level, fmt.Sprintf("play-it-again: "+format, args...))
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func init() {
	scrobbler.Register(&playAgain{})
}

func main() {}
```

---

## `go.mod`

```
module play-it-again

go 1.21

require (
    github.com/extism/go-pdk v1.0.0
    github.com/navidrome/navidrome/plugins/pdk/go v0.0.0
)

// Point this to wherever you cloned the Navidrome source repo.
replace github.com/navidrome/navidrome/plugins/pdk/go => ../navidrome/plugins/pdk/go
```

---

## `Makefile`

```makefile
PLUGIN = play-it-again.ndp

.PHONY: build clean

build: $(PLUGIN)

$(PLUGIN): manifest.json plugin.wasm
	zip -j $@ manifest.json plugin.wasm

plugin.wasm: main.go go.mod
	tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .

clean:
	rm -f plugin.wasm $(PLUGIN)
```

---

## Build and install

### 1. Prerequisites

- [TinyGo](https://tinygo.org/getting-started/install/) installed and on your `PATH`
- Go 1.21+
- The Navidrome source cloned alongside your plugin directory:

```
your-projects/
├── navidrome/          ← git clone https://github.com/navidrome/navidrome
└── play-it-again/         ← this plugin
```

### 2. Fetch dependencies and build

```bash
cd play-it-again
go mod tidy
make build
# Produces: play-it-again.ndp
```

### 3. Install

Copy `play-it-again.ndp` to your Navidrome plugins folder (default: `<navidrome-data-dir>/plugins/`).

Enable plugins in `navidrome.toml`:

```toml
[Plugins]
Enabled = true
```

### 4. Configure in Navidrome

1. Open Navidrome → avatar menu → **Plugins**
2. Find **Play it again** and enable it
3. Under **Users**, assign the plugin to your user account (required for scrobbler plugins)
4. Click the plugin to open its settings and set:

| Key                 | Default      | Meaning                                                                                          |
| ------------------- | ------------ | ------------------------------------------------------------------------------------------------ |
| `playlist_name`     | `Play Later` | Name of the watchlist playlist                                                                   |
| `threshold_percent` | `0`          | % of tracks that must be played before the album is removed. `0` = remove after the first track. |

### 5. Create the playlist

In Navidrome (or any Subsonic client), create a playlist with the exact name you configured — `Play Later` by default. Add full albums to it. The plugin watches for that exact name.

---

## Notes

- **Scrobble timing**: Navidrome triggers a scrobble after roughly 50% of a track's duration has played (following the Last.fm convention). A track you skip early will not count.
- **Duplicate protection**: The plugin stores track IDs in a KVStore database. Playing the same track twice only counts once.
- **Client compatibility**: Because this runs server-side and works through the standard playlist API, it is transparent to all Subsonic clients including Symfonium, Narjo, and any others you use.
- **Threshold of 50** is a good middle ground: the album disappears after you've genuinely worked through half of it.

---

## Potential compile adjustments

The `host.SubsonicAPICall` return type and `host.KVStoreDelete` signature may differ slightly between PDK versions. If TinyGo gives a type error on those lines:

- If `resp` is `[]byte` rather than `string`, change `[]byte(resp)` to just `resp` in `apiCall`.
- If `KVStoreDelete` returns only `error` (not a tuple), change `if err := host.KVStoreDelete(key)` accordingly.
