package paginate_test

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wimwenigerkind/paginate"
	"github.com/wimwenigerkind/paginate/ginx"
)

type Article struct {
	ID        int64     `gorm:"primaryKey"`
	CreatedAt time.Time `gorm:"not null"`
	Title     string    `gorm:"not null"`
	AuthorID  int64     `gorm:"not null"`
}

// The wiring a service does once, at startup.
func ExampleNew() {
	allowlist, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields: map[string][]paginate.Column{
			"created_at": {{Name: "created_at"}},
			"title":      {{Name: "title"}},
			// One token, several columns: composite sorts stay the service's
			// decision rather than something a client composes.
			"author": {{Name: "author_id"}, {Name: "title"}},
		},
		Default:  "-created_at",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		panic(err)
	}

	paginator, err := paginate.New(paginate.Config{
		Allowlist:    allowlist,
		DefaultLimit: 20,
		MaxLimit:     100,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(paginator.Allowlist().Tokens())
	// Output: [author created_at title]
}

type articleHandler struct {
	db        *gorm.DB
	paginator *paginate.Paginator
}

// The handler. Filtering is ordinary GORM in your own code; paginate adds only
// the seek predicate, the ORDER BY and the LIMIT.
func ExampleKeyset() {
	var h articleHandler

	handle := func(c *gin.Context) {
		q := h.db.Model(&Article{})
		if author := c.Query("author_id"); author != "" {
			q = q.Where("author_id = ?", author)
		}

		page, err := paginate.Keyset[Article](q, h.paginator, ginx.Bind(c))
		if err != nil {
			// Abort renders the 400s; anything else stays the service's to
			// handle, because it is not the client's fault.
			if !ginx.Abort(c, err) {
				_ = c.Error(err)
			}
			return
		}
		c.JSON(http.StatusOK, page)
	}
	_ = handle
}

// Offset paging, for a page-number UI that needs a total.
func ExampleOffset() {
	var h articleHandler

	handle := func(c *gin.Context) {
		page, err := paginate.Offset[Article](h.db.Model(&Article{}), h.paginator, ginx.BindOffset(c))
		if err != nil {
			if !ginx.Abort(c, err) {
				_ = c.Error(err)
			}
			return
		}
		c.JSON(http.StatusOK, page)
	}
	_ = handle
}

// Errors carry a stable code and match with errors.Is.
func ExampleError() {
	_, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields:  map[string][]paginate.Column{"title": {{Name: "title"}}},
		Default: "-title",
		// No Tiebreak: without one, rows sharing a sort value have no defined
		// order and paging through them skips and repeats rows.
	})

	fmt.Println(errors.Is(err, paginate.ErrConfig))

	var perr *paginate.Error
	if errors.As(err, &perr) {
		fmt.Println(perr.Code)
	}
	// Output:
	// true
	// config
}

// Serving both shapes during a migration: clients still sending page= keep the
// old response, everyone else gets cursors.
func ExampleKeyset_migration() {
	var h articleHandler

	handle := func(c *gin.Context) {
		_, sendsPage := c.GetQuery("page")
		if sendsPage {
			// ... the existing offset-based handler ...
			return
		}
		page, err := paginate.Keyset[Article](h.db.Model(&Article{}), h.paginator, ginx.Bind(c))
		if err != nil {
			if !ginx.Abort(c, err) {
				_ = c.Error(err)
			}
			return
		}
		c.JSON(http.StatusOK, page)
	}
	_ = handle
}
