package paginate

import (
	"gorm.io/gorm"
)

// OffsetRequest is the client input for offset pagination.
type OffsetRequest struct {
	// Page is the 1-based page number. Zero means the first page.
	Page int
	// Limit is the page size. Zero means the paginator's default.
	Limit int
	// Sort is an allowlisted sort token, optionally prefixed with "-".
	Sort string
}

// OffsetPage is the offset response envelope. It shares data and
// meta.has_more with Page so a client can read either shape for those.
type OffsetPage[T any] struct {
	// Data is never nil: an empty page serialises as [].
	Data []T        `json:"data"`
	Meta OffsetMeta `json:"meta"`
}

// OffsetMeta describes the page's place in the sequence.
type OffsetMeta struct {
	Page  int `json:"page"`
	Limit int `json:"limit"`
	// Total is the number of matching rows at the moment of the count. It is a
	// separate query and it is stale as soon as anything is written.
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
	HasMore    bool  `json:"has_more"`
}

// Offset runs an offset-paginated query. It exists for page-number user
// interfaces; prefer Keyset everywhere else.
//
// Offset pagination is not stable under concurrent writes. A row inserted
// before the current position shifts every later row down by one, so the
// client sees a row twice; a deletion shifts them up, so the client never sees
// a row at all. It also pays for a COUNT over the full result set on every
// request. Neither is a bug in this implementation. It is what paging by row
// offset means.
//
// The caller's db is left untouched, and must not already carry an ORDER BY or
// a LIMIT.
func Offset[T any](db *gorm.DB, p *Paginator, req OffsetRequest) (OffsetPage[T], error) {
	var empty OffsetPage[T]

	if db == nil {
		return empty, errf(CodeConfig, "", "Offset needs a database handle")
	}
	if p == nil {
		return empty, errf(CodeConfig, "", "Offset needs a Paginator")
	}
	if err := clauseConflict(db); err != nil {
		return empty, err
	}
	if req.Page < 0 {
		return empty, errf(CodeInvalidPage, "page", "page must be a positive number")
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
	// Offset paging tolerates nullable sort columns: NULLs sort to one end
	// rather than dropping out of the result.
	bound, err := bind(db, keys, &model, false)
	if err != nil {
		return empty, err
	}

	page := req.Page
	if page == 0 {
		page = 1
	}
	offset := (page - 1) * limit

	var total int64
	countTx := db.Session(&gorm.Session{})
	if db.Statement == nil || (db.Statement.Model == nil && db.Statement.Table == "") {
		countTx = countTx.Model(&model)
	}
	if err := countTx.Count(&total).Error; err != nil {
		return empty, err
	}

	rows := make([]T, 0, limit)
	tx := applyOrder(db.Session(&gorm.Session{}), bound, false)
	if err := tx.Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return empty, err
	}
	if rows == nil {
		rows = []T{}
	}

	totalPages := int(total / int64(limit))
	if total%int64(limit) != 0 {
		totalPages++
	}

	return OffsetPage[T]{
		Data: rows,
		Meta: OffsetMeta{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
			HasMore:    int64(offset+len(rows)) < total,
		},
	}, nil
}
