# paginate

Keyset and offset pagination for Gin and GORM services, with one shared
list-response contract.

## Install

```sh
go get github.com/wimwenigerkind/paginate
```

Go 1.25 or newer. Depends on gorm, plus gin in the `ginx` subpackage.

## Use

```go
allowlist, err := paginate.NewAllowlist(paginate.AllowlistConfig{
    Fields: map[string][]paginate.Column{
        "created_at": {{Name: "created_at"}},
        "title":      {{Name: "title"}},
    },
    Default:  "-created_at",
    Tiebreak: paginate.Column{Name: "id"},
})

paginator, err := paginate.New(paginate.Config{
    Allowlist: allowlist, DefaultLimit: 20, MaxLimit: 100,
})

err = paginate.Validate[Post](db, paginator)
```

```go
page, err := paginate.Keyset[Post](h.db.Model(&Post{}), h.paginator, ginx.Bind(c))
if err != nil {
    if !ginx.Abort(c, err) {
        _ = c.Error(err)
    }
    return
}
c.JSON(http.StatusOK, page)
```

Sort columns come from the allowlist, never from the request. `Tiebreak` is
required and must be unique and NOT NULL. Index the sort to match:

```sql
CREATE INDEX idx_posts_keyset ON posts (created_at DESC, id DESC);
```

`paginate.Offset` serves page numbers and a total where an interface needs
them. A runnable service is in [`example/`](example/).

## Contract

```
GET /posts?limit=25&sort=-created_at&cursor=eyJ2Ijox...
```

```json
{
  "data": [],
  "meta": { "next_cursor": "...", "has_more": true, "limit": 25 }
}
```

`data` is never `null`. Branch on `has_more`. Cursors are opaque: pass back
what you were given.

Bad requests fail with a `*paginate.Error` carrying a stable `Code`
(`invalid_cursor`, `cursor_mismatch`, `invalid_limit`, `limit_too_large`,
`unknown_sort`, `invalid_page`). `ginx.Abort` renders those as 400 and returns
`false` for anything that is not the client's fault.

## Adopting it

Call `Validate` at boot, add the index, then serve both shapes for one release
by keeping the existing handler for requests that send `page=`. Clients move to
following `meta.next_cursor` until `meta.has_more` is false.

## Testing

```sh
go test ./...
go test -tags integration ./...
```
