package embyproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/store/queries"
)

// RewriteContext carries the per-request identity and addressing needed to rewrite a
// PlaybackInfo body: the public Echo origin/prefix the client must use, the Emby
// item/server/user being played, and the Echo user the request authenticated as.
type RewriteContext struct {
	PublicBaseURL string
	ProxyPrefix   string
	ItemID        string
	EmbyServerID  string
	EmbyUserID    string
	EchoUserID    string
	DeviceID      string
	PlaySessionID string
}

// RewriteResult summarizes what Rewrite did so the caller can log/meter the outcome.
// SessionsCreated counts mapped sources that received a playback session; ErrorTokensCreated
// counts mapped sources whose overall outcome is a controlled error (i.e. got NO session) —
// a TranscodingUrl neutralized on an otherwise session-backed source is NOT counted here.
// ErrorReason is the reason of the last error-backed source (empty when none).
type RewriteResult struct {
	SessionsCreated    int
	ErrorTokensCreated int
	ErrorReason        string
	MappedSources      int
	UnmappedSources    int
}

// MappedSource is the Echo-side resolution of one Emby MediaSources[].Path: the library
// entry (and its blob) the path maps to, plus the mapping rule and normalized path that
// produced the match. It carries no upstream URL or auth material by construction.
type MappedSource struct {
	LibraryID      int64
	LibraryEntryID int64
	BlobID         int64
	RelPath        string
	PathNorm       string
	MappingID      int64
}

// SourceMapper resolves an Emby MediaSources[].Path to an Echo library entry.
type SourceMapper interface {
	// MapSource resolves an Emby MediaSources[].Path to an Echo library entry via the
	// configured emby_library_mappings + the item-mapping cache. Returns (_, false, nil)
	// when no mapping matches (unmapped). rawPath may be empty.
	MapSource(ctx context.Context, embyServerID, itemID, mediaSourceID, rawPath string) (MappedSource, bool, error)
}

// SessionCreator mints the DB-backed tokens that replace upstream playable URLs.
type SessionCreator interface {
	CreatePlaybackSession(context.Context, CreatePlaybackSessionInput) (string, queries.PlaybackSession, error)
	CreateErrorToken(context.Context, CreateErrorTokenInput) (string, queries.PlaybackErrorToken, error)
}

// QuotaChecker reports whether starting another stream is allowed for the Echo user.
type QuotaChecker interface {
	CheckStreamAllowed(ctx context.Context, echoUserID string) error
}

// LiveCopyChecker reports whether the user has at least one playable copy of an entry.
type LiveCopyChecker interface {
	ResolveCopies(ctx context.Context, echoUserID string, libraryEntryID int64, preferProvider string, limit int64) ([]queries.ListPlayableCopiesForUserRow, error)
}

// Rewriter rewrites a PlaybackInfo JSON body so that every mapped MediaSource exposes ONLY
// Echo /stream/{token} or /error/{token} URLs and carries no upstream auth material. It is
// the playback-bypass security boundary: a mapped source must never leave with an upstream
// Emby playable URL, an api_key, or any RequiredHttpHeaders.
type Rewriter struct {
	Mapper           SourceMapper
	Sessions         SessionCreator
	Quota            QuotaChecker
	Resolver         LiveCopyChecker
	RedactMappedPath bool
}

// NewRewriter builds a Rewriter with RedactMappedPath enabled — the safe default is set
// explicitly here rather than relying on the Go zero value, so a mapped source's upstream
// filesystem Path is always replaced with a non-playable echo:// placeholder.
func NewRewriter(m SourceMapper, s SessionCreator, q QuotaChecker, r LiveCopyChecker) *Rewriter {
	return &Rewriter{
		Mapper:           m,
		Sessions:         s,
		Quota:            q,
		Resolver:         r,
		RedactMappedPath: true,
	}
}

// playableURLAllowlist is the exact set of *Url fields Echo knows how to neutralize on a
// mapped source. Any other key ending in "Url" is an UNKNOWN playable field and forces a
// fail-closed outcome so an unrecognized upstream locator can never leak.
var playableURLAllowlist = map[string]struct{}{
	"DirectStreamUrl": {},
	"StreamUrl":       {},
	"TranscodingUrl":  {},
}

