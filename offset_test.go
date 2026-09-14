package paginate

import (
	"errors"
	"testing"
	"time"
)

func TestOffsetGeneratedSQL(t *testing.T) {
	tests := []struct {
		name     string
		req      OffsetRequest
		wantSQL  string
		wantVars []any
	}{
		{
			name:     "first page",
			req:      OffsetRequest{Page: 1, Limit: 25, Sort: "-created_at"},
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at` DESC,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{25},
		},
		{
			name:     "page zero is the first page",
			req:      OffsetRequest{Limit: 25, Sort: "-created_at"},
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at` DESC,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{25},
		},
		{
			name:     "third page offsets by two pages",
			req:      OffsetRequest{Page: 3, Limit: 10, Sort: "created_at"},
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at`,`posts`.`id` LIMIT ? OFFSET ?",
			wantVars: []any{10, 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, got := dryRun(t)
			p := testPaginator(t, postAllowlist())

			if _, err := Offset[post](db.Model(&post{}), p, tt.req); err != nil {
				t.Fatalf("Offset(): %v", err)
			}
			if got.sql != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", got.sql, tt.wantSQL)
			}
			if len(got.vars) != len(tt.wantVars) {
				t.Fatalf("vars = %#v, want %#v", got.vars, tt.wantVars)
			}
			for i := range tt.wantVars {
				if got.vars[i] != tt.wantVars[i] {
					t.Errorf("var %d = %#v, want %#v", i, got.vars[i], tt.wantVars[i])
				}
			}
		})
	}
}

func TestOffsetRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name    string
		req     OffsetRequest
		wantErr error
	}{
		{"negative page", OffsetRequest{Page: -1}, ErrInvalidPage},
		{"negative limit", OffsetRequest{Limit: -1}, ErrInvalidLimit},
		{"limit over the maximum", OffsetRequest{Limit: DefaultMaxLimit + 1}, ErrLimitTooLarge},
		{"unknown sort field", OffsetRequest{Sort: "password_hash"}, ErrUnknownSort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := dryRun(t)
			p := testPaginator(t, postAllowlist())
			if _, err := Offset[post](db.Model(&post{}), p, tt.req); !errors.Is(err, tt.wantErr) {
				t.Errorf("Offset() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Nullable columns are refused for keyset paging, where a NULL sort key drops
// rows from every page, but they are fine for offset paging, where NULLs
// simply sort to one end.
func TestOffsetAcceptsNullableSortColumn(t *testing.T) {
	type draft struct {
		ID          int64
		PublishedAt *time.Time
	}

	db, _ := dryRun(t)
	p := testPaginator(t, AllowlistConfig{
		Fields:   map[string][]Column{"published_at": {{Name: "published_at"}}},
		Default:  "-published_at",
		Tiebreak: Column{Name: "id"},
	})

	if _, err := Offset[draft](db.Model(&draft{}), p, OffsetRequest{}); err != nil {
		t.Errorf("Offset() on a nullable sort column: %v", err)
	}
	if _, err := Keyset[draft](db.Model(&draft{}), p, Request{}); !errors.Is(err, ErrUnsortableField) {
		t.Errorf("Keyset() error = %v, want %v", err, ErrUnsortableField)
	}
}
