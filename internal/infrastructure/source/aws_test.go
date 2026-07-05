package source

import "testing"

func TestParseSSMArn(t *testing.T) {
	tests := []struct {
		name          string
		arn           string
		wantRegion    string
		wantParamPath string
		wantErr       bool
	}{
		{name: "standard ARN", arn: "arn:aws:ssm:ap-southeast-7:627443353872:parameter/crd-portal/frontend", wantRegion: "ap-southeast-7", wantParamPath: "/crd-portal/frontend"},
		{name: "single-segment path", arn: "arn:aws:ssm:us-east-1:123456789012:parameter/myapp", wantRegion: "us-east-1", wantParamPath: "/myapp"},
		{name: "deep nested path", arn: "arn:aws:ssm:eu-west-1:999:parameter/a/b/c/d", wantRegion: "eu-west-1", wantParamPath: "/a/b/c/d"},
		{name: "bare parameter name — region empty", arn: "/crd-portal/frontend", wantRegion: "", wantParamPath: "/crd-portal/frontend"},
		{name: "bare deep nested path", arn: "/a/b/c/d", wantRegion: "", wantParamPath: "/a/b/c/d"},
		{name: "bare empty path", arn: "/", wantErr: true},
		{name: "not an ARN", arn: "projects/foo/locations/global", wantErr: true},
		{name: "wrong service", arn: "arn:aws:s3:::my-bucket", wantErr: true},
		{name: "missing parameter prefix", arn: "arn:aws:ssm:us-east-1:123:secret/foo", wantErr: true},
		{name: "empty region", arn: "arn:aws:ssm::123:parameter/foo", wantErr: true},
		{name: "too few fields", arn: "arn:aws:ssm:us-east-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, paramPath, err := parseSSMArn(tt.arn)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseSSMArn(%q): expected error, got nil", tt.arn)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSSMArn(%q): unexpected error: %v", tt.arn, err)
			}
			if region != tt.wantRegion {
				t.Errorf("region = %q, want %q", region, tt.wantRegion)
			}
			if paramPath != tt.wantParamPath {
				t.Errorf("paramPath = %q, want %q", paramPath, tt.wantParamPath)
			}
		})
	}
}

func TestIsSSMLocation(t *testing.T) {
	cases := []struct {
		location string
		want     bool
	}{
		{"arn:aws:ssm:ap-southeast-7:627443353872:parameter/crd-portal/frontend", true},
		{"arn:aws:ssm:us-east-1:123456789012:parameter/my/param", true},
		{"/crd-portal/frontend", true},
		{"/my/param", true},
		{"projects/my-project/locations/global/parameters/my-param", false},
		{"projects/cfw-cloudops-portal-nonprod/locations/global/parameters/cms-structure-api", false},
		{"", false},
		{"arn:aws:secretsmanager:us-east-1:123:secret:foo", false},
	}
	for _, c := range cases {
		got := isSSMLocation(c.location)
		if got != c.want {
			t.Errorf("isSSMLocation(%q) = %v, want %v", c.location, got, c.want)
		}
	}
}
