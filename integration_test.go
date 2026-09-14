//go:build integration

package paginate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wimwenigerkind/paginate"
)

type Post struct {
	ID        int64     `gorm:"primaryKey"`
	CreatedAt time.Time `gorm:"not null"`
	Title     string    `gorm:"not null"`
	AuthorID  int64     `gorm:"not null"`
}

var sharedDSN string

// TestMain starts one PostgreSQL container for the whole package and tears it
// down at the end. Each test gets its own table, so tests stay independent
// without paying for a container each.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("paginate"),
		tcpostgres.WithUsername("paginate"),
		tcpostgres.WithPassword("paginate"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting PostgreSQL: %v\n", err)
		os.Exit(1)
	}

	code := func() int {
		defer func() {
			if err := container.Terminate(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "terminating PostgreSQL: %v\n", err)
			}
		}()

		sharedDSN, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading connection string: %v\n", err)
			return 1
		}
		return m.Run()
	}()

	os.Exit(code)
}

// tableNames must fit PostgreSQL's 63-character identifier limit, and test
// names are long, so they are truncated and made unique by a counter.
var tableSeq atomic.Int64

func tableNameFor(t *testing.T) string {
	t.Helper()
	cleaned := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(t.Name()))
	if len(cleaned) > 40 {
		cleaned = cleaned[:40]
	}
	return fmt.Sprintf("p_%s_%d", cleaned, tableSeq.Add(1))
}

