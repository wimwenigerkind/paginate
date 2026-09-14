package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/wimwenigerkind/paginate"
	"github.com/wimwenigerkind/paginate/ginx"
)

// Post is an ordinary GORM model. Nothing about it is paginate-specific.
type Post struct {
	ID        int64     `gorm:"primaryKey"                 json:"id"`
	CreatedAt time.Time `gorm:"not null;index:idx_posts_keyset,sort:desc,priority:1" json:"created_at"`
	Title     string    `gorm:"not null"                   json:"title"`
	AuthorID  int64     `gorm:"not null"                   json:"author_id"`
}

// PostsHandler owns one Paginator per resource. There is no global state and
// nothing to initialise at import time.
type PostsHandler struct {
	db        *gorm.DB
	paginator *paginate.Paginator
}

func NewPostsHandler(db *gorm.DB) (*PostsHandler, error) {
	// The allowlist is the security boundary: a client picks one of these
	// tokens and a direction, and nothing else ever becomes a SQL identifier.
	// author_id is deliberately absent, so clients cannot sort by it.
	allowlist, err := paginate.NewAllowlist(paginate.AllowlistConfig{
		Fields: map[string][]paginate.Column{
			"created_at": {{Name: "created_at"}},
			"title":      {{Name: "title"}},
		},
		Default:  "-created_at",
		Tiebreak: paginate.Column{Name: "id"},
	})
	if err != nil {
		return nil, err
	}

	paginator, err := paginate.New(paginate.Config{
		Allowlist:    allowlist,
		DefaultLimit: 20,
		MaxLimit:     100,
	})
	if err != nil {
		return nil, err
	}

	// Fail at boot rather than on the first request that happens to use a
	// broken sort. This is the only check that asks the database whether the
	// columns are really NOT NULL and the tiebreak really unique.
	if err := paginate.Validate[Post](db, paginator); err != nil {
		return nil, fmt.Errorf("pagination config does not match the posts table: %w", err)
	}

	return &PostsHandler{db: db, paginator: paginator}, nil
}

// List is the keyset-paginated endpoint: the default for feeds and infinite
// scrolling, and the one that behaves under concurrent writes.
func (h *PostsHandler) List(c *gin.Context) {
	// The handler's own filtering goes on the query as usual. paginate adds
	// only the seek predicate, the ORDER BY and the LIMIT.
	q := h.db.Model(&Post{})
	if author := c.Query("author_id"); author != "" {
		q = q.Where("author_id = ?", author)
	}

	page, err := paginate.Keyset[Post](q, h.paginator, ginx.Bind(c))
	if err != nil {
		if !ginx.Abort(c, err) {
			log.Printf("listing posts: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				gin.H{"error": gin.H{"code": "internal", "message": "could not list posts"}})
		}
		return
	}
	c.JSON(http.StatusOK, page)
}

// ListPages is the offset-paginated endpoint, for a page-number UI that needs
// a total. It is the secondary option: under concurrent writes it re-shows and
// skips rows, and it pays for a COUNT on every request.
func (h *PostsHandler) ListPages(c *gin.Context) {
	page, err := paginate.Offset[Post](h.db.Model(&Post{}), h.paginator, ginx.BindOffset(c))
	if err != nil {
		if !ginx.Abort(c, err) {
			log.Printf("listing posts: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				gin.H{"error": gin.H{"code": "internal", "message": "could not list posts"}})
		}
		return
	}
	c.JSON(http.StatusOK, page)
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=example dbname=example port=5432 sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("connecting to PostgreSQL: %v\n\n"+
			"Start one with:\n"+
			"  docker run --rm -d -p 5432:5432 -e POSTGRES_PASSWORD=example \\\n"+
			"      -e POSTGRES_DB=example --name paginate-example postgres:17-alpine\n"+
			"or point DATABASE_URL at your own.", err)
	}
	if err := db.AutoMigrate(&Post{}); err != nil {
		log.Fatalf("migrating: %v", err)
	}
	if err := seed(db); err != nil {
		log.Fatalf("seeding: %v", err)
	}

	handler, err := NewPostsHandler(db)
	if err != nil {
		log.Fatalf("wiring the posts handler: %v", err)
	}

	r := gin.Default()
	r.GET("/posts", handler.List)
	r.GET("/posts/pages", handler.ListPages)

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	log.Printf("listening on %s; try curl 'localhost%s/posts?limit=5'", addr, addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("serving: %v", err)
	}
}

func seed(db *gorm.DB) error {
	var count int64
	if err := db.Model(&Post{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	posts := make([]Post, 200)
	for i := range posts {
		posts[i] = Post{
			// Every fourth post shares a timestamp with its neighbour, so the
			// tiebreak column has something to do.
			CreatedAt: base.Add(time.Duration(i/4) * time.Hour),
			Title:     fmt.Sprintf("Post number %03d", i+1),
			AuthorID:  int64(i%5 + 1),
		}
	}
	return db.CreateInBatches(posts, 100).Error
}