// Rewrite parses raw as a generic JSON AST, rewrites every mapped MediaSource's playable
// URL fields to Echo tokens (and clears its auth headers + Path), leaves unmapped sources
// untouched, and re-marshals. Any mapper/collaborator error other than a recognized
// playback sentinel fails closed (returns the error so the handler can answer 503), to
// guarantee no partially-rewritten body leaks an upstream URL.
func (rw *Rewriter) Rewrite(raw []byte, rctx RewriteContext) ([]byte, RewriteResult, error) {
	var result RewriteResult

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, result, err
	}

	sources, ok := doc["MediaSources"].([]any)
	if !ok || len(sources) == 0 {
		// Nothing playable to rewrite; re-marshal unchanged.
		out, err := json.Marshal(doc)
		if err != nil {
			return nil, result, err
		}
		return out, result, nil
	}

	ctx := context.Background()
	publicBase := strings.TrimRight(rctx.PublicBaseURL, "/")
	prefix := rctx.ProxyPrefix
	if prefix == "" {
		prefix = "/emby"
	}

	for _, item := range sources {
		source, ok := item.(map[string]any)
		if !ok {
			// A non-object MediaSources element is malformed; fail closed rather than
			// pass it through unexamined.
			return nil, result, errors.New("playbackinfo: MediaSources element is not an object")
		}

		mediaSourceID, _ := source["Id"].(string)
		rawPath, _ := source["Path"].(string)

		mapped, mappedOK, err := rw.Mapper.MapSource(ctx, rctx.EmbyServerID, rctx.ItemID, mediaSourceID, rawPath)
		if err != nil {
			return nil, result, err
		}
		if !mappedOK {
			// Genuinely upstream content Echo does not manage: leave untouched.
			result.UnmappedSources++
			continue
		}

		result.MappedSources++
		if err := rw.rewriteMappedSource(ctx, source, mediaSourceID, mapped, rctx, publicBase, prefix, &result); err != nil {
			return nil, result, err
		}
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, result, err
	}
	return out, result, nil
}