// newTable gives a test its own table, migrated and indexed for keyset paging.
func newTable(t *testing.T) (*gorm.DB, string) {
	t.Helper()

	if sharedDSN == "" {
		t.Fatal("no PostgreSQL connection; TestMain did not start a container")
	}
	db, err := gorm.Open(postgres.Open(sharedDSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}

	table := tableNameFor(t)
	scoped := db.Table(table)
	if err := scoped.AutoMigrate(&Post{}); err != nil {
		t.Fatalf("migrating %s: %v", table, err)
	}
	// The index the keyset predicate is meant to ride.
	if err := db.Exec(fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS %s_keyset ON %s (created_at DESC, id DESC)`, table, table)).Error; err != nil {
		t.Fatalf("creating index: %v", err)
	}

	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })
	return db, table
}

func query(db *gorm.DB, table string) *gorm.DB { return db.Table(table) }

// seed inserts n posts. createdAt decides how many distinct timestamps there
// are, which is how ties are produced.
func seed(t *testing.T, db *gorm.DB, table string, n int, createdAt func(i int) time.Time) []Post {
	t.Helper()
	if n == 0 {
		return nil
	}
	posts := make([]Post, n)
	for i := range posts {
		posts[i] = Post{
			ID:        int64(i + 1),
			CreatedAt: createdAt(i),
			Title:     fmt.Sprintf("post %04d", i+1),
			AuthorID:  int64(i%3 + 1),
		}
	}
	if err := db.Table(table).CreateInBatches(posts, 500).Error; err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return posts
}

func distinctTimes(base time.Time) func(int) time.Time {
	return func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) }
}

func sharedTime(base time.Time) func(int) time.Time {
	return func(int) time.Time { return base }
}

func newPaginator(t *testing.T) *paginate.Paginator {
	t.Helper()
	a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields: map[string][]paginate.Column{
			"created_at":   {{Name: "created_at"}},
			"title":        {{Name: "title"}},
			"recent_title": {{Name: "created_at"}, {Name: "title", Invert: true}},
		},
		Default:  "-created_at",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}
	p, err := paginate.New(paginate.Config{Allowlist: a, MaxLimit: 1000})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return p
}

// walk pages forward to the end and returns the IDs in the order they came
// back, plus the number of requests it took. It fails the test rather than
// looping forever if paging does not terminate.
func walk(t *testing.T, db *gorm.DB, table string, p *paginate.Paginator, req paginate.Request) ([]int64, int) {
	t.Helper()

	var ids []int64
	pages := 0
	for {
		pages++
		if pages > 10_000 {
			t.Fatal("paging did not terminate")
		}
		page, err := paginate.Keyset[Post](query(db, table), p, req)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, post := range page.Data {
			ids = append(ids, post.ID)
		}
		if !page.Meta.HasMore {
			if page.Meta.NextCursor != "" {
				t.Errorf("page %d reports no more rows but still returned a next cursor", pages)
			}
			return ids, pages
		}
		if page.Meta.NextCursor == "" {
			t.Fatalf("page %d reports more rows but returned no cursor", pages)
		}
		req.Cursor = page.Meta.NextCursor
	}
}

func descendingIDs(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(n - i)
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func duplicates(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	var dupes []int64
	for _, id := range ids {
		if seen[id] {
			dupes = append(dupes, id)
		}
		seen[id] = true
	}
	return dupes
}

func TestForwardWalkCoversEveryRowExactlyOnce(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		rows  int
		limit int
	}{
		{"fewer rows than the limit", 3, 10},
		{"exactly the limit", 10, 10},
		{"one row past the limit", 11, 10},
		{"several whole pages", 50, 10},
		{"a page size of one", 7, 1},
		{"an awkward remainder", 47, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, table := newTable(t)
			seed(t, db, table, tt.rows, distinctTimes(base))
			p := newPaginator(t)

			ids, pages := walk(t, db, table, p, paginate.Request{Limit: tt.limit})

			if want := descendingIDs(tt.rows); !equalIDs(ids, want) {
				t.Errorf("walked %v, want %v", ids, want)
			}
			if dupes := duplicates(ids); len(dupes) > 0 {
				t.Errorf("rows returned more than once: %v", dupes)
			}
			wantPages := (tt.rows + tt.limit - 1) / tt.limit
			if wantPages == 0 {
				wantPages = 1
			}
			if pages != wantPages {
				t.Errorf("took %d pages, want %d", pages, wantPages)
			}
		})
	}
}

func TestBackwardWalkMirrorsForward(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 47, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	// Page forward to the last page, keeping each page's first cursor.
	var prevCursors []string
	var forwardIDs []int64
	req := paginate.Request{Limit: 9}
	for {
		page, err := paginate.Keyset[Post](query(db, table), p, req)
		if err != nil {
			t.Fatalf("paging forward: %v", err)
		}
		for _, post := range page.Data {
			forwardIDs = append(forwardIDs, post.ID)
		}
		prevCursors = append(prevCursors, page.Meta.PrevCursor)
		if !page.Meta.HasMore {
			break
		}
		req.Cursor = page.Meta.NextCursor
	}

	if prevCursors[0] != "" {
		t.Error("the first page returned a previous cursor")
	}
	for i, c := range prevCursors[1:] {
		if c == "" {
			t.Errorf("page %d returned no previous cursor", i+2)
		}
	}

	// Now walk back from the last page and check we retrace the same rows.
	var backwardIDs []int64
	cursor := prevCursors[len(prevCursors)-1]
	for cursor != "" {
		page, err := paginate.Keyset[Post](query(db, table), p, paginate.Request{Limit: 9, Cursor: cursor})
		if err != nil {
			t.Fatalf("paging backward: %v", err)
		}
		if !page.Meta.HasMore {
			t.Error("a page reached by paging backward should report that a page follows it")
		}
		ids := make([]int64, len(page.Data))
		for i, post := range page.Data {
			ids[i] = post.ID
		}
		backwardIDs = append(ids, backwardIDs...)
		cursor = page.Meta.PrevCursor
	}

	// The backward walk covers everything except the final page.
	want := forwardIDs[:len(forwardIDs)-len(forwardIDs)%9]
	if len(forwardIDs)%9 == 0 {
		want = forwardIDs[:len(forwardIDs)-9]
	}
	if !equalIDs(backwardIDs, want) {
		t.Errorf("backward walk returned\n %v\nwant\n %v", backwardIDs, want)
	}
	if dupes := duplicates(backwardIDs); len(dupes) > 0 {
		t.Errorf("rows returned more than once while paging backward: %v", dupes)
	}
}

// Every row shares one created_at value, so ordering rests entirely on the
// tiebreak column. Without it, pages would skip and repeat rows.
func TestTiesOnTheSortKey(t *testing.T) {
	db, table := newTable(t)
	const rows = 1000
	seed(t, db, table, rows, sharedTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	ids, _ := walk(t, db, table, p, paginate.Request{Limit: 25})

	if len(ids) != rows {
		t.Errorf("walked %d rows, want %d", len(ids), rows)
	}
	if dupes := duplicates(ids); len(dupes) > 0 {
		t.Errorf("rows returned more than once despite the tiebreak: %v", dupes)
	}
	if want := descendingIDs(rows); !equalIDs(ids, want) {
		t.Error("rows sharing a sort value did not come back in tiebreak order")
	}
}

func TestEmptyResultSet(t *testing.T) {
	db, table := newTable(t)
	p := newPaginator(t)

	page, err := paginate.Keyset[Post](query(db, table), p, paginate.Request{Limit: 25})
	if err != nil {
		t.Fatalf("Keyset(): %v", err)
	}
	if page.Data == nil {
		t.Error("Data is nil; it must serialise as [] rather than null")
	}
	if len(page.Data) != 0 {
		t.Errorf("Data has %d rows, want 0", len(page.Data))
	}
	if page.Meta.HasMore {
		t.Error("HasMore is true on an empty result")
	}
	if page.Meta.NextCursor != "" || page.Meta.PrevCursor != "" {
		t.Errorf("an empty page returned cursors: next=%q prev=%q", page.Meta.NextCursor, page.Meta.PrevCursor)
	}
	if page.Meta.Limit != 25 {
		t.Errorf("Limit = %d, want 25", page.Meta.Limit)
	}
}

// A filtered query that matches nothing must behave like an empty table.
func TestEmptyResultSetAfterFilter(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 20, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	page, err := paginate.Keyset[Post](
		query(db, table).Where("author_id = ?", 999), p, paginate.Request{Limit: 25})
	if err != nil {
		t.Fatalf("Keyset(): %v", err)
	}
	if len(page.Data) != 0 || page.Meta.HasMore {
		t.Errorf("filtered-to-nothing page = %+v, want empty", page.Meta)
	}
}

// The guarantee keyset paging makes under concurrent writes: every row that
// existed when paging started is returned exactly once. Rows inserted midway
// may or may not appear, depending on where they land in the sort order. That
// is inherent, and it is the part offset pagination gets wrong by shifting
// every later row instead.
func TestConcurrentInsertsDuringPaging(t *testing.T) {
	db, table := newTable(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	const existing = 200
	seed(t, db, table, existing, distinctTimes(base))
	p := newPaginator(t)

	preexisting := make(map[int64]bool, existing)
	for i := 1; i <= existing; i++ {
		preexisting[int64(i)] = true
	}

	stop := make(chan struct{})
	var inserted atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		next := int64(existing + 1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Insert both before and after the region being paged, so the
			// writes are not conveniently out of the way.
			offset := time.Duration(next%int64(existing)) * time.Second
			err := db.Table(table).Create(&Post{
				ID:        next,
				CreatedAt: base.Add(offset),
				Title:     fmt.Sprintf("inserted %d", next),
				AuthorID:  1,
			}).Error
			if err != nil {
				return
			}
			inserted.Add(1)
			next++
			time.Sleep(time.Millisecond)
		}
	}()

	var seen []int64
	req := paginate.Request{Limit: 10}
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("paging did not terminate")
		}
		page, err := paginate.Keyset[Post](query(db, table), p, req)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, post := range page.Data {
			seen = append(seen, post.ID)
		}
		if !page.Meta.HasMore {
			break
		}
		req.Cursor = page.Meta.NextCursor
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	// Without this the test could pass by never overlapping the writer at all.
	if n := inserted.Load(); n < 10 {
		t.Fatalf("only %d rows were inserted while paging; the test did not exercise concurrency", n)
	}

	if dupes := duplicates(seen); len(dupes) > 0 {
		t.Errorf("rows returned more than once while writes were happening: %v", dupes)
	}

	found := make(map[int64]bool, len(seen))
	for _, id := range seen {
		found[id] = true
	}
	var missing []int64
	for id := range preexisting {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d rows that existed before paging started were never returned: %v", len(missing), missing)
	}
}

func TestConcurrentDeletesDuringPaging(t *testing.T) {
	db, table := newTable(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	const rows = 200
	seed(t, db, table, rows, distinctTimes(base))
	p := newPaginator(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Delete from the low-numbered end, which is where a descending walk
		// is heading.
		for id := int64(1); id <= rows/2; id++ {
			select {
			case <-stop:
				return
			default:
			}
			db.Table(table).Where("id = ?", id).Delete(&Post{})
			time.Sleep(time.Millisecond)
		}
	}()

	var seen []int64
	req := paginate.Request{Limit: 10}
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("paging did not terminate")
		}
		page, err := paginate.Keyset[Post](query(db, table), p, req)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, post := range page.Data {
			seen = append(seen, post.ID)
		}
		if !page.Meta.HasMore {
			break
		}
		req.Cursor = page.Meta.NextCursor
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if dupes := duplicates(seen); len(dupes) > 0 {
		t.Errorf("rows returned more than once while rows were being deleted: %v", dupes)
	}
	// Rows above the deleted range must all still have been seen.
	for id := int64(rows/2 + 1); id <= rows; id++ {
		found := false
		for _, got := range seen {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("row %d was never returned although it was never deleted", id)
		}
	}
}

// A sort whose columns run in different directions cannot use row-value
// comparison. It must still page correctly.
func TestMixedDirectionSortPagesCorrectly(t *testing.T) {
	db, table := newTable(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Ten distinct timestamps, twenty rows each, so the second key decides.
	seed(t, db, table, 200, func(i int) time.Time {
		return base.Add(time.Duration(i/20) * time.Hour)
	})
	p := newPaginator(t)

	paged, _ := walk(t, db, table, p, paginate.Request{Limit: 7, Sort: "-recent_title"})

	var want []int64
	err := db.Table(table).
		Order("created_at DESC, title ASC, id DESC").
		Pluck("id", &want).Error
	if err != nil {
		t.Fatalf("reading the expected order: %v", err)
	}

	if !equalIDs(paged, want) {
		t.Errorf("mixed-direction paging returned a different order than the equivalent ORDER BY\n got %v\nwant %v",
			paged[:min(len(paged), 20)], want[:min(len(want), 20)])
	}
	if dupes := duplicates(paged); len(dupes) > 0 {
		t.Errorf("rows returned more than once: %v", dupes)
	}
}

// The whole point of the row-value predicate is that PostgreSQL can answer it
// from the index. If a refactor turns it back into a post-scan filter, paging
// stays correct and quietly gets slower with every page, so assert the plan,
// and assert it against the statement paginate actually emits rather than a
// hand-written lookalike.
func TestSameDirectionSeekUsesTheIndex(t *testing.T) {
	db, table := newTable(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	seed(t, db, table, 5000, distinctTimes(base))
	if err := db.Exec("ANALYZE " + table).Error; err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	p := newPaginator(t)

	// Take a cursor from deep in the table, where a filtering plan would show
	// its cost.
	var deep Post
	if err := db.Table(table).Order("created_at DESC, id DESC").Offset(4000).First(&deep).Error; err != nil {
		t.Fatalf("reading a deep row: %v", err)
	}
	captured := captureStatement(t, db, table, p, deep)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrapping the connection: %v", err)
	}
	rows, err := sqlDB.Query("EXPLAIN "+captured.sql, captured.vars...)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", captured.sql, err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scanning the plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the plan: %v", err)
	}

	joined := strings.Join(plan, "\n")
	t.Logf("statement: %s", captured.sql)
	t.Logf("plan:\n%s", joined)

	if !strings.Contains(joined, "Index Cond") {
		t.Errorf("the seek predicate is not being used as an index condition:\n%s", joined)
	}
	if strings.Contains(joined, "Seq Scan") {
		t.Errorf("the seek predicate produced a sequential scan:\n%s", joined)
	}
}

type statement struct {
	sql  string
	vars []any
}

// captureStatement runs a real paginated query against a cursor pointing at
// the given row, and returns the SQL and bind variables that went to the
// database.
func captureStatement(t *testing.T, db *gorm.DB, table string, p *paginate.Paginator, at Post) statement {
	t.Helper()

	// Reach the cursor for `at` by paging to it, so the cursor is one the
	// library itself issued.
	var cursor string
	req := paginate.Request{Limit: 500}
	for {
		page, err := paginate.Keyset[Post](query(db, table), p, req)
		if err != nil {
			t.Fatalf("paging to the cursor: %v", err)
		}
		found := false
		for _, post := range page.Data {
			if post.ID == at.ID {
				found = true
			}
		}
		if found || !page.Meta.HasMore {
			cursor = page.Meta.NextCursor
			break
		}
		req.Cursor = page.Meta.NextCursor
	}
	if cursor == "" {
		t.Fatal("could not obtain a cursor deep enough to be interesting")
	}

	var got statement
	session := db.Session(&gorm.Session{NewDB: true})
	err := session.Callback().Query().After("gorm:query").Register("paginate:capture", func(d *gorm.DB) {
		got.sql = d.Statement.SQL.String()
		got.vars = append([]any(nil), d.Statement.Vars...)
	})
	if err != nil {
		t.Fatalf("registering capture callback: %v", err)
	}

	if _, err := paginate.Keyset[Post](session.Table(table), p, paginate.Request{Limit: 25, Cursor: cursor}); err != nil {
		t.Fatalf("Keyset(): %v", err)
	}
	if got.sql == "" {
		t.Fatal("no statement was captured")
	}
	return got
}

func TestOffsetPagination(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 47, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	tests := []struct {
		name           string
		page           int
		wantRows       int
		wantHasMore    bool
		wantTotalPages int
	}{
		{"first page", 1, 10, true, 5},
		{"page zero is the first page", 0, 10, true, 5},
		{"a middle page", 3, 10, true, 5},
		{"the last page holds the remainder", 5, 7, false, 5},
		{"past the end", 6, 0, false, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := paginate.Offset[Post](query(db, table), p, paginate.OffsetRequest{
				Page: tt.page, Limit: 10,
			})
			if err != nil {
				t.Fatalf("Offset(): %v", err)
			}
			if len(page.Data) != tt.wantRows {
				t.Errorf("rows = %d, want %d", len(page.Data), tt.wantRows)
			}
			if page.Meta.Total != 47 {
				t.Errorf("Total = %d, want 47", page.Meta.Total)
			}
			if page.Meta.TotalPages != tt.wantTotalPages {
				t.Errorf("TotalPages = %d, want %d", page.Meta.TotalPages, tt.wantTotalPages)
			}
			if page.Meta.HasMore != tt.wantHasMore {
				t.Errorf("HasMore = %v, want %v", page.Meta.HasMore, tt.wantHasMore)
			}
			if page.Data == nil {
				t.Error("Data is nil; it must serialise as []")
			}
		})
	}
}

func TestOffsetRespectsCallerFilter(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 30, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	page, err := paginate.Offset[Post](
		query(db, table).Where("author_id = ?", 1), p, paginate.OffsetRequest{Limit: 5})
	if err != nil {
		t.Fatalf("Offset(): %v", err)
	}
	if page.Meta.Total != 10 {
		t.Errorf("Total = %d, want 10: the count must apply the caller's filter", page.Meta.Total)
	}
	for _, post := range page.Data {
		if post.AuthorID != 1 {
			t.Errorf("returned a row with author_id %d despite the filter", post.AuthorID)
		}
	}
}

func TestKeysetRespectsCallerFilter(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 60, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	var ids []int64
	req := paginate.Request{Limit: 4}
	for {
		page, err := paginate.Keyset[Post](query(db, table).Where("author_id = ?", 2), p, req)
		if err != nil {
			t.Fatalf("Keyset(): %v", err)
		}
		for _, post := range page.Data {
			if post.AuthorID != 2 {
				t.Fatalf("returned a row with author_id %d despite the filter", post.AuthorID)
			}
			ids = append(ids, post.ID)
		}
		if !page.Meta.HasMore {
			break
		}
		req.Cursor = page.Meta.NextCursor
	}
	if len(ids) != 20 {
		t.Errorf("walked %d filtered rows, want 20", len(ids))
	}
	if dupes := duplicates(ids); len(dupes) > 0 {
		t.Errorf("rows returned more than once: %v", dupes)
	}
}

// A cursor taken from one sort order must not be usable against another: the
// page would be plausible and wrong.
func TestCursorFromAnotherSortIsRefused(t *testing.T) {
	db, table := newTable(t)
	seed(t, db, table, 30, distinctTimes(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
	p := newPaginator(t)

	page, err := paginate.Keyset[Post](query(db, table), p, paginate.Request{Limit: 5, Sort: "-created_at"})
	if err != nil {
		t.Fatalf("Keyset(): %v", err)
	}

	for _, sort := range []string{"created_at", "title", "-title"} {
		t.Run(sort, func(t *testing.T) {
			_, err := paginate.Keyset[Post](query(db, table), p,
				paginate.Request{Limit: 5, Sort: sort, Cursor: page.Meta.NextCursor})
			if !errors.Is(err, paginate.ErrCursorMismatch) {
				t.Errorf("error = %v, want %v", err, paginate.ErrCursorMismatch)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	db, table := newTable(t)
	scoped := db.Table(table)

	t.Run("accepts a sound allowlist", func(t *testing.T) {
		if err := paginate.Validate[Post](scoped, newPaginator(t)); err != nil {
			t.Errorf("Validate(): %v", err)
		}
	})

	t.Run("rejects a nullable sort column", func(t *testing.T) {
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN published_at timestamptz", table)).Error; err != nil {
			t.Fatalf("adding column: %v", err)
		}
		t.Cleanup(func() {
			db.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN published_at", table))
		})

		// The Go field is a value type, so the cheap check passes and only the
		// database can tell us the column is nullable.
		type PostWithPublished struct {
			ID          int64 `gorm:"primaryKey"`
			CreatedAt   time.Time
			Title       string
			AuthorID    int64
			PublishedAt time.Time
		}

		a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
			Fields:   map[string][]paginate.Column{"published_at": {{Name: "published_at"}}},
			Default:  "-published_at",
			Tiebreak: paginate.Column{Name: "id"},
		})
		if err != nil {
			t.Fatalf("NewAllowlist(): %v", err)
		}
		p, err := paginate.New(paginate.Config{Allowlist: a})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}

		if err := paginate.Validate[PostWithPublished](db.Table(table), p); !errors.Is(err, paginate.ErrUnsortableField) {
			t.Errorf("Validate() error = %v, want %v", err, paginate.ErrUnsortableField)
		}
	})

	t.Run("rejects a column the table does not have", func(t *testing.T) {
		a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
			Fields:   map[string][]paginate.Column{"title": {{Name: "title"}}},
			Default:  "title",
			Tiebreak: paginate.Column{Name: "nonexistent"},
		})
		if err != nil {
			t.Fatalf("NewAllowlist(): %v", err)
		}
		p, err := paginate.New(paginate.Config{Allowlist: a})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if err := paginate.Validate[Post](scoped, p); !errors.Is(err, paginate.ErrUnsortableField) {
			t.Errorf("Validate() error = %v, want %v", err, paginate.ErrUnsortableField)
		}
	})
}
