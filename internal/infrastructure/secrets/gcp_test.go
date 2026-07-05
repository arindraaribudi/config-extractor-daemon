package secrets

import "testing"

func TestGCPResolverSupports(t *testing.T) {
	r := NewGCPResolver()
	cases := []struct {
		ref  string
		want bool
	}{
		{"secretmanager.googleapis.com/projects/123/secrets/my-secret/versions/latest", true},
		{"secretmanager.googleapis.com/", true},
		{"secretsmanager.amazonaws.com/us-east-1/my-secret", false},
		{"arn:aws:secretsmanager:us-east-1:123:secret:foo", false},
		{"", false},
	}
	for _, c := range cases {
		if got := r.Supports(c.ref); got != c.want {
			t.Errorf("gcpResolver.Supports(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}