// rewriteMappedSource neutralizes a single mapped MediaSource in place. On return the
// source carries no upstream playable URL, no auth headers, and a redacted Path. Errors are
// returned (never swallowed) so the caller fails the whole body closed.
func (rw *Rewriter) rewriteMappedSource(
	ctx context.Context,
	source map[string]any,
	mediaSourceID string,
	mapped MappedSource,
	rctx RewriteContext,
	publicBase, prefix string,
	result *RewriteResult,
) error {
	// Always-applied hardening (regardless of session vs error outcome): drop auth headers
	// and redact the upstream filesystem Path. Done up front so any early return below is
	// still safe.
	source["RequiredHttpHeaders"] = map[string]any{}
	if _, ok := source["AddApiKeyToDirectStreamUrl"]; ok {
		source["AddApiKeyToDirectStreamUrl"] = false
	}
	if rw.RedactMappedPath {
		source["Path"] = "echo://mapped/" + strconv.FormatInt(mapped.LibraryEntryID, 10)
	}
	redactNestedURLFields(source)

	urlFields := urlFieldNames(source)

	// Unknown *Url field present => fail closed. Point every *Url at an error token so no
	// upstream locator survives. This source gets NO playback session.
	if hasUnknownURLField(urlFields) {
		const reason = "unknown_playable_url"
		token, err := rw.mintErrorToken(ctx, rctx, mediaSourceID, reason, 502)
		if err != nil {
			return err
		}
		errURL := publicBase + prefix + "/error/" + token
		for _, name := range urlFields {
			source[name] = errURL
		}
		result.ErrorTokensCreated++
		result.ErrorReason = reason
		return nil
	}

	// Only allowlisted *Url fields remain. A source is Echo-playable iff it advertises a
	// direct-play locator (DirectStreamUrl or StreamUrl), or Emby reports it as direct
	// playable without materializing a URL field (real local-file PlaybackInfo does this).
	_, hasDirect := source["DirectStreamUrl"]
	_, hasStream := source["StreamUrl"]
	directPlayable := boolField(source, "SupportsDirectPlay") || boolField(source, "SupportsDirectStream")
	playable := hasDirect || hasStream || directPlayable

	if playable {
		// Authorize + check a live copy exists + check quota before minting a session.
		reason, fatal := rw.authorizeSource(ctx, rctx, mapped)
		if fatal != nil {
			return fatal
		}
		if reason == "" {
			// Authorized and in quota: mint a real playback session.
			token, _, err := rw.Sessions.CreatePlaybackSession(ctx, CreatePlaybackSessionInput{
				EchoUserID:     rctx.EchoUserID,
				EmbyServerID:   rctx.EmbyServerID,
				EmbyUserID:     rctx.EmbyUserID,
				DeviceID:       rctx.DeviceID,
				ItemID:         rctx.ItemID,
				MediaSourceID:  mediaSourceID,
				LibraryEntryID: mapped.LibraryEntryID,
				BlobID:         mapped.BlobID,
			})
			if err != nil {
				return err
			}
			streamURL := publicBase + prefix + "/stream/" + token
			if hasDirect || (!hasStream && directPlayable) {
				source["DirectStreamUrl"] = streamURL
			}
			if hasStream {
				source["StreamUrl"] = streamURL
			}
			result.SessionsCreated++

			// v0.2 does not take over transcode: neutralize any TranscodingUrl to an error
			// URL. This is a per-field neutralization on a session-backed source and does
			// NOT count toward ErrorTokensCreated.
			if _, hasTranscode := source["TranscodingUrl"]; hasTranscode {
				errToken, err := rw.mintErrorToken(ctx, rctx, mediaSourceID, "unsupported_transcode", 415)
				if err != nil {
					return err
				}
				source["TranscodingUrl"] = publicBase + prefix + "/error/" + errToken
			}
			return nil
		}
		// A check failed (unauthorized / no live copy / quota / temporarily unavailable):
		// fall through to the error-backed path with the determined reason.
		if !hasDirect && !hasStream && directPlayable {
			urlFields = append(urlFields, "DirectStreamUrl")
		}
		return rw.makeErrorBacked(ctx, source, mediaSourceID, urlFields, rctx, publicBase, prefix, reason, result)
	}

	// Not Echo-playable (e.g. transcode-only): error-backed with reason unsupported_transcode.
	return rw.makeErrorBacked(ctx, source, mediaSourceID, urlFields, rctx, publicBase, prefix, "unsupported_transcode", result)
}

func boolField(source map[string]any, name string) bool {
	value, _ := source[name].(bool)
	return value
}

// makeErrorBacked mints ONE error token with reason and points every present *Url field at
// it. This is a source-level error outcome (no session), so it increments ErrorTokensCreated.
func (rw *Rewriter) makeErrorBacked(
	ctx context.Context,
	source map[string]any,
	mediaSourceID string,
	urlFields []string,
	rctx RewriteContext,
	publicBase, prefix, reason string,
	result *RewriteResult,
) error {
	token, err := rw.mintErrorToken(ctx, rctx, mediaSourceID, reason, errorStatusForReason(reason))
	if err != nil {
		return err
	}
	errURL := publicBase + prefix + "/error/" + token
	for _, name := range urlFields {
		source[name] = errURL
	}
	result.ErrorTokensCreated++
	result.ErrorReason = reason
	return nil
}

// authorizeSource runs the resolver + quota checks for a playable mapped source. It returns
// ("", nil) when the source is authorized and in quota. It returns (reason, nil) for a
// controlled failure that should become an error token. It returns ("", err) for an
// unexpected error that must fail the whole body closed.
func (rw *Rewriter) authorizeSource(ctx context.Context, rctx RewriteContext, mapped MappedSource) (reason string, fatal error) {
	copies, err := rw.Resolver.ResolveCopies(ctx, rctx.EchoUserID, mapped.LibraryEntryID, "", 5)
	switch {
	case errors.Is(err, playback.ErrUnauthorized):
		return "unauthorized", nil
	case errors.Is(err, playback.ErrEntryMissing):
		return "temporary_unavailable", nil
	case err != nil:
		return "", err
	}
	if len(copies) == 0 {
		return "temporary_unavailable", nil
	}

	if qerr := rw.Quota.CheckStreamAllowed(ctx, rctx.EchoUserID); qerr != nil {
		if errors.Is(qerr, playback.ErrQuotaExceeded) {
			return "quota_exceeded", nil
		}
		return "", qerr
	}
	return "", nil
}

