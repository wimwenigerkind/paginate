package paginate

import (
	"errors"
	"testing"
)

func validConfig() AllowlistConfig {
	return AllowlistConfig{
		Fields: map[string][]Column{
			"created_at": {{Name: "created_at"}},
			"title":      {{Name: "title"}},
			"author":     {{Name: "last_name"}, {Name: "first_name"}},
		},
		Default:  "-created_at",
		Tiebreak: Column{Name: "id"},
	}
}

func TestNewAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*AllowlistConfig)
		wantErr error
	}{
		{name: "valid", mutate: func(*AllowlistConfig) {}},
		{
			name:    "no fields",
			mutate:  func(c *AllowlistConfig) { c.Fields = nil },
			wantErr: ErrConfig,
		},
		{
			name:    "no tiebreak",
			mutate:  func(c *AllowlistConfig) { c.Tiebreak = Column{} },
			wantErr: ErrConfig,
		},
		{
			name:    "inverted tiebreak",
			mutate:  func(c *AllowlistConfig) { c.Tiebreak.Invert = true },
			wantErr: ErrConfig,
		},
		{
			name:    "no default",
			mutate:  func(c *AllowlistConfig) { c.Default = "" },
			wantErr: ErrConfig,
		},
		{
			name:    "default names unknown field",
			mutate:  func(c *AllowlistConfig) { c.Default = "-nope" },
			wantErr: ErrConfig,
		},
		{
			name:    "empty token",
			mutate:  func(c *AllowlistConfig) { c.Fields[""] = []Column{{Name: "x"}} },
			wantErr: ErrConfig,
		},
		{
			name:    "token with descending prefix",
			mutate:  func(c *AllowlistConfig) { c.Fields["-x"] = []Column{{Name: "x"}} },
			wantErr: ErrConfig,
		},
		{
			name:    "token with comma",
			mutate:  func(c *AllowlistConfig) { c.Fields["a,b"] = []Column{{Name: "x"}} },
			wantErr: ErrConfig,
		},
		{
			name:    "token with no columns",
			mutate:  func(c *AllowlistConfig) { c.Fields["empty"] = []Column{} },
			wantErr: ErrConfig,
		},
		{
			name:    "column with empty name",
			mutate:  func(c *AllowlistConfig) { c.Fields["broken"] = []Column{{Name: ""}} },
			wantErr: ErrConfig,
		},
		{
			name: "duplicate column in one field",
			mutate: func(c *AllowlistConfig) {
				c.Fields["dup"] = []Column{{Name: "a"}, {Name: "a"}}
			},
			wantErr: ErrConfig,
		},
		{
			name: "same column name on different tables is not a duplicate",
			mutate: func(c *AllowlistConfig) {
				c.Fields["joined"] = []Column{{Table: "posts", Name: "id"}, {Table: "users", Name: "id"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			got, err := NewAllowlist(cfg)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewAllowlist() error = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("NewAllowlist() returned a non-nil allowlist alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAllowlist() unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("NewAllowlist() returned nil without an error")
			}
		})
	}
}

func TestAllowlistDoesNotAliasCallerConfig(t *testing.T) {
	cfg := validConfig()
	a, err := NewAllowlist(cfg)
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}

	// Mutating the caller's config after construction must not reach inside.
	cfg.Fields["title"][0].Name = "hacked"
	delete(cfg.Fields, "created_at")

	keys, err := a.resolve("title")
	if err != nil {
		t.Fatalf("resolve(): %v", err)
	}
	if keys[0].Name != "title" {
		t.Errorf("column name = %q, want %q: allowlist aliases the caller's slice", keys[0].Name, "title")
	}
	if _, err := a.resolve("created_at"); err != nil {
		t.Errorf("resolve(created_at) failed after caller deleted the key: %v", err)
	}
}

