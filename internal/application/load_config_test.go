package application

import (
	"context"
	"errors"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

type fakeSource struct {
	kind    domain.ProviderKind
	payload domain.Payload
	err     error
	called  bool
}

func (f *fakeSource) Kind() domain.ProviderKind { return f.kind }
func (f *fakeSource) Fetch(ctx context.Context, ref domain.Reference, mode domain.FetchMode) (domain.Payload, error) {
	f.called = true
	return f.payload, f.err
}

func TestLoadConfigUseCase_MatchAndFetch(t *testing.T) {
	src := &fakeSource{kind: domain.ProviderGCP, payload: domain.Payload("FOO=bar")}
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return true }, Source: src},
		},
		Strict: true,
	}
	res, err := uc.Run(context.Background(), "projects/p/locations/l/parameters/x", "v1", domain.FetchGet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Provider != domain.ProviderGCP {
		t.Errorf("provider = %q, want gcp", res.Provider)
	}
	if string(res.Payload) != "FOO=bar" {
		t.Errorf("payload = %q, want FOO=bar", res.Payload)
	}
	if !src.called {
		t.Error("Fetch was not called")
	}
}

func TestLoadConfigUseCase_NoMatch(t *testing.T) {
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return false }, Source: &fakeSource{kind: domain.ProviderAWS}},
		},
		Strict: true,
	}
	_, err := uc.Run(context.Background(), "loc", "v", domain.FetchGet)
	if err == nil {
		t.Fatal("expected error for no matching source")
	}
}

func TestLoadConfigUseCase_StrictFetchError(t *testing.T) {
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return true }, Source: &fakeSource{kind: domain.ProviderAWS, err: errors.New("boom")}},
		},
		Strict: true,
	}
	_, err := uc.Run(context.Background(), "loc", "v", domain.FetchGet)
	if err == nil {
		t.Fatal("expected strict fetch error")
	}
}

func TestLoadConfigUseCase_NonStrictFetchError(t *testing.T) {
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return true }, Source: &fakeSource{kind: domain.ProviderAWS, err: errors.New("boom")}},
		},
		Strict: false,
	}
	res, err := uc.Run(context.Background(), "loc", "v", domain.FetchGet)
	if err != nil {
		t.Fatalf("non-strict should swallow: %v", err)
	}
	if string(res.Payload) != "" {
		t.Errorf("payload = %q, want empty", res.Payload)
	}
	if res.Provider != domain.ProviderAWS {
		t.Errorf("provider = %q, want aws", res.Provider)
	}
}

func TestLoadConfigUseCase_FactoryBuildsSource(t *testing.T) {
	src := &fakeSource{kind: domain.ProviderGCP, payload: "X=1"}
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return true }, Source: nil, Factory: func(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error) {
				return src, nil
			}},
		},
		Strict: true,
	}
	if _, err := uc.Run(context.Background(), "loc", "v", domain.FetchGet); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !src.called {
		t.Error("factory-built source was not used")
	}
}

func TestLoadConfigUseCase_FactoryError(t *testing.T) {
	uc := LoadConfigUseCase{
		Registry: []SourceEntry{
			{Match: func(string) bool { return true }, Source: nil, Factory: func(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error) {
				return nil, errors.New("factory-boom")
			}},
		},
		Strict: true,
	}
	if _, err := uc.Run(context.Background(), "loc", "v", domain.FetchGet); err == nil {
		t.Fatal("expected factory error")
	}
}

func TestSourceEntry_KindGuess(t *testing.T) {
	if (SourceEntry{}).KindGuess() != "unknown" {
		t.Error("nil source should guess 'unknown'")
	}
	if (SourceEntry{Source: &fakeSource{kind: domain.ProviderAWS}}).KindGuess() != "aws" {
		t.Error("source-backed entry should guess source kind")
	}
}