// mintErrorToken creates an error token and returns its full selector.secret token string.
func (rw *Rewriter) mintErrorToken(ctx context.Context, rctx RewriteContext, mediaSourceID, reason string, httpStatus int) (string, error) {
	token, _, err := rw.Sessions.CreateErrorToken(ctx, CreateErrorTokenInput{
		EchoUserID:    nullString(rctx.EchoUserID),
		EmbyServerID:  nullString(rctx.EmbyServerID),
		EmbyUserID:    rctx.EmbyUserID,
		ItemID:        rctx.ItemID,
		MediaSourceID: mediaSourceID,
		Reason:        reason,
		HTTPStatus:    httpStatus,
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// errorStatusForReason maps an error reason to the HTTP status the player should observe.
func errorStatusForReason(reason string) int {
	switch reason {
	case "unauthorized":
		return 403
	case "quota_exceeded":
		return 429
	case "unsupported_transcode":
		return 415
	case "unknown_playable_url":
		return 502
	case "no_live_copy", "temporary_unavailable":
		return 503
	default:
		return 503
	}
}

// urlFieldNames returns the source keys ending in "Url", sorted for deterministic output.
func urlFieldNames(source map[string]any) []string {
	var names []string
	for key, value := range source {
		if _, ok := value.(string); ok && strings.HasSuffix(key, "Url") {
			names = append(names, key)
		}
	}
	sort.Strings(names)
	return names
}

func redactNestedURLFields(source map[string]any) {
	for key, value := range source {
		if _, ok := value.(string); ok && strings.HasSuffix(key, "Url") {
			continue
		}
		redactURLFieldsRecursive(value)
	}
}

func redactURLFieldsRecursive(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if _, ok := child.(string); ok && strings.HasSuffix(key, "Url") {
				delete(node, key)
				continue
			}
			redactURLFieldsRecursive(child)
		}
	case []any:
		for _, child := range node {
			redactURLFieldsRecursive(child)
		}
	}
}

// hasUnknownURLField reports whether any *Url field is outside the playable allowlist.
func hasUnknownURLField(urlFields []string) bool {
	for _, name := range urlFields {
		if _, ok := playableURLAllowlist[name]; !ok {
			return true
		}
	}
	return false
}

// dbItemMappingQuerier is the subset of *queries.Queries the DBSourceMapper needs.
// *queries.Queries satisfies it, so callers pass store.Queries.
type dbItemMappingQuerier interface {
	ListEnabledEmbyLibraryMappings(context.Context, queries.ListEnabledEmbyLibraryMappingsParams) ([]queries.EmbyLibraryMapping, error)
	GetLibraryEntry(context.Context, queries.GetLibraryEntryParams) (queries.LibraryEntry, error)
	GetValidEmbyItemMapping(context.Context, queries.GetValidEmbyItemMappingParams) (queries.EmbyItemMapping, error)
	ListItemMappingsByItem(context.Context, queries.ListItemMappingsByItemParams) ([]queries.EmbyItemMapping, error)
	UpsertEmbyItemMapping(context.Context, queries.UpsertEmbyItemMappingParams) error
}

// DBSourceMapper resolves Emby paths to Echo library entries using the configured
// emby_library_mappings, and records the resolution into the emby_item_mappings cache. The
// cache is only a hint: every call recomputes the mapping from the path and re-validates the
// library entry, so a stale cache row can never act as an authorization grant.
type DBSourceMapper struct {
	q   dbItemMappingQuerier
	now func() time.Time
}

// NewDBSourceMapper builds a DBSourceMapper over q with the injected clock.
func NewDBSourceMapper(q dbItemMappingQuerier, now func() time.Time) *DBSourceMapper {
	return &DBSourceMapper{q: q, now: now}
}

func (m *DBSourceMapper) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// MapSource implements SourceMapper. See the interface doc for the (_, false, nil)
// "unmapped" contract. Any error returned must fail the rewrite closed.
func (m *DBSourceMapper) MapSource(ctx context.Context, embyServerID, itemID, mediaSourceID, rawPath string) (MappedSource, bool, error) {
	if rawPath == "" {
		return m.mapCachedSource(ctx, embyServerID, itemID, mediaSourceID)
	}

	mappings, err := m.q.ListEnabledEmbyLibraryMappings(ctx, queries.ListEnabledEmbyLibraryMappingsParams{EmbyServerID: embyServerID})
	if err != nil {
		return MappedSource{}, false, err
	}
	rules := make([]MappingRule, 0, len(mappings))
	for _, row := range mappings {
		rules = append(rules, MappingRule{
			ID:            row.ID,
			LibraryID:     row.LibraryID,
			PrefixNorm:    row.EmbyPathPrefixNorm,
			EchoRelPrefix: row.EchoRelPrefix,
			CaseSensitive: row.CaseSensitive != 0,
		})
	}

	res, err := MatchPath(rules, rawPath)
	if errors.Is(err, ErrNoMapping) {
		return MappedSource{}, false, nil
	}
	if err != nil {
		return MappedSource{}, false, err
	}

	entry, err := m.q.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{
		LibraryID: res.LibraryID,
		RelPath:   res.RelPath,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// The configured prefix matched but Echo has no such entry: not Echo content.
		return MappedSource{}, false, nil
	}
	if err != nil {
		return MappedSource{}, false, err
	}

	// Record/refresh the item-mapping cache hint. We always recompute+upsert in Phase 4
	// (the GetValidEmbyItemMapping fast-path is intentionally skipped) because the cache is
	// re-validated here and is never treated as an auth grant.
	now := m.clock().Unix()
	if err := m.q.UpsertEmbyItemMapping(ctx, queries.UpsertEmbyItemMappingParams{
		EmbyServerID:          embyServerID,
		EmbyItemID:            itemID,
		MediaSourceID:         mediaSourceID,
		MappingID:             res.MappingID,
		MediaSourcePathRaw:    rawPath,
		MediaSourcePathNorm:   res.PathNorm,
		PathNormVersion:       PathNormVersion,
		LibraryID:             res.LibraryID,
		RelPath:               res.RelPath,
		LibraryEntryID:        entry.ID,
		BlobID:                entry.BlobID,
		LibraryEntryUpdatedAt: entry.UpdatedAt,
		EmbyItemEtag:          sql.NullString{},
		LastSeenAt:            now,
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		return MappedSource{}, false, err
	}

	return MappedSource{
		LibraryID:      res.LibraryID,
		LibraryEntryID: entry.ID,
		BlobID:         entry.BlobID,
		RelPath:        res.RelPath,
		PathNorm:       res.PathNorm,
		MappingID:      res.MappingID,
	}, true, nil
}

func (m *DBSourceMapper) mapCachedSource(ctx context.Context, embyServerID, itemID, mediaSourceID string) (MappedSource, bool, error) {
	if itemID == "" {
		return MappedSource{}, false, nil
	}
	rows, err := m.q.ListItemMappingsByItem(ctx, queries.ListItemMappingsByItemParams{
		EmbyServerID: embyServerID,
		EmbyItemID:   itemID,
	})
	if err != nil {
		return MappedSource{}, false, err
	}
	if len(rows) == 0 {
		return MappedSource{}, false, nil
	}
	mappings, err := m.q.ListEnabledEmbyLibraryMappings(ctx, queries.ListEnabledEmbyLibraryMappingsParams{EmbyServerID: embyServerID})
	if err != nil {
		return MappedSource{}, false, err
	}
	enabledMappings := make(map[int64]struct{}, len(mappings))
	for _, row := range mappings {
		enabledMappings[row.ID] = struct{}{}
	}
	for _, row := range rows {
		if mediaSourceID != "" && row.MediaSourceID != mediaSourceID {
			continue
		}
		if row.PathNormVersion != PathNormVersion {
			continue
		}
		if _, ok := enabledMappings[row.MappingID]; !ok {
			continue
		}
		entry, err := m.q.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{
			LibraryID: row.LibraryID,
			RelPath:   row.RelPath,
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return MappedSource{}, false, err
		}
		if entry.ID != row.LibraryEntryID || entry.BlobID != row.BlobID || entry.UpdatedAt != row.LibraryEntryUpdatedAt {
			continue
		}
		return MappedSource{
			LibraryID:      row.LibraryID,
			LibraryEntryID: row.LibraryEntryID,
			BlobID:         row.BlobID,
			RelPath:        row.RelPath,
			PathNorm:       row.MediaSourcePathNorm,
			MappingID:      row.MappingID,
		}, true, nil
	}
	return MappedSource{}, false, nil
}

// maxPlaybackInfoBody caps how many bytes of an upstream PlaybackInfo response Echo will
// read into memory before rewriting/inspecting it. PlaybackInfo bodies are small JSON
// documents; the cap bounds memory and protects against a hostile/huge upstream body.
const maxPlaybackInfoBody = 8 << 20 // 8 MiB

// PlaybackInfoQuerier is the subset of *queries.Queries the PlaybackInfoHandler needs:
// it maps the Emby user to the Echo user and looks up the item-mapping evidence that
// decides fail-closed vs passthrough on a bad upstream response. *queries.Queries
// satisfies it, so callers pass store.Queries.
type PlaybackInfoQuerier interface {
	GetEmbyUserLink(ctx context.Context, arg queries.GetEmbyUserLinkParams) (queries.EmbyUserLink, error)
	ListItemMappingsByItem(ctx context.Context, arg queries.ListItemMappingsByItemParams) ([]queries.EmbyItemMapping, error)
}

// playbackInfoMetrics is the NARROW, nil-safe metrics surface the PlaybackInfo
// handler records its rewrite outcome through. Following the local-interface pattern
// used for streamMetrics, it keeps embyproxy from a hard dependency on the concrete
// metrics type and lets tests omit it (nil = no-op). *metrics.Metrics satisfies it.
type playbackInfoMetrics interface {
	PlaybackInfoRewrite(result string)
}

// PlaybackInfoConfig carries the addressing, identity scope, and DB access the
// PlaybackInfoHandler needs. PublicBaseURL/ProxyPrefix are the Echo origin+prefix written
// into rewritten URLs; UpstreamBase is the Emby origin requests are forwarded to;
// EmbyServerID scopes the user-link and item-mapping lookups. Metrics is optional
// (nil = unmetered).
type PlaybackInfoConfig struct {
	PublicBaseURL string
	ProxyPrefix   string
	EmbyServerID  string
	UpstreamBase  *url.URL
	Querier       PlaybackInfoQuerier
	Metrics       playbackInfoMetrics
}

// PlaybackInfoHandler proxies a PlaybackInfo request to upstream Emby and rewrites the JSON
// body so every mapped MediaSource exposes only Echo tokens (never an upstream playable URL
// or auth material). It is the playback-bypass HTTP boundary and is SECURITY-CRITICAL: it
// MUST NEVER hand the client an upstream Emby playable URL/auth material for a mapped or
// evidenced item. On any upstream outcome that is not a parseable JSON 2xx, it fails CLOSED
// with 503 whenever the item has mapping evidence OR the response carries a playable
// Location; only a genuinely unmapped, non-playable error is transparently passed through.
//
// upstream is the RoundTripper used to reach Emby; nil falls back to http.DefaultTransport.
func PlaybackInfoHandler(cfg PlaybackInfoConfig, rw *Rewriter, upstream http.RoundTripper, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	// Do NOT follow upstream redirects: a 3xx with a Location is a response Echo must
	// inspect (and, for a playable locator, fail closed on) — never chase. Chasing would
	// also hang on an unreachable redirect target.
	client := &http.Client{
		Transport:     upstream,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	prefix := cfg.ProxyPrefix
	if prefix == "" {
		prefix = "/emby"
	}
	// Parse the public base once for response-header rewriting on the passthrough path. A
	// parse failure leaves it nil, which RewriteLocation treats as "leave Location alone";
	// that is safe because the fail-closed branch (mapped evidence / playable Location)
	// never copies the upstream Location in the first place.
	publicBase, _ := url.Parse(cfg.PublicBaseURL)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// rewriteResult is the safe outcome enum recorded for echo_playbackinfo_rewrite_total
		// on every exit. It defaults to fail_closed (the safe outcome of every early return)
		// and is overwritten on the success / passthrough paths. The metric is nil-safe.
		rewriteResult := "fail_closed"
		defer func() {
			if cfg.Metrics != nil {
				cfg.Metrics.PlaybackInfoRewrite(rewriteResult)
			}
		}()

		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeFailClosed(w)
			return
		}
		ctx := r.Context()
		itemID := chi.URLParam(r, "item_id")

		// Build the upstream request: retarget origin to UpstreamBase, strip the proxy
		// prefix from the path, preserve method/query/body. Force Accept-Encoding: identity
		// so Go's transport does not auto-gzip/auto-decompress and we control decoding.
		outURL := *cfg.UpstreamBase
		outURL.Path = stripPrefixPath(r.URL.Path, prefix)
		outURL.RawQuery = r.URL.RawQuery
		upReq, err := http.NewRequestWithContext(ctx, r.Method, outURL.String(), r.Body)
		if err != nil {
			logger.Error("emby playbackinfo: build upstream request failed", "item", itemID, "err", err)
			writeFailClosed(w)
			return
		}
		copyForwardableHeaders(upReq.Header, r.Header)
		upReq.Header.Set("Accept-Encoding", "identity")

		resp, err := client.Do(upReq)
		if err != nil {
			logger.Error("emby playbackinfo: upstream request failed", "item", itemID, "err", err)
			writeFailClosed(w)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := readUpstreamBody(resp)
		if err != nil {
			logger.Error("emby playbackinfo: read upstream body failed", "item", itemID, "err", err)
			writeFailClosed(w)
			return
		}

		is2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
		isJSON := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json")

		if is2xx && isJSON {
			embyUserID := r.URL.Query().Get("UserId")
			echoUserID, err := resolveEchoUser(ctx, cfg, embyUserID)
			if err != nil {
				logger.Error("emby playbackinfo: resolve echo user failed", "item", itemID, "err", err)
				writeFailClosed(w)
				return
			}

			out, rwResult, err := rw.Rewrite(body, RewriteContext{
				PublicBaseURL: cfg.PublicBaseURL,
				ProxyPrefix:   prefix,
				ItemID:        itemID,
				EmbyServerID:  cfg.EmbyServerID,
				EmbyUserID:    embyUserID,
				EchoUserID:    echoUserID,
				DeviceID:      r.URL.Query().Get("DeviceId"),
			})
			if err != nil {
				// A rewrite error means we could NOT guarantee the body is neutralized:
				// fail closed rather than risk leaking an upstream URL.
				logger.Error("emby playbackinfo: rewrite failed", "item", itemID, "err", err)
				writeFailClosed(w)
				return
			}

			// Record the safe rewrite outcome: error_url when any source became an error
			// token, rewritten when at least one session was minted, else passthrough (no
			// mapped sources to rewrite).
			switch {
			case rwResult.ErrorTokensCreated > 0:
				rewriteResult = "error_url"
			case rwResult.SessionsCreated > 0:
				rewriteResult = "rewritten"
			default:
				rewriteResult = "passthrough"
			}

			// Success: never echo the upstream Content-Encoding/Content-Length; the body has
			// been decoded + rewritten, so set fresh headers describing the rewritten bytes.
			h := w.Header()
			h.Del("Content-Encoding")
			h.Del("Content-Length")
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Length", strconv.Itoa(len(out)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(out)
			return
		}

		// Non-2xx, non-JSON, or unparseable JSON: decide fail-closed vs transparent passthrough.
		mappedEvidence := false
		if rows, err := cfg.Querier.ListItemMappingsByItem(ctx, queries.ListItemMappingsByItemParams{
			EmbyServerID: cfg.EmbyServerID,
			EmbyItemID:   itemID,
		}); err == nil {
			mappedEvidence = len(rows) > 0
		} else {
			// If we cannot even check for mapping evidence, we cannot prove the item is
			// unmapped: fail closed rather than risk passing a playable upstream response.
			logger.Error("emby playbackinfo: list item mappings failed", "item", itemID, "err", err)
			writeFailClosed(w)
			return
		}

		playableLocation := looksPlayableLocator(resp.Header.Get("Location"))

		if mappedEvidence || playableLocation {
			// SECURITY: an evidenced item, or a response that hands back a playable upstream
			// locator, must never reach the client untokenized. Drop the upstream
			// Location/body entirely and answer a controlled 503.
			logger.Warn("emby playbackinfo: failing closed on non-json upstream",
				"item", itemID, "status", resp.StatusCode,
				"mapped_evidence", mappedEvidence, "playable_location", playableLocation)
			writeFailClosed(w)
			return
		}

		// Genuinely unmapped + non-playable: transparently relay the upstream response,
		// rewriting Location/Set-Cookie back through Echo via the shared header helper.
		rewriteResult = "passthrough"
		rewritten := RewriteResponseHeaders(resp.Header, HeaderConfig{
			UpstreamBase: cfg.UpstreamBase,
			PublicBase:   publicBase,
			ProxyPrefix:  prefix,
		})
		dst := w.Header()
		for key, values := range rewritten {
			dst[key] = values
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	})
}

// resolveEchoUser maps the Emby user id to the linked Echo user id. An unknown user
// (sql.ErrNoRows) is NOT an error: it yields "" so mapped sources fail authorization inside
// Rewrite and become error tokens (safe). Any other DB error is returned so the caller fails
// the request closed.
func resolveEchoUser(ctx context.Context, cfg PlaybackInfoConfig, embyUserID string) (string, error) {
	link, err := cfg.Querier.GetEmbyUserLink(ctx, queries.GetEmbyUserLinkParams{
		EmbyServerID: cfg.EmbyServerID,
		EmbyUserID:   embyUserID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return link.EchoUserID, nil
}

// readUpstreamBody reads up to maxPlaybackInfoBody bytes of resp, gunzipping when the
// upstream declared Content-Encoding: gzip (which it may do even though Echo requested
// identity, so this is handled defensively).
func readUpstreamBody(resp *http.Response) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaybackInfoBody))
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		decoded, err := io.ReadAll(io.LimitReader(gz, maxPlaybackInfoBody))
		if err != nil {
			return nil, err
		}
		return decoded, nil
	}
	return raw, nil
}

