package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const (
	// Play Later settings
	defaultPlayLaterEnabled      = true
	defaultPlayLaterPlaylistName = "Play Later"
	defaultPlayLaterThreshold    = 30
	playlistCacheTTL             = 300  // seconds — playlist ID cache lifetime
	trackInfoCacheTTL            = 3600 // seconds — track→album and album→count are immutable

	// Forgotten Records settings
	defaultForgottenRecordsEnabled       = true
	defaultForgottenRecordsPlaylistName  = "Forgotten records"
	defaultForgottenRecordsAlbumCount    = 8
	defaultForgottenRecordsAlbumPoolSize = 50
	defaultForgottenRecordsThreshold     = 30
	defaultForgottenRecordsSchedule      = "0 0 * * *"
	frScheduleID                         = "fr-rebuild"
	frRefreshOnceID                      = "fr-refresh-once"
	manualRefreshRateLimit               = 300 // seconds — rate limit for manual refresh trigger
	albumListPageSize                    = 500
	playlistBatchSize                    = 200
)

// --- Plugin ---

type plugin struct{}

func (p *plugin) IsAuthorized(req scrobbler.IsAuthorizedRequest) (bool, error) {
	// Navidrome does not pass the username in ScrobbleRequest, so we bridge it
	// here: IsAuthorized is always called immediately before Scrobble for the
	// same user, so saving it now lets Scrobble retrieve it from KVStore.
	if req.Username != "" {
		host.KVStoreSet("state:last_username", []byte(req.Username))
	}
	return true, nil
}

func (p *plugin) NowPlaying(_ scrobbler.NowPlayingRequest) error {
	return nil
}

func (p *plugin) PlaybackReport(_ scrobbler.PlaybackReportRequest) error {
	return nil
}

func (p *plugin) Scrobble(req scrobbler.ScrobbleRequest) error {
	username := req.Username
	trackID := req.Track.ID

	// Navidrome doesn't populate req.Username for Scrobble; retrieve it from
	// the value saved by the preceding IsAuthorized call.
	if username == "" {
		if data, exists, _ := host.KVStoreGet("state:last_username"); exists {
			username = string(data)
		}
	}

	logf("scrobble received: user=%s track=%q album=%q", username, req.Track.Title, req.Track.Album)

	// Resolve albumId and total track count for this track (shared by both features).
	albumID, totalTracks, err := fetchAlbumInfo(username, trackID)
	if err != nil {
		logf("error resolving album for track %q: %v", req.Track.Title, err)
		return nil
	}
	if albumID == "" || totalTracks == 0 {
		return nil
	}

	// Play Later: check and possibly remove from the Play Later playlist.
	if configBool("playlater_enabled", defaultPlayLaterEnabled) {
		checkPlaylistRemoval(username, trackID, albumID, req.Track.Album, totalTracks,
			configStr("playlater_playlist_name", defaultPlayLaterPlaylistName),
			clamp(configInt("playlater_threshold", defaultPlayLaterThreshold), 1, 100), "")
	}

	// Forgotten Records: same removal logic, separate playlist and KV namespace.
	if configBool("forgottenrecords_enabled", defaultForgottenRecordsEnabled) {
		checkPlaylistRemoval(username, trackID, albumID, req.Track.Album, totalTracks,
			configStr("forgottenrecords_playlist_name", defaultForgottenRecordsPlaylistName),
			clamp(configInt("forgottenrecords_threshold", defaultForgottenRecordsThreshold), 1, 100), "fr:")
	}

	// Manual Forgotten Records refresh: if enabled, schedule a one-time rebuild
	// on the next scrobble. Rate-limited via KVStore timestamp.
	if configBool("forgottenrecords_enabled", defaultForgottenRecordsEnabled) {
		if v, ok := host.ConfigGet("forgottenrecords_refresh_now"); ok && v == "true" {
			if shouldTriggerManualRefresh() {
				if _, err := host.SchedulerScheduleOneTime(0, "rebuild-now", frRefreshOnceID); err != nil {
					logf("fr: failed to schedule manual refresh: %v", err)
				} else {
					logf("fr: manual refresh scheduled")
				}
			}
		}
	}

	return nil
}

