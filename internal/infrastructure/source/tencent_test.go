package source

import (
	"errors"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)

func TestParseTencentLocation(t *testing.T) {
	tests := []struct {
		name       string
		location   string
		version    string
		wantBucket string
		wantRegion string
		wantKey    string
		wantErrSub string
	}{
		{
			name:       "full URL with versions segment",
			location:   "https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig",
			version:    "ENV-1",
			wantBucket: "cds-oms-sit-1409486316",
			wantRegion: "ap-bangkok",
			wantKey:    "projects/my-proj/parameters/appconfig/versions/ENV-1",
		},
		{
			name:       "URL with single-segment path",
			location:   "https://bucket-123.cos.ap-shanghai.myqcloud.com/cfg",
			version:    "v1",
			wantBucket: "bucket-123",
			wantRegion: "ap-shanghai",
			wantKey:    "cfg/versions/v1",
		},
		{
			name:       "dev-1 regression",
			location:   "https://cds-stk-dev-1409486316.cos.ap-bangkok.myqcloud.com/projects/cds-stk/parameters",
			version:    "dev-1",
			wantBucket: "cds-stk-dev-1409486316",
			wantRegion: "ap-bangkok",
			wantKey:    "projects/cds-stk/parameters/versions/dev-1",
		},
		{
			name:       "non-https scheme rejected",
			location:   "http://bucket.cos.ap-shanghai.myqcloud.com/cfg",
			version:    "v1",
			wantErrSub: "https",
		},
		{
			name:       "host without .cos. rejected",
			location:   "https://bucket.example.com/cfg",
			version:    "v1",
			wantErrSub: ".cos.",
		},
		{
			name:       "host without .myqcloud.com rejected",
			location:   "https://bucket.cos.example.com/cfg",
			version:    "v1",
			wantErrSub: "myqcloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, region, key, err := parseTencentLocation(tt.location, tt.version)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if !contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error %q missing %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bucket != tt.wantBucket || region != tt.wantRegion || key != tt.wantKey {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)",
					bucket, region, key, tt.wantBucket, tt.wantRegion, tt.wantKey)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestTencentSource_Fetch_UnsupportedMode(t *testing.T) {
	s := tencentSource{}
	_, err := s.Fetch(t.Context(), domain.Reference{
		Location: "https://b.cos.ap-bangkok.myqcloud.com/cfg",
		Version:  "v1",
	}, domain.FetchRender)
	if err == nil {
		t.Fatal("expected error for render mode")
	}
	if !errors.Is(err, domain.ErrUnsupportedMode) {
		t.Fatalf("expected ErrUnsupportedMode, got %v", err)
	}
}

func TestTencentSource_Fetch_LocationError(t *testing.T) {
	s := tencentSource{}
	_, err := s.Fetch(t.Context(), domain.Reference{
		Location: "http://b.cos.ap-bangkok.myqcloud.com/cfg",
		Version:  "v1",
	}, domain.FetchGet)
	if err == nil {
		t.Fatal("expected error for http scheme")
	}
}

func TestNewTencentSource_STSTokenWiring(t *testing.T) {
	prevNew := cosNewClient
	defer func() { cosNewClient = prevNew }()

	var captured *tencentcred.Credentials
	cosNewClient = func(bucket, region string, creds *tencentcred.Credentials) (cosObjectGetResult, error) {
		captured = creds
		return nil, nil
	}

	t.Setenv("TENCENTCLOUD_SECRETID", "")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "")
	t.Setenv("TENCENTCLOUD_TOKEN", "")
	tencentcred.ResetForTest()

	creds := &tencentcred.Credentials{SecretID: "s", SecretKey: "k", Token: "the-token"}
	if _, err := cosNewClient("bucket", "ap-bangkok", creds); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Token != "the-token" {
		t.Fatalf("expected Token=%q passed through, got captured=%+v", "the-token", captured)
	}
}
