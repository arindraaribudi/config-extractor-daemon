package application

import (
	"reflect"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

func TestExecChildUseCaseNoArgs(t *testing.T) {
	err := (ExecChildUseCase{Args: nil}).Run(nil)
	if err == nil {
		t.Fatal("expected error for empty args, got nil")
	}
}

func TestMergeEnv(t *testing.T) {
	tests := []struct {
		name  string
		base  []string
		pairs []domain.EnvPair
		want  []string
	}{
		{
			name:  "new names are appended",
			base:  []string{"A=1"},
			pairs: []domain.EnvPair{"B=2"},
			want:  []string{"A=1", "B=2"},
		},
		{
			name:  "injected value replaces the inherited one in place",
			base:  []string{"A=1", "B=2", "C=3"},
			pairs: []domain.EnvPair{"B=injected"},
			want:  []string{"A=1", "B=injected", "C=3"},
		},
		{
			name:  "duplicates in the base collapse to one, last wins",
			base:  []string{"A=1", "A=2"},
			pairs: nil,
			want:  []string{"A=2"},
		},
		{
			name:  "the same name injected twice keeps the last",
			base:  nil,
			pairs: []domain.EnvPair{"A=1", "A=2"},
			want:  []string{"A=2"},
		},
		{
			name:  "a value containing = is kept whole",
			base:  nil,
			pairs: []domain.EnvPair{"DSN=postgres://u:p@h/db?x=y"},
			want:  []string{"DSN=postgres://u:p@h/db?x=y"},
		},
		{
			name:  "an empty value is a value, not a deletion",
			base:  []string{"A=1"},
			pairs: []domain.EnvPair{"A="},
			want:  []string{"A="},
		},
		{
			name:  "an entry without = is passed through untouched",
			base:  []string{"WEIRD", "A=1"},
			pairs: nil,
			want:  []string{"WEIRD", "A=1"},
		},
		{
			name: "nothing in, nothing out",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeEnv(tt.base, tt.pairs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeEnv = %v, want %v", got, tt.want)
			}
		})
	}
}