func TestAllowlistResolve(t *testing.T) {
	type want struct {
		col  string
		desc bool
	}
	tests := []struct {
		name      string
		cfg       func(*AllowlistConfig)
		requested string
		want      []want
		wantErr   error
	}{
		{
			name:      "empty request uses the default",
			requested: "",
			want:      []want{{"created_at", true}, {"id", true}},
		},
		{
			name:      "explicit ascending",
			requested: "created_at",
			want:      []want{{"created_at", false}, {"id", false}},
		},
		{
			name:      "explicit descending",
			requested: "-created_at",
			want:      []want{{"created_at", true}, {"id", true}},
		},
		{
			name:      "multi-column field keeps its order",
			requested: "author",
			want:      []want{{"last_name", false}, {"first_name", false}, {"id", false}},
		},
		{
			name:      "tiebreak follows the requested direction",
			requested: "-author",
			want:      []want{{"last_name", true}, {"first_name", true}, {"id", true}},
		},
		{
			name: "inverted column runs against the requested direction",
			cfg: func(c *AllowlistConfig) {
				c.Fields["mixed"] = []Column{{Name: "created_at"}, {Name: "title", Invert: true}}
			},
			requested: "-mixed",
			want:      []want{{"created_at", true}, {"title", false}, {"id", true}},
		},
		{
			name: "tiebreak is not repeated when the field already ends on it",
			cfg: func(c *AllowlistConfig) {
				c.Fields["id"] = []Column{{Name: "id"}}
			},
			requested: "-id",
			want:      []want{{"id", true}},
		},
		{
			name:      "unknown field",
			requested: "password_hash",
			wantErr:   ErrUnknownSort,
		},
		{
			name:      "unknown field, descending",
			requested: "-password_hash",
			wantErr:   ErrUnknownSort,
		},
		{
			name:      "a bare minus is not a field",
			requested: "-",
			wantErr:   ErrUnknownSort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			a, err := NewAllowlist(cfg)
			if err != nil {
				t.Fatalf("NewAllowlist(): %v", err)
			}

			keys, err := a.resolve(tt.requested)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolve(%q) error = %v, want %v", tt.requested, err, tt.wantErr)
				}
				var perr *Error
				if errors.As(err, &perr) && perr.Param != "sort" {
					t.Errorf("error Param = %q, want %q", perr.Param, "sort")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q): %v", tt.requested, err)
			}
			if len(keys) != len(tt.want) {
				t.Fatalf("resolve(%q) returned %d keys, want %d: %+v", tt.requested, len(keys), len(tt.want), keys)
			}
			for i, w := range tt.want {
				if keys[i].Name != w.col || keys[i].desc != w.desc {
					t.Errorf("key %d = {%s desc=%v}, want {%s desc=%v}", i, keys[i].Name, keys[i].desc, w.col, w.desc)
				}
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	base := []sortKey{
		{Column: Column{Name: "created_at"}, desc: true},
		{Column: Column{Name: "id"}, desc: true},
	}

	if got, want := fingerprint(base), fingerprint(base); got != want {
		t.Errorf("fingerprint is not stable: %q vs %q", got, want)
	}

	tests := []struct {
		name string
		keys []sortKey
	}{
		{
			name: "direction differs",
			keys: []sortKey{
				{Column: Column{Name: "created_at"}, desc: false},
				{Column: Column{Name: "id"}, desc: true},
			},
		},
		{
			name: "column differs",
			keys: []sortKey{
				{Column: Column{Name: "updated_at"}, desc: true},
				{Column: Column{Name: "id"}, desc: true},
			},
		},
		{
			name: "table qualifier differs",
			keys: []sortKey{
				{Column: Column{Table: "posts", Name: "created_at"}, desc: true},
				{Column: Column{Name: "id"}, desc: true},
			},
		},
		{
			name: "order differs",
			keys: []sortKey{
				{Column: Column{Name: "id"}, desc: true},
				{Column: Column{Name: "created_at"}, desc: true},
			},
		},
		{
			name: "extra key",
			keys: append(append([]sortKey{}, base...), sortKey{Column: Column{Name: "title"}}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if fingerprint(tt.keys) == fingerprint(base) {
				t.Errorf("fingerprint() collides with the base sort order")
			}
		})
	}
}
