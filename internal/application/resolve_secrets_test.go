package application

import (
	"context"
	"errors"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

type fakeResolver struct {
	supports func(string) bool
	resolve  func(context.Context, string) (string, error)
}

func (f fakeResolver) Supports(ref string) bool { return f.supports(ref) }
func (f fakeResolver) Resolve(ctx context.Context, ref string) (string, error) {
	return f.resolve(ctx, ref)
}

func TestResolveSecretsUseCase_StrictError(t *testing.T) {
	uc := ResolveSecretsUseCase{
		Resolvers: []domain.SecretResolver{fakeResolver{
			supports: func(string) bool { return true },
			resolve:  func(context.Context, string) (string, error) { return "", errors.New("nope") },
		}},
		Strict: true,
	}
	_, err := uc.Run(context.Background(), []domain.EnvPair{"A=__SECRET_REF__(s)"})
	if err == nil {
		t.Fatal("expected strict error")
	}
}

func TestResolveSecretsUseCase_NonStrictError(t *testing.T) {
	uc := ResolveSecretsUseCase{
		Resolvers: []domain.SecretResolver{fakeResolver{
			supports: func(string) bool { return true },
			resolve:  func(context.Context, string) (string, error) { return "", errors.New("nope") },
		}},
		Strict: false,
	}
	res, err := uc.Run(context.Background(), []domain.EnvPair{"A=__SECRET_REF__(s)"})
	if err != nil {
		t.Fatalf("non-strict should swallow: %v", err)
	}
	if !res.WarningsLogged {
		t.Error("WarningsLogged should be true")
	}
}

func TestResolveSecretsUseCase_NoPlaceholders(t *testing.T) {
	uc := ResolveSecretsUseCase{Resolvers: nil, Strict: true}
	res, err := uc.Run(context.Background(), []domain.EnvPair{"A=plain"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Placeholders != 0 || res.VarsUpdated != 0 {
		t.Errorf("counts = (%d,%d), want 0,0", res.Placeholders, res.VarsUpdated)
	}
}

func TestResolveSecretsUseCase_Success(t *testing.T) {
	uc := ResolveSecretsUseCase{
		Resolvers: []domain.SecretResolver{fakeResolver{
			supports: func(string) bool { return true },
			resolve:  func(context.Context, string) (string, error) { return "secret-value", nil },
		}},
		Strict: true,
	}
	res, err := uc.Run(context.Background(), []domain.EnvPair{"A=__SECRET_REF__(s)"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Placeholders != 1 || res.VarsUpdated != 1 {
		t.Errorf("counts = (%d,%d), want 1,1", res.Placeholders, res.VarsUpdated)
	}
}
