package discovery

import (
	"time"

	"github.com/xmm2022/echo/internal/discovery/dispatch"
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

type Subscription = storeq.DiscoverySubscription
type SubscriptionMatch = storeq.SubscriptionMatch

type Source struct {
	ID         int64
	Kind       SourceKind
	Name       string
	ConfigJSON string
	ConfigJson string
	SecretRef  string
}

func (s Source) ConfigText() string {
	if s.ConfigJson != "" {
		return s.ConfigJson
	}
	return s.ConfigJSON
}

type RunIDs struct {
	SourceID       int64
	SubscriptionID int64
	JobID          int64
}

type SubscriptionBundle struct {
	SubscriptionID     int64
	TMDBID             string
	MediaType          string
	LibraryID          int64
	ProducerProfileID  int64
	RuleProfileID      int64
	MatchMode          string
	RuleProfileVersion int64
	RuleProfileJSON    string
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

type SourceCrawlResult struct {
	Items           []ParsedResource
	TelegramCursors []TelegramCursorUpdate
}

type TelegramCursorUpdate struct {
	ChannelRef      string
	LastMessageID   int64
	LastMessageDate int64
}

type CandidateResource struct {
	ID               int64
	Provider         Provider
	LinkKind         LinkKind
	TMDBID           string
	MediaType        string
	Title            string
	SeasonNumber     int64
	EpisodeStart     int64
	EpisodeEnd       int64
	ShareCode        string
	ReceiveCode      string
	ShareURLRedacted string
	FeatureJSON      string
}

type Match struct {
	ID int64
}

type DispatchBundle struct {
	MatchID        int64
	SubscriptionID int64
	ResourceID     int64
	Profile        dispatch.ProducerProfile
	Resource       dispatch.Resource
}

type ReconcileBundle struct {
	MatchID         int64
	JobID           int64
	JobStatus       string
	JobProgressJSON string
	JobError        string
}

type MatchResult struct {
	MatchID        int64
	Decision       string
	DispatchState  string
	LibraryEntryID int64
	BlobID         int64
	CopyID         int64
	FailureKind    string
	FailureMessage string
	FinishedAt     time.Time
}
