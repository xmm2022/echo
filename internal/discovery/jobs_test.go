package discovery

import (
	"context"
	"testing"

	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestJobHandlersRegisterAllDiscoveryKinds(t *testing.T) {
	handlers := JobHandlers(Deps{})
	for _, kind := range []string{
		KindSourceCrawl,
		KindSubscriptionCheck,
		KindDispatch,
		KindReconcile,
		KindTMDBRefresh,
	} {
		if handlers[kind] == nil {
			t.Fatalf("missing handler for %s", kind)
		}
	}
}

func TestTMDBRefreshHandlerAcceptsEmptyPayload(t *testing.T) {
	st := openDiscoveryTestStore(t)
	handler := JobHandlers(Deps{
		Store: NewStore(st),
		TMDB:  fakeTMDBClient{},
	})[KindTMDBRefresh]

	if err := handler(context.Background(), queries.Job{ID: 1, Kind: KindTMDBRefresh, Payload: ""}); err != nil {
		t.Fatalf("handler empty payload: %v", err)
	}
}

type fakeTMDBClient struct{}

func (fakeTMDBClient) MovieDetails(context.Context, string) (tmdb.Media, error) {
	return tmdb.Media{}, nil
}

func (fakeTMDBClient) TVDetails(context.Context, string) (tmdb.Media, error) {
	return tmdb.Media{}, nil
}

func (fakeTMDBClient) Search(context.Context, string, string) ([]tmdb.Media, error) {
	return nil, nil
}
