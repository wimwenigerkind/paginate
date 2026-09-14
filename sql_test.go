package paginate

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils/tests"
)

type post struct {
	ID        int64
	CreatedAt time.Time
	Title     string
}

type capture struct {
	sql  string
	vars []any
}

// dryRun builds a GORM handle that renders SQL without a database, so the
// exact statement paginate produces can be asserted directly.
func dryRun(t *testing.T) (*gorm.DB, *capture) {
	t.Helper()

	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{
		DryRun: true,
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("opening dry-run database: %v", err)
	}

	var got capture
	err = db.Callback().Query().After("gorm:query").Register("paginate:capture", func(d *gorm.DB) {
		got.sql = d.Statement.SQL.String()
		got.vars = append([]any(nil), d.Statement.Vars...)
	})
	if err != nil {
		t.Fatalf("registering capture callback: %v", err)
	}
	return db, &got
}

func testPaginator(t *testing.T, cfg AllowlistConfig) *Paginator {
	t.Helper()
	a, err := NewAllowlist(cfg)
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}
	p, err := New(Config{Allowlist: a})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return p
}

func postAllowlist() AllowlistConfig {
	return AllowlistConfig{
		Fields: map[string][]Column{
			"created_at": {{Name: "created_at"}},
			"title":      {{Name: "title"}},
		},
		Default:  "-created_at",
		Tiebreak: Column{Name: "id"},
	}
}

func mixedAllowlist() AllowlistConfig {
	cfg := postAllowlist()
	// "newest first, then alphabetically": the title runs against the
	// requested direction, which rules out row-value comparison.
	cfg.Fields["recent_title"] = []Column{{Name: "created_at"}, {Name: "title", Invert: true}}
	return cfg
}

