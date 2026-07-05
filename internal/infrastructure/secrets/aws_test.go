package secrets

import "testing"

func TestAWSResolverSupports(t *testing.T) {
	r := NewAWSResolver()
	cases := []struct {
		ref  string
		want bool
	}{
		{"secretsmanager.amazonaws.com/us-east-1/my-secret", true},
		{"secretsmanager.amazonaws.com/eu-west-1/prod/db-password", true},
		{"secretsmanager.ap-southeast-7.amazonaws.com/projects/627443353872/secrets/crd_portal-frontend--github_pat", true},
		{"secretsmanager.us-east-1.amazonaws.com/projects/123456789012/secrets/my-secret", true},
		{"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := r.Supports(c.ref); got != c.want {
			t.Errorf("awsResolver.Supports(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

func TestParseAWSSecretRef_RegionalFormat(t *testing.T) {
	cases := []struct {
		ref      string
		region   string
		secretID string
		wantErr  bool
	}{
		{ref: "secretsmanager.ap-southeast-7.amazonaws.com/projects/627443353872/secrets/crd_portal-frontend--github_pat", region: "ap-southeast-7", secretID: "crd_portal-frontend--github_pat"},
		{ref: "secretsmanager.us-east-1.amazonaws.com/projects/123456789012/secrets/my-db-password", region: "us-east-1", secretID: "my-db-password"},
		{ref: "secretsmanager.amazonaws.com/us-east-1/my-secret", region: "us-east-1", secretID: "my-secret"},
		{ref: "secretsmanager.us-east-1.amazonaws.com/bad", wantErr: true},
		{ref: "secretsmanager.us-east-1.amazonaws.com/projects/123/no-secrets-segment", wantErr: true},
	}
	for _, c := range cases {
		region, secretID, err := parseAWSSecretRef(c.ref)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseAWSSecretRef(%q): expected error, got nil", c.ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAWSSecretRef(%q): unexpected error: %v", c.ref, err)
			continue
		}
		if region != c.region {
			t.Errorf("region = %q, want %q", region, c.region)
		}
		if secretID != c.secretID {
			t.Errorf("secretID = %q, want %q", secretID, c.secretID)
		}
	}
}