// OnInit is called once when the plugin is loaded (not on hot-reload).
// It registers the recurring schedule for the Forgotten Records rebuild.
func (p *plugin) OnInit() error {
	// If Forgotten Records is disabled, cancel any lingering schedule and exit.
	if !configBool("forgottenrecords_enabled", defaultForgottenRecordsEnabled) {
		if err := host.SchedulerCancelSchedule(frScheduleID); err != nil {
			logf("fr: disabled — cancel existing schedule (non-fatal): %v", err)
		} else {
			logf("fr: disabled — recurring rebuild schedule cancelled")
		}
		return nil
	}

	cron := configStr("forgottenrecords_schedule", defaultForgottenRecordsSchedule)

	// Cancel any existing schedule with the same ID (ignore errors if not found).
	if err := host.SchedulerCancelSchedule(frScheduleID); err != nil {
		logf("fr: cancel existing schedule (non-fatal): %v", err)
	}

	if _, err := host.SchedulerScheduleRecurring(cron, "rebuild", frScheduleID); err != nil {
		logf("fr: failed to schedule recurring rebuild: %v", err)
		return nil // don't fail the plugin load — Play Later still works
	}
	logf("fr: scheduled recurring rebuild with cron %q", cron)
	return nil
}

// OnCallback handles scheduled callbacks. The "rebuild" payload triggers a
// Forgotten Records rebuild for every Navidrome user.
func (p *plugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	if req.Payload != "rebuild" && req.Payload != "rebuild-now" {
		return nil
	}
	logf("fr: rebuild callback received")

	// Defensive: honour the enable toggle even if a stale schedule fires.
	if !configBool("forgottenrecords_enabled", defaultForgottenRecordsEnabled) {
		logf("fr: enabled=false — skipping rebuild callback")
		return nil
	}

	users, err := host.UsersGetUsers()
	if err != nil {
		logf("fr: error getting users: %v", err)
		return nil
	}
	for _, u := range users {
		if err := rebuildForgottenRecords(u.UserName); err != nil {
			logf("fr: error rebuilding for user %s: %v", u.UserName, err)
		}
	}
	return nil
}

// --- Playlist removal logic (shared by Play Later and Forgotten Records) ---

// checkPlaylistRemoval handles the scrobble-driven removal workflow for a single
// playlist. kvPrefix namespaces the KVStore played-track keys ("" for Play Later,
// "fr:" for Forgotten Records) so both features track play counts independently.
func checkPlaylistRemoval(username, trackID, albumID, albumName string, totalTracks int,
	playlistName string, threshold int, kvPrefix string) {
	playlistID, err := findPlaylist(username, playlistName)
	if err != nil {
		logf("error looking up playlist %q for user %s: %v", playlistName, username, err)
		return
	}
	if playlistID == "" {
		return
	}

	indexes, err := albumIndexesInPlaylist(username, playlistID, albumID)
	if err != nil {
		logf("error reading playlist %q: %v", playlistName, err)
		return
	}
	if len(indexes) == 0 {
		return
	}

	playedKey := kvPrefix + "played:" + username + ":" + albumID
	playedSet := loadPlayedSet(playedKey)
	playedSet[trackID] = true
	savePlayedSet(playedKey, playedSet)

	required := clamp(int(math.Ceil(float64(totalTracks)*float64(threshold)/100.0)), 1, math.MaxInt32)
	logf("user=%s album=%q: %d/%d tracks played, need %d to remove (%s)",
		username, albumName, len(playedSet), totalTracks, required, playlistName)

	if len(playedSet) < required {
		return
	}

	logf("user=%s album=%q: threshold reached, removing %d tracks from %q",
		username, albumName, len(indexes), playlistName)
	if err := removeFromPlaylist(username, playlistID, indexes); err != nil {
		logf("error removing album %q from playlist %q: %v", albumName, playlistName, err)
		return
	}
	logf("user=%s album=%q: successfully removed from %q", username, albumName, playlistName)

	host.KVStoreDelete(playedKey)
	host.CacheRemove("playlist:" + username + ":" + playlistName)
}

// --- Forgotten Records rebuild ---