func TestGeneratedSQL(t *testing.T) {
	at := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		cfg      AllowlistConfig
		query    func(*gorm.DB) *gorm.DB
		sort     string
		cursorAt func(*testing.T, *Paginator) string
		limit    int
		wantSQL  string
		wantVars []any
	}{
		{
			name:     "first page, descending",
			cfg:      postAllowlist(),
			sort:     "-created_at",
			limit:    25,
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at` DESC,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{26},
		},
		{
			name:     "first page, ascending",
			cfg:      postAllowlist(),
			sort:     "created_at",
			limit:    10,
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at`,`posts`.`id` LIMIT ?",
			wantVars: []any{11},
		},
		{
			name:  "forward cursor uses row-value comparison",
			cfg:   postAllowlist(),
			sort:  "-created_at",
			limit: 25,
			cursorAt: func(t *testing.T, p *Paginator) string {
				return mustCursor(t, p, "-created_at", forward, at, int64(7))
			},
			wantSQL: "SELECT * FROM `posts` WHERE (`posts`.`created_at`, `posts`.`id`) < (?, ?) " +
				"ORDER BY `posts`.`created_at` DESC,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{at, int64(7), 26},
		},
		{
			name:  "ascending forward cursor seeks upward",
			cfg:   postAllowlist(),
			sort:  "created_at",
			limit: 25,
			cursorAt: func(t *testing.T, p *Paginator) string {
				return mustCursor(t, p, "created_at", forward, at, int64(7))
			},
			wantSQL: "SELECT * FROM `posts` WHERE (`posts`.`created_at`, `posts`.`id`) > (?, ?) " +
				"ORDER BY `posts`.`created_at`,`posts`.`id` LIMIT ?",
			wantVars: []any{at, int64(7), 26},
		},
		{
			name:  "backward cursor flips both the comparison and the order",
			cfg:   postAllowlist(),
			sort:  "-created_at",
			limit: 25,
			cursorAt: func(t *testing.T, p *Paginator) string {
				return mustCursor(t, p, "-created_at", backward, at, int64(7))
			},
			wantSQL: "SELECT * FROM `posts` WHERE (`posts`.`created_at`, `posts`.`id`) > (?, ?) " +
				"ORDER BY `posts`.`created_at`,`posts`.`id` LIMIT ?",
			wantVars: []any{at, int64(7), 26},
		},
		{
			name:  "mixed directions fall back to the OR-chain",
			cfg:   mixedAllowlist(),
			sort:  "-recent_title",
			limit: 25,
			cursorAt: func(t *testing.T, p *Paginator) string {
				keys, err := p.allow.resolve("-recent_title")
				if err != nil {
					t.Fatalf("resolve(): %v", err)
				}
				c, err := encodeCursor(keys, forward, map[string]any{
					"created_at": at, "title": "hello", "id": int64(7),
				})
				if err != nil {
					t.Fatalf("encodeCursor(): %v", err)
				}
				return c
			},
			wantSQL: "SELECT * FROM `posts` WHERE ((`posts`.`created_at` < ?) OR " +
				"(`posts`.`created_at` = ? AND `posts`.`title` > ?) OR " +
				"(`posts`.`created_at` = ? AND `posts`.`title` = ? AND `posts`.`id` < ?)) " +
				"ORDER BY `posts`.`created_at` DESC,`posts`.`title`,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{at, at, "hello", at, "hello", int64(7), 26},
		},
		{
			name:  "the caller's conditions are ANDed with the OR-chain, not ORed",
			cfg:   mixedAllowlist(),
			query: func(db *gorm.DB) *gorm.DB { return db.Where("author_id = ?", 42) },
			sort:  "-recent_title",
			limit: 25,
			cursorAt: func(t *testing.T, p *Paginator) string {
				keys, err := p.allow.resolve("-recent_title")
				if err != nil {
					t.Fatalf("resolve(): %v", err)
				}
				c, err := encodeCursor(keys, forward, map[string]any{
					"created_at": at, "title": "hello", "id": int64(7),
				})
				if err != nil {
					t.Fatalf("encodeCursor(): %v", err)
				}
				return c
			},
			// GORM adds parentheses of its own around a raw condition holding
			// an OR. Ours stay anyway: correctness must not depend on that.
			wantSQL: "SELECT * FROM `posts` WHERE author_id = ? AND (((`posts`.`created_at` < ?) OR " +
				"(`posts`.`created_at` = ? AND `posts`.`title` > ?) OR " +
				"(`posts`.`created_at` = ? AND `posts`.`title` = ? AND `posts`.`id` < ?))) " +
				"ORDER BY `posts`.`created_at` DESC,`posts`.`title`,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{42, at, at, "hello", at, "hello", int64(7), 26},
		},
		{
			name: "an explicit table qualifier survives a join",
			cfg: AllowlistConfig{
				Fields:   map[string][]Column{"created_at": {{Table: "p", Name: "created_at"}}},
				Default:  "-created_at",
				Tiebreak: Column{Table: "p", Name: "id"},
			},
			query: func(db *gorm.DB) *gorm.DB {
				return db.Table("posts p").Joins("JOIN users u ON u.id = p.author_id")
			},
			sort:  "-created_at",
			limit: 25,
			wantSQL: "SELECT `p`.`id`,`p`.`created_at`,`p`.`title` FROM posts p " +
				"JOIN users u ON u.id = p.author_id ORDER BY `p`.`created_at` DESC,`p`.`id` DESC LIMIT ?",
			wantVars: []any{26},
		},
		{
			name: "an unqualified column follows the query to another table",
			cfg:  postAllowlist(),
			query: func(db *gorm.DB) *gorm.DB {
				return db.Table("posts_archive")
			},
			sort:  "-created_at",
			limit: 25,
			wantSQL: "SELECT * FROM `posts_archive` " +
				"ORDER BY `posts_archive`.`created_at` DESC,`posts_archive`.`id` DESC LIMIT ?",
			wantVars: []any{26},
		},
		{
			name:     "limit falls back to the paginator default",
			cfg:      postAllowlist(),
			sort:     "-created_at",
			limit:    0,
			wantSQL:  "SELECT * FROM `posts` ORDER BY `posts`.`created_at` DESC,`posts`.`id` DESC LIMIT ?",
			wantVars: []any{DefaultLimit + 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, got := dryRun(t)
			p := testPaginator(t, tt.cfg)

			q := db.Model(&post{})
			if tt.query != nil {
				q = tt.query(db)
			}

			req := Request{Limit: tt.limit, Sort: tt.sort}
			if tt.cursorAt != nil {
				req.Cursor = tt.cursorAt(t, p)
			}

			if _, err := Keyset[post](q, p, req); err != nil {
				t.Fatalf("Keyset(): %v", err)
			}
			if got.sql != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", got.sql, tt.wantSQL)
			}
			if !reflect.DeepEqual(got.vars, tt.wantVars) {
				t.Errorf("vars mismatch\n got: %#v\nwant: %#v", got.vars, tt.wantVars)
			}
		})
	}
}