// copyForwardableHeaders copies request headers from src to dst, dropping hop-by-hop
// headers and the Accept-Encoding the caller sent (the handler pins its own identity
// encoding). Host is carried by the request line, not a header here.
func copyForwardableHeaders(dst, src http.Header) {
	stripped := StripHopByHop(src)
	for key, values := range stripped {
		if strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		cp := make([]string, len(values))
		copy(cp, values)
		dst[key] = cp
	}
}

// looksPlayableLocator reports whether a Location value points at a playback/download/
// transcode locator that must never be handed to the client untokenized. It inspects only
// the path (case-insensitively) so query strings cannot smuggle a match past it, and mirrors
// the spirit of guard.go's streamLikePath. An unparseable Location is treated as playable
// (fail safe).
func looksPlayableLocator(raw string) bool {
	if raw == "" {
		return false
	}
	path := raw
	if u, err := url.Parse(raw); err == nil {
		path = u.Path
	} else {
		return true
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, "/videos/") ||
		strings.Contains(lower, "/audio/") ||
		strings.Contains(lower, "/stream") ||
		strings.Contains(lower, "/download") ||
		strings.Contains(lower, "/hls") ||
		strings.Contains(lower, ".m3u8")
}

// writeFailClosed writes Echo's controlled 503 for the PlaybackInfo boundary. It mirrors the
// fail-closed JSON shape used elsewhere (503 + X-Echo-Reason: temporary_unavailable) and
// deliberately copies NOTHING from the upstream response.
func writeFailClosed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Echo-Reason", "temporary_unavailable")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary_unavailable"})
}