// rebuildForgottenRecords fetches all albums, selects the least-recently-played
// ones, randomly picks albumCount from a pool of albumCount×multiplier, and
// fully rebuilds the playlist with their tracks.
func rebuildForgottenRecords(username string) error {
	playlistName := configStr("forgottenrecords_playlist_name", defaultForgottenRecordsPlaylistName)
	albumCount := clamp(configInt("forgottenrecords_album_count", defaultForgottenRecordsAlbumCount), 1, 1000)
	albumPoolSize := clamp(configInt("forgottenrecords_album_pool_size", defaultForgottenRecordsAlbumPoolSize), 1, 1000)

	albums, err := fetchAllAlbums(username)
	if err != nil {
		return fmt.Errorf("fetching albums: %w", err)
	}
	if len(albums) == 0 {
		return nil
	}

	// Sort by played timestamp ascending. Empty string (never played) sorts
	// first since "" < any non-empty string. ISO 8601 timestamps sort
	// lexicographically in chronological order.
	sort.Slice(albums, func(i, j int) bool {
		return albums[i].Played < albums[j].Played
	})

	// Use the album pool size directly, but ensure it's at least albumCount
	// and not larger than the total library album size.
	poolSize := albumPoolSize
	if poolSize < albumCount {
		poolSize = albumCount
	}
	if poolSize > len(albums) {
		poolSize = len(albums)
	}
	pool := albums[:poolSize]

	selected := randomSelect(pool, albumCount)

	var allTrackIDs []string
	for _, a := range selected {
		// Clear any stale played-track state so the album starts fresh.
		host.KVStoreDelete("fr:played:" + username + ":" + a.ID)
		ids, err := fetchAlbumTrackIDs(username, a.ID)
		if err != nil {
			logf("fr: error fetching tracks for album %q: %v", a.Name, err)
			continue
		}
		allTrackIDs = append(allTrackIDs, ids...)
	}

	if len(allTrackIDs) == 0 {
		return fmt.Errorf("no tracks collected for rebuild")
	}

	if err := rebuildPlaylist(username, playlistName, allTrackIDs); err != nil {
		return fmt.Errorf("rebuilding playlist: %w", err)
	}

	logf("fr: rebuilt playlist %q for user %s with %d albums (%d tracks)",
		playlistName, username, len(selected), len(allTrackIDs))
	return nil
}