func TestKeysetRejectsConflictingClauses(t *testing.T) {
	tests := []struct {
		name  string
		query func(*gorm.DB) *gorm.DB
	}{
		{"caller ordered the query", func(db *gorm.DB) *gorm.DB { return db.Model(&post{}).Order("title") }},
		{"caller limited the query", func(db *gorm.DB) *gorm.DB { return db.Model(&post{}).Limit(5) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := dryRun(t)
			p := testPaginator(t, postAllowlist())
			_, err := Keyset[post](tt.query(db), p, Request{})
			if !errors.Is(err, ErrConfig) {
				t.Errorf("Keyset() error = %v, want %v", err, ErrConfig)
			}
		})
	}
}

func TestKeysetLeavesCallerHandleUntouched(t *testing.T) {
	db, got := dryRun(t)
	p := testPaginator(t, postAllowlist())

	shared := db.Model(&post{}).Where("author_id = ?", 42)

	if _, err := Keyset[post](shared, p, Request{Limit: 5}); err != nil {
		t.Fatalf("Keyset(): %v", err)
	}
	first := got.sql

	// The caller reuses their handle. If paginate had written its ORDER BY and
	// LIMIT onto it, this second call would inherit them and fail.
	if _, err := Keyset[post](shared, p, Request{Limit: 5}); err != nil {
		t.Fatalf("second Keyset() on the same handle: %v", err)
	}
	if got.sql != first {
		t.Errorf("reusing the caller's handle changed the query\n got: %s\nwant: %s", got.sql, first)
	}
}

func TestKeysetRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr error
	}{
		{"negative limit", Request{Limit: -1}, ErrInvalidLimit},
		{"limit over the maximum", Request{Limit: DefaultMaxLimit + 1}, ErrLimitTooLarge},
		{"unknown sort field", Request{Sort: "password_hash"}, ErrUnknownSort},
		{"malformed cursor", Request{Cursor: "!!!"}, ErrInvalidCursor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := dryRun(t)
			p := testPaginator(t, postAllowlist())
			_, err := Keyset[post](db.Model(&post{}), p, tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Keyset() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestKeysetRejectsNullableSortColumn(t *testing.T) {
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

	_, err := Keyset[draft](db.Model(&draft{}), p, Request{})
	if !errors.Is(err, ErrUnsortableField) {
		t.Errorf("Keyset() error = %v, want %v", err, ErrUnsortableField)
	}
}

func mustCursor(t *testing.T, p *Paginator, sort string, dir direction, createdAt time.Time, id int64) string {
	t.Helper()
	keys, err := p.allow.resolve(sort)
	if err != nil {
		t.Fatalf("resolve(): %v", err)
	}
	c, err := encodeCursor(keys, dir, map[string]any{"created_at": createdAt, "id": id})
	if err != nil {
		t.Fatalf("encodeCursor(): %v", err)
	}
	return c
}
