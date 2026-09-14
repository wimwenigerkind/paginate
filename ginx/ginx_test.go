package ginx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils/tests"

	"github.com/wimwenigerkind/paginate"
)

// dryRunDB renders SQL without a database, so the binding can be checked
// against the real paginate entry points.
func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true, Logger: logger.Discard})
	if err != nil {
		t.Fatalf("opening dry-run database: %v", err)
	}
	return db
}

func init() { gin.SetMode(gin.TestMode) }

func contextFor(t *testing.T, query string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/posts?"+query, nil)
	return c
}

func TestBind(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  paginate.Request
	}{
		{
			name:  "empty query uses zero values",
			query: "",
			want:  paginate.Request{},
		},
		{
			name:  "all parameters",
			query: "limit=50&cursor=abc&sort=-created_at",
			want:  paginate.Request{Limit: 50, Cursor: "abc", Sort: "-created_at"},
		},
		{
			name:  "unparseable limit becomes an invalid limit, not a default",
			query: "limit=banana",
			want:  paginate.Request{Limit: unparseable},
		},
		{
			name:  "empty limit means unset",
			query: "limit=&sort=title",
			want:  paginate.Request{Sort: "title"},
		},
		{
			name:  "negative limit is passed through to be refused",
			query: "limit=-5",
			want:  paginate.Request{Limit: -5},
		},
		{
			name:  "a cursor containing base64url characters survives",
			query: "cursor=eyJ2Ijox-_abc",
			want:  paginate.Request{Cursor: "eyJ2Ijox-_abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Bind(contextFor(t, tt.query)); got != tt.want {
				t.Errorf("Bind() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBindOffset(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  paginate.OffsetRequest
	}{
		{name: "empty query", query: "", want: paginate.OffsetRequest{}},
		{
			name:  "all parameters",
			query: "page=3&limit=10&sort=title",
			want:  paginate.OffsetRequest{Page: 3, Limit: 10, Sort: "title"},
		},
		{
			name:  "unparseable page",
			query: "page=last",
			want:  paginate.OffsetRequest{Page: unparseable},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BindOffset(contextFor(t, tt.query)); got != tt.want {
				t.Errorf("BindOffset() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A number ginx could not parse must reach paginate as something paginate
// refuses, not as "unset". Otherwise "limit=banana" would quietly return the
// default page size and nobody would learn the request was wrong.
func TestUnparseableNumbersAreRefusedDownstream(t *testing.T) {
	a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields:   map[string][]paginate.Column{"id": {{Name: "id"}}},
		Default:  "-id",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}
	p, err := paginate.New(paginate.Config{Allowlist: a})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	type row struct{ ID int64 }
	db := dryRunDB(t)

	if _, err := paginate.Keyset[row](db.Model(&row{}), p, Bind(contextFor(t, "limit=banana"))); !errors.Is(err, paginate.ErrInvalidLimit) {
		t.Errorf("Keyset() error = %v, want %v", err, paginate.ErrInvalidLimit)
	}
	if _, err := paginate.Offset[row](db.Model(&row{}), p, BindOffset(contextFor(t, "page=last"))); !errors.Is(err, paginate.ErrInvalidPage) {
		t.Errorf("Offset() error = %v, want %v", err, paginate.ErrInvalidPage)
	}
}

func TestError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantOK    bool
		wantCode  string
		wantParam string
	}{
		{
			name:      "limit too large is the client's fault",
			err:       &paginate.Error{Code: paginate.CodeLimitTooLarge, Param: "limit"},
			wantOK:    true,
			wantCode:  "limit_too_large",
			wantParam: "limit",
		},
		{
			name:      "invalid cursor is the client's fault",
			err:       &paginate.Error{Code: paginate.CodeInvalidCursor, Param: "cursor"},
			wantOK:    true,
			wantCode:  "invalid_cursor",
			wantParam: "cursor",
		},
		{
			name:     "cursor mismatch is the client's fault",
			err:      &paginate.Error{Code: paginate.CodeCursorMismatch, Param: "cursor"},
			wantOK:   true,
			wantCode: "cursor_mismatch",
		},
		{
			name:   "a configuration mistake is not a 400",
			err:    &paginate.Error{Code: paginate.CodeConfig},
			wantOK: false,
		},
		{
			name:   "an unsortable field is not a 400",
			err:    &paginate.Error{Code: paginate.CodeUnsortableField},
			wantOK: false,
		},
		{
			name:   "a database error is not ours to render",
			err:    errors.New("connection refused"),
			wantOK: false,
		},
		{
			name:   "a nil error renders nothing",
			err:    nil,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body, ok := Error(tt.err)
			if ok != tt.wantOK {
				t.Fatalf("Error() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if tt.wantParam != "" && body.Error.Param != tt.wantParam {
				t.Errorf("param = %q, want %q", body.Error.Param, tt.wantParam)
			}
		})
	}
}

func TestAbort(t *testing.T) {
	t.Run("writes a 400 for a client fault", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		if !Abort(c, &paginate.Error{Code: paginate.CodeUnknownSort, Param: "sort"}) {
			t.Fatal("Abort() = false, want true")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if !c.IsAborted() {
			t.Error("Abort() did not abort the context")
		}

		var body ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		if body.Error.Code != "unknown_sort" {
			t.Errorf("code = %q, want %q", body.Error.Code, "unknown_sort")
		}
	})

	t.Run("writes nothing for a server fault", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		if Abort(c, errors.New("connection refused")) {
			t.Fatal("Abort() = true, want false")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("Abort() wrote a body for an error it does not own: %s", rec.Body)
		}
	})
}

// The body shown to a client must not carry the wrapped cause: it is usually
// an encoding/json parse error describing our internals, not the request.
func TestErrorBodyOmitsTheUnderlyingCause(t *testing.T) {
	a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields:   map[string][]paginate.Column{"id": {{Name: "id"}}},
		Default:  "-id",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}
	p, err := paginate.New(paginate.Config{Allowlist: a})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	type row struct{ ID int64 }
	_, err = paginate.Keyset[row](dryRunDB(t).Model(&row{}), p,
		Bind(contextFor(t, "cursor=bm90anNvbg")))
	if err == nil {
		t.Fatal("expected an error for a malformed cursor")
	}

	_, body, ok := Error(err)
	if !ok {
		t.Fatalf("Error() did not recognise %v as a client fault", err)
	}
	if strings.Contains(body.Error.Message, "invalid character") {
		t.Errorf("the client-facing message leaks the parse error: %q", body.Error.Message)
	}
	// The cause is still there for logs.
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("the cause was dropped from the logged error: %q", err.Error())
	}
}

// A limit that could not be parsed must not be echoed back: the value the
// client sees would be our sentinel, not what they sent.
func TestUnparseableLimitMessageDoesNotEchoTheSentinel(t *testing.T) {
	a, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields:   map[string][]paginate.Column{"id": {{Name: "id"}}},
		Default:  "-id",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		t.Fatalf("NewAllowlist(): %v", err)
	}
	p, err := paginate.New(paginate.Config{Allowlist: a})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	type row struct{ ID int64 }
	_, err = paginate.Keyset[row](dryRunDB(t).Model(&row{}), p, Bind(contextFor(t, "limit=banana")))
	_, body, ok := Error(err)
	if !ok {
		t.Fatalf("Error() did not recognise %v as a client fault", err)
	}
	if strings.Contains(body.Error.Message, "-1") {
		t.Errorf("the message echoes the internal sentinel: %q", body.Error.Message)
	}
}
