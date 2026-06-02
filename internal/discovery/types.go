package discovery

import (
	"time"

	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type Provider string
type SourceKind string
type LinkKind string

const (
	Provider115           Provider   = "115"
	SourceTelegramMTProto SourceKind = "telegram_mtproto"
	SourcePosterHTTP      SourceKind = "poster_http"
	Link115Share          LinkKind   = "115_share"
)

type Source = storeq.DiscoverySource
type Subscription = storeq.DiscoverySubscription
type SubscriptionMatch = storeq.SubscriptionMatch

type SubscriptionBundle struct {
	Subscription Subscription
	Bundle       storeq.GetDiscoverySubscriptionBundleRow
}

type ParsedResource struct {
	Provider         Provider
	LinkKind         LinkKind
	ExternalKey      string
	TMDBID           string
	MediaType        string
	Title            string
	SeasonNumber     int64
	EpisodeStart     int64
	EpisodeEnd       int64
	ShareCode        string
	ReceiveCode      string
	ShareURLRedacted string
	RawText          []byte
	RawTextRedacted  string
	RawTextRef       string
	FeatureJSON      string
	ParsedJSON       string
	ObservedAt       time.Time
}
