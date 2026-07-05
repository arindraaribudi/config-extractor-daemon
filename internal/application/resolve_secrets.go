package application

import (
	"context"
	"fmt"
	"log"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// ResolveSecretsUseCase replaces __SECRET_REF__() placeholders in pairs.
// In strict mode any unresolved ref is fatal; otherwise we warn and leave
// the placeholder literal in place (downstream env writer then escapes it).
type ResolveSecretsUseCase struct {
	Resolvers []domain.SecretResolver
	Strict    bool
}

type ResolveSecretsResult struct {
	Pairs          []domain.EnvPair
	Placeholders   int
	VarsUpdated    int
	WarningsLogged bool
}

func (uc ResolveSecretsUseCase) Run(ctx context.Context, pairs []domain.EnvPair) (ResolveSecretsResult, error) {
	updated, total, varsUpdated, err := domain.ResolveSecretRefs(ctx, pairs, uc.Resolvers)
	if err != nil {
		if uc.Strict {
			return ResolveSecretsResult{}, fmt.Errorf("%w: %v", domain.ErrStrictSecretRef, err)
		}
		log.Printf("WARNING: secret ref resolution: %v", err)
		return ResolveSecretsResult{Pairs: pairs, WarningsLogged: true}, nil
	}
	if total > 0 {
		log.Printf("secret refs: %d placeholder(s) resolved, %d var(s) updated", total, varsUpdated)
	}
	return ResolveSecretsResult{Pairs: updated, Placeholders: total, VarsUpdated: varsUpdated}, nil
}
