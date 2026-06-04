package media

type TMDBSummaryDTO struct {
	TMDBID       string `json:"tmdb_id"`
	MediaType    string `json:"media_type"`
	Title        string `json:"title"`
	ReleaseYear  int    `json:"release_year,omitempty"`
	Overview     string `json:"overview,omitempty"`
	PosterPath   string `json:"poster_path,omitempty"`
	Availability string `json:"availability"`
}

type RequestDTO struct {
	ID             int64  `json:"id"`
	TMDBID         string `json:"tmdb_id"`
	MediaType      string `json:"media_type"`
	Title          string `json:"title"`
	TargetLabel    string `json:"target_label"`
	Status         string `json:"status"`
	SafeReason     string `json:"safe_reason,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	ReviewedAt     int64  `json:"reviewed_at,omitempty"`
	SubscriptionID int64  `json:"subscription_id,omitempty"`
}

type SubscriptionDTO struct {
	ID             int64  `json:"id"`
	TMDBID         string `json:"tmdb_id"`
	MediaType      string `json:"media_type"`
	Title          string `json:"title"`
	TargetLabel    string `json:"target_label,omitempty"`
	UserStatus     string `json:"user_status"`
	PipelineStatus string `json:"pipeline_status"`
	LatestState    string `json:"latest_state"`
	UpdatedAt      int64  `json:"updated_at"`
}

type TargetDTO struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	MediaType   string `json:"media_type,omitempty"`
	Default     bool   `json:"default"`
	RequestMode string `json:"request_mode"`
	CanSearch   bool   `json:"can_search"`
}