// randomSelect returns n items selected at random from pool (without replacement).
func randomSelect(pool []albumInfo, n int) []albumInfo {
	if n >= len(pool) {
		return pool
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	perm := r.Perm(len(pool))
	result := make([]albumInfo, n)
	for i := 0; i < n; i++ {
		result[i] = pool[perm[i]]
	}
	return result
}

// --- Subsonic API helpers ---

// apiStatus is a minimal struct for checking the Subsonic response envelope.
type apiStatus struct {
	SubsonicResponse struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"subsonic-response"`
}

// apiGet calls the Subsonic API, checks the status field, then unmarshals into v.
func apiGet(uri string, v interface{}) error {
	resp, err := host.SubsonicAPICall(uri)
	if err != nil {
		return err
	}
	var status apiStatus
	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return fmt.Errorf("subsonic: malformed response: %w", err)
	}
	if status.SubsonicResponse.Status != "ok" {
		if e := status.SubsonicResponse.Error; e != nil {
			return fmt.Errorf("subsonic error %d: %s", e.Code, e.Message)
		}
		return fmt.Errorf("subsonic: status %q", status.SubsonicResponse.Status)
	}
	return json.Unmarshal([]byte(resp), v)
}

// --- Subsonic API response types ---

type songResp struct {
	SubsonicResponse struct {
		Song *songObj `json:"song"`
	} `json:"subsonic-response"`
}

type songObj struct {
	AlbumID string `json:"albumId"`
}

type albumResp struct {
	SubsonicResponse struct {
		Album *albumObj `json:"album"`
	} `json:"subsonic-response"`
}

type albumObj struct {
	SongCount int `json:"songCount"`
}

type playlistsResp struct {
	SubsonicResponse struct {
		Playlists *playlistsObj `json:"playlists"`
	} `json:"subsonic-response"`
}

type playlistsObj struct {
	Playlist []playlistEntry `json:"playlist"`
}

type playlistEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type playlistResp struct {
	SubsonicResponse struct {
		Playlist *playlistObj `json:"playlist"`
	} `json:"subsonic-response"`
}

type playlistObj struct {
	Entry []songEntry `json:"entry"`
}

type songEntry struct {
	ID      string `json:"id"`
	AlbumID string `json:"albumId"`
}

// Forgotten Records response types

type albumList2Resp struct {
	SubsonicResponse struct {
		AlbumList2 *struct {
			Album []albumInfo `json:"album"`
		} `json:"albumList2"`
	} `json:"subsonic-response"`
}

type albumInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Played string `json:"played"`
}

type albumSongsResp struct {
	SubsonicResponse struct {
		Album *struct {
			Song []struct {
				ID string `json:"id"`
			} `json:"song"`
		} `json:"album"`
	} `json:"subsonic-response"`
}

// fetchAlbumInfo returns the Navidrome albumId and the album's total track count.
// Results are cached for trackInfoCacheTTL seconds because the track→album
// relationship and album track counts are immutable between library rescans.
func fetchAlbumInfo(username, trackID string) (albumID string, totalTracks int, err error) {
	// Try cache for albumId.
	albumCacheKey := "song:" + trackID + ":albumId"
	if cached, exists, _ := host.CacheGetString(albumCacheKey); exists {
		albumID = cached
	} else {
		var sr songResp
		if err = apiGet("getSong?id="+url.QueryEscape(trackID)+"&u="+url.QueryEscape(username), &sr); err != nil {
			return
		}
		if sr.SubsonicResponse.Song == nil || sr.SubsonicResponse.Song.AlbumID == "" {
			return
		}
		albumID = sr.SubsonicResponse.Song.AlbumID
		host.CacheSetString(albumCacheKey, albumID, trackInfoCacheTTL)
	}

	// Try cache for song count.
	countCacheKey := "album:" + albumID + ":songCount"
	if cached, exists, _ := host.CacheGetString(countCacheKey); exists {
		totalTracks, _ = strconv.Atoi(cached)
		return
	}

	var ar albumResp
	if err = apiGet("getAlbum?id="+url.QueryEscape(albumID)+"&u="+url.QueryEscape(username), &ar); err != nil {
		albumID = ""
		return
	}
	if ar.SubsonicResponse.Album == nil {
		albumID = ""
		return
	}
	totalTracks = ar.SubsonicResponse.Album.SongCount
	// Only cache a valid (non-zero) count so a broken album response doesn't
	// suppress retries for the full TTL duration.
	if totalTracks > 0 {
		host.CacheSetString(countCacheKey, strconv.Itoa(totalTracks), trackInfoCacheTTL)
	}
	return
}

// findPlaylist returns the playlist ID whose name matches playlistName, using a
// short-lived in-memory cache to avoid repeated getPlaylists calls.
// The cache key includes the playlist name so multiple playlists (e.g. Play Later
// and Forgotten records) each get their own cache entry.
func findPlaylist(username, playlistName string) (string, error) {
	cacheKey := "playlist:" + username + ":" + playlistName
	if id, exists, _ := host.CacheGetString(cacheKey); exists {
		return id, nil
	}

	var pr playlistsResp
	if err := apiGet("getPlaylists?u="+url.QueryEscape(username), &pr); err != nil {
		return "", err
	}
	if pr.SubsonicResponse.Playlists == nil {
		return "", nil
	}

	for _, pl := range pr.SubsonicResponse.Playlists.Playlist {
		if strings.EqualFold(pl.Name, playlistName) {
			host.CacheSetString(cacheKey, pl.ID, playlistCacheTTL)
			return pl.ID, nil
		}
	}
	return "", nil
}

// albumIndexesInPlaylist returns the 0-based positions of all tracks from
// albumID in the given playlist.
func albumIndexesInPlaylist(username, playlistID, albumID string) ([]int, error) {
	var pr playlistResp
	uri := "getPlaylist?id=" + url.QueryEscape(playlistID) + "&u=" + url.QueryEscape(username)
	if err := apiGet(uri, &pr); err != nil {
		return nil, err
	}
	if pr.SubsonicResponse.Playlist == nil {
		return nil, nil
	}

	var indexes []int
	for i, entry := range pr.SubsonicResponse.Playlist.Entry {
		if entry.AlbumID == albumID {
			indexes = append(indexes, i)
		}
	}
	return indexes, nil
}

// removeFromPlaylist removes entries at the given indexes from the playlist.
// Indexes are sorted descending so sequential removals (if the API processes
// them one by one) do not shift earlier positions.
func removeFromPlaylist(username, playlistID string, indexes []int) error {
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))

	parts := make([]string, len(indexes))
	for i, idx := range indexes {
		parts[i] = fmt.Sprintf("songIndexToRemove=%d", idx)
	}

	uri := "updatePlaylist?playlistId=" + url.QueryEscape(playlistID) +
		"&u=" + url.QueryEscape(username) +
		"&" + strings.Join(parts, "&")

	var base struct{}
	return apiGet(uri, &base)
}

// --- Forgotten Records API helpers ---

// fetchAllAlbums paginates through getAlbumList2 to retrieve every album in the
// user's library. The Subsonic API has no "least recently played" sort, so we
// fetch all albums and sort client-side.
func fetchAllAlbums(username string) ([]albumInfo, error) {
	var all []albumInfo
	offset := 0
	for {
		uri := fmt.Sprintf("getAlbumList2?type=alphabeticalByName&size=%d&offset=%d&u=%s",
			albumListPageSize, offset, url.QueryEscape(username))
		var resp albumList2Resp
		if err := apiGet(uri, &resp); err != nil {
			return nil, err
		}
		if resp.SubsonicResponse.AlbumList2 == nil {
			break
		}
		albums := resp.SubsonicResponse.AlbumList2.Album
		if len(albums) == 0 {
			break
		}
		all = append(all, albums...)
		if len(albums) < albumListPageSize {
			break
		}
		offset += albumListPageSize
	}
	return all, nil
}

// fetchAlbumTrackIDs returns the track IDs for a given album.
func fetchAlbumTrackIDs(username, albumID string) ([]string, error) {
	var resp albumSongsResp
	uri := "getAlbum?id=" + url.QueryEscape(albumID) + "&u=" + url.QueryEscape(username)
	if err := apiGet(uri, &resp); err != nil {
		return nil, err
	}
	if resp.SubsonicResponse.Album == nil {
		return nil, nil
	}
	var ids []string
	for _, s := range resp.SubsonicResponse.Album.Song {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// rebuildPlaylist finds or creates the playlist, clears it, then adds all trackIDs.
func rebuildPlaylist(username, playlistName string, trackIDs []string) error {
	playlistID, err := findOrCreatePlaylist(username, playlistName)
	if err != nil {
		return err
	}
	if err := clearPlaylist(username, playlistID); err != nil {
		return err
	}
	return addTracksToPlaylist(username, playlistID, trackIDs)
}

// findOrCreatePlaylist looks up the playlist by name; if not found, creates it.
func findOrCreatePlaylist(username, playlistName string) (string, error) {
	id, err := findPlaylist(username, playlistName)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	// Create the playlist, then clear cache and look it up again.
	host.CacheRemove("playlist:" + username + ":" + playlistName)
	uri := "createPlaylist?name=" + url.QueryEscape(playlistName) + "&u=" + url.QueryEscape(username)
	var base struct{}
	if err := apiGet(uri, &base); err != nil {
		return "", err
	}
	return findPlaylist(username, playlistName)
}

// clearPlaylist removes all entries from a playlist by repeatedly removing
// the last batch of entries. Removing from the end avoids index-shifting issues.
func clearPlaylist(username, playlistID string) error {
	count, err := playlistEntryCount(username, playlistID)
	if err != nil {
		return err
	}
	for count > 0 {
		batch := count
		if batch > playlistBatchSize {
			batch = playlistBatchSize
		}
		parts := make([]string, batch)
		for i := 0; i < batch; i++ {
			parts[i] = fmt.Sprintf("songIndexToRemove=%d", count-1-i)
		}
		uri := "updatePlaylist?playlistId=" + url.QueryEscape(playlistID) +
			"&u=" + url.QueryEscape(username) +
			"&" + strings.Join(parts, "&")
		var base struct{}
		if err := apiGet(uri, &base); err != nil {
			return err
		}
		count -= batch
	}
	return nil
}

// addTracksToPlaylist adds track IDs to the playlist in batches.
func addTracksToPlaylist(username, playlistID string, trackIDs []string) error {
	for i := 0; i < len(trackIDs); i += playlistBatchSize {
		end := i + playlistBatchSize
		if end > len(trackIDs) {
			end = len(trackIDs)
		}
		batch := trackIDs[i:end]
		parts := make([]string, len(batch))
		for j, id := range batch {
			parts[j] = "songIdToAdd=" + url.QueryEscape(id)
		}
		uri := "updatePlaylist?playlistId=" + url.QueryEscape(playlistID) +
			"&u=" + url.QueryEscape(username) +
			"&" + strings.Join(parts, "&")
		var base struct{}
		if err := apiGet(uri, &base); err != nil {
			return err
		}
	}
	return nil
}

// playlistEntryCount returns the number of entries in a playlist.
func playlistEntryCount(username, playlistID string) (int, error) {
	var pr playlistResp
	uri := "getPlaylist?id=" + url.QueryEscape(playlistID) + "&u=" + url.QueryEscape(username)
	if err := apiGet(uri, &pr); err != nil {
		return 0, err
	}
	if pr.SubsonicResponse.Playlist == nil {
		return 0, nil
	}
	return len(pr.SubsonicResponse.Playlist.Entry), nil
}

// --- KVStore helpers ---

// loadPlayedSet loads the set of scrobbled track IDs for a given key.
func loadPlayedSet(key string) map[string]bool {
	data, exists, _ := host.KVStoreGet(key)
	if !exists || len(data) == 0 {
		return make(map[string]bool)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return make(map[string]bool)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// savePlayedSet persists the set of scrobbled track IDs.
func savePlayedSet(key string, set map[string]bool) {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	data, _ := json.Marshal(ids)
	host.KVStoreSet(key, data)
}

// --- Config helpers ---

func configStr(key, fallback string) string {
	if v, ok := host.ConfigGet(key); ok && v != "" {
		return v
	}
	return fallback
}

func configInt(key string, fallback int) int {
	if v, ok := host.ConfigGetInt(key); ok {
		return int(v)
	}
	return fallback
}

// configBool reads a boolean config value. Navidrome stores booleans as the
// strings "true"/"false", so we parse those and fall back to the provided
// default when the key is missing or has an unexpected value.
func configBool(key string, fallback bool) bool {
	v, ok := host.ConfigGet(key)
	if !ok {
		return fallback
	}
	switch v {
	case "true":
		return true
	case "false":
		return false
	default:
		return fallback
	}
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// shouldTriggerManualRefresh rate-limits manual refresh triggers using a KVStore
// timestamp so repeated scrobbles (while the toggle is on) don't schedule
// multiple one-time rebuilds.
func shouldTriggerManualRefresh() bool {
	data, exists, _ := host.KVStoreGet("fr:manual_refresh_time")
	if exists {
		var lastTime int64
		json.Unmarshal(data, &lastTime)
		if time.Now().Unix()-lastTime < int64(manualRefreshRateLimit) {
			return false
		}
	}
	ts, _ := json.Marshal(time.Now().Unix())
	host.KVStoreSet("fr:manual_refresh_time", ts)
	return true
}

// --- Logging ---

// Neither os.Stderr nor pdk.Log reach docker logs from inside Navidrome's WASM
// sandbox. Instead, log lines are written to the plugin's KVStore as a rolling
// buffer (last 50 lines). Read them with the Python snippet in README.md.

const (
	logKey      = "log:buffer"
	logMaxLines = 50
)

// logf appends a timestamped line to the KVStore log buffer when logging is
// enabled in the plugin config.
func logf(format string, args ...interface{}) {
	v, ok := host.ConfigGet("logging")
	if !ok || v != "true" {
		return
	}

	line := time.Now().UTC().Format("2006-01-02 15:04:05") + " " + fmt.Sprintf(format, args...)

	data, _, _ := host.KVStoreGet(logKey)
	var lines []string
	if len(data) > 0 {
		json.Unmarshal(data, &lines)
	}

	lines = append(lines, line)
	if len(lines) > logMaxLines {
		lines = lines[len(lines)-logMaxLines:]
	}

	data, _ = json.Marshal(lines)
	host.KVStoreSet(logKey, data)
}

func init() {
	scrobbler.Register(&plugin{})
	scheduler.Register(&plugin{})
	lifecycle.Register(&plugin{})
}

func main() {}
