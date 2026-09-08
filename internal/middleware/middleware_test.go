package middleware

import "testing"

func TestAccountsUserInfoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "empty defaults to gateway",
			base: "",
			want: "https://my.lisaos.dev/api/accounts/me",
		},
		{
			name: "legacy public accounts host rewrites to gateway",
			base: "https://accounts.lisaos.dev",
			want: "https://my.lisaos.dev/api/accounts/me",
		},
		{
			name: "gateway root gets accounts prefix",
			base: "https://my.lisaos.dev",
			want: "https://my.lisaos.dev/api/accounts/me",
		},
		{
			name: "gateway api root gets accounts prefix",
			base: "https://my.lisaos.dev/api",
			want: "https://my.lisaos.dev/api/accounts/me",
		},
		{
			name: "gateway accounts root appends userinfo path",
			base: "https://my.lisaos.dev/api/accounts",
			want: "https://my.lisaos.dev/api/accounts/me",
		},
		{
			name: "internal service root stays service-relative",
			base: "http://srv-captain--accounts",
			want: "http://srv-captain--accounts/api/me",
		},
		{
			name: "custom service base path stays service-relative",
			base: "http://localhost:8000/accounts",
			want: "http://localhost:8000/accounts/api/me",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := accountsUserInfoURL(tt.base)
			if err != nil {
				t.Fatalf("accountsUserInfoURL(%q) returned error: %v", tt.base, err)
			}
			if got != tt.want {
				t.Fatalf("accountsUserInfoURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}
