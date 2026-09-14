package paginate

import (
	"context"
	"reflect"

	"gorm.io/gorm"
)

// Request is the client input for keyset pagination. Bind it from the query
// string with the ginx subpackage, or fill it in yourself.
type Request struct {
	// Limit is the maximum number of rows to return. Zero means the
	// paginator's default.
	Limit int
	// Cursor is an opaque cursor from a previous response's Meta. Empty means
	// the first page.
	Cursor string
	// Sort is an allowlisted sort token, optionally prefixed with "-" for
	// descending order. Empty means the allowlist's default.
	Sort string
}

// Page is the keyset response envelope.
type Page[T any] struct {
	// Data is never nil: an empty page serialises as [].
	Data []T  `json:"data"`
	Meta Meta `json:"meta"`
}

// Meta describes the page's place in the sequence.
type Meta struct {
	// NextCursor fetches the page after this one. Absent when there is none.
	NextCursor string `json:"next_cursor,omitempty"`
	// PrevCursor fetches the page before this one. Absent on the first page.
	PrevCursor string `json:"prev_cursor,omitempty"`
	// HasMore reports whether a page follows this one. When paging backward it
	// reports the state at the time the request was made: the row the cursor
	// came from may since have been deleted.
	HasMore bool `json:"has_more"`
	// Limit is the limit actually applied, after defaults.
	Limit int `json:"limit"`
}

// Keyset runs a keyset-paginated query.
//
// db may already carry Where, Joins, Preload, Select and a context; paginate
// adds the seek predicate, the ORDER BY and the LIMIT, and nothing else. It
// must not already carry an ORDER BY or a LIMIT, because paginate owns those
// and a caller-supplied ordering would silently break paging.
//
// The caller's db is left untouched: all conditions are added to an isolated
// session.
//
// Errors from this package are *Error and carry a Code; errors from the
// database are returned as they came.
func Keyset[T any](db *gorm.DB, p *Paginator, req Request) (Page[T], error) {
	var empty Page[T]

	if db == nil {
		return empty, errf(CodeConfig, "", "Keyset needs a database handle")
	}
	if p == nil {
		return empty, errf(CodeConfig, "", "Keyset needs a Paginator")
	}
	if err := clauseConflict(db); err != nil {
		return empty, err
	}

	limit, err := p.resolveLimit(req.Limit)
	if err != nil {
		return empty, err
	}
	keys, err := p.allow.resolve(req.Sort)
	if err != nil {
		return empty, err
	}

	var model T
	bound, err := bind(db, keys, &model, true)
	if err != nil {
		return empty, err
	}

	dir := forward
	var values []any
	if req.Cursor != "" {
		d, fields, err := decodeCursor(req.Cursor, keys)
		if err != nil {
			return empty, err
		}
		dir = d
		values = make([]any, len(bound))
		for i, k := range bound {
			v, err := decodeValue(fields[keys[i].qualified()], k.field.FieldType)
			if err != nil {
				return empty, err
			}
			values[i] = v
		}
	}

	// Paging backward runs the same query with every direction flipped, then
	// reverses the rows back into display order.
	flip := dir == backward

	// Isolate our clauses from the caller's handle while keeping the
	// conditions they already put on it.
	tx := db.Session(&gorm.Session{})
	if values != nil {
		sql, args := seekPredicate(tx, bound, values, flip)
		tx = tx.Where(sql, args...)
	}
	tx = applyOrder(tx, bound, flip)

	// One row beyond the limit tells us whether another page exists, without a
	// second query and without a count.
	rows := make([]T, 0, limit+1)
	if err := tx.Limit(limit + 1).Find(&rows).Error; err != nil {
		return empty, err
	}

	extra := len(rows) > limit
	if extra {
		rows = rows[:limit]
	}
	if flip {
		reverse(rows)
	}

	page := Page[T]{Data: rows, Meta: Meta{Limit: limit}}
	if page.Data == nil {
		page.Data = []T{}
	}
	if len(rows) == 0 {
		return page, nil
	}

	// has_more and the cursors describe display order, not the direction this
	// particular request happened to travel in.
	hasMore, hasPrev := extra, req.Cursor != ""
	if flip {
		// We came from a cursor that sits after this page, so a page follows.
		// The extra row is evidence of a page before this one instead.
		hasMore, hasPrev = true, extra
	}

	page.Meta.HasMore = hasMore
	if hasMore {
		if page.Meta.NextCursor, err = cursorAt(keys, bound, forward, rows[len(rows)-1]); err != nil {
			return empty, err
		}
	}
	if hasPrev {
		if page.Meta.PrevCursor, err = cursorAt(keys, bound, backward, rows[0]); err != nil {
			return empty, err
		}
	}
	return page, nil
}

// cursorAt encodes the sort values of one row. keys and bound are parallel:
// keys supplies the declared names the cursor is keyed by, bound supplies the
// struct fields the values are read from.
func cursorAt[T any](keys []sortKey, bound []resolvedKey, dir direction, row T) (string, error) {
	rv := reflect.ValueOf(row)
	values := make(map[string]any, len(bound))
	for i, k := range bound {
		v, _ := k.field.ValueOf(context.Background(), rv)
		values[keys[i].qualified()] = v
	}
	return encodeCursor(keys, dir, values)
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
