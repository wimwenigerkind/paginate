package paginate

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/wimwenigerkind/paginate/internal/model"
)

// resolvedKey is a sort key bound to a model: the struct field its value is
// read from, plus the table qualifier to emit in SQL.
//
// sqlTable is kept apart from the declared Column.Table on purpose. Cursors
// and the sort fingerprint are keyed by the *declared* name, so they stay
// stable no matter what the model's table turns out to be called.
type resolvedKey struct {
	sortKey
	sqlTable string
	field    *schema.Field
}

func (k resolvedKey) column() clause.Column {
	return clause.Column{Table: k.sqlTable, Name: k.Name}
}

// bind locates each sort key on the model. Table qualifiers left empty are
// filled in with the model's table, so an unqualified column stays unambiguous
// once a join is added.
//
// requireNotNull rejects nullable columns. Keyset paging needs it, because a
// NULL sort key makes every comparison against it neither true nor false and
// the row drops out of every page. Offset paging does not: NULLs just sort to
// one end.
func bind(db *gorm.DB, keys []sortKey, dest any, requireNotNull bool) ([]resolvedKey, error) {
	s, err := model.Parse(db, dest)
	if err != nil {
		return nil, wrapf(CodeConfig, "", err, "cannot read the schema of the model being paginated")
	}

	// Qualify unqualified columns with the table the query actually reads
	// from. The caller may have pointed the query at a different table than
	// the model's default (a per-tenant table, an archive, a test fixture),
	// and GORM keeps that name, alias included, in Statement.Table.
	fallback := s.Table
	if db.Statement != nil && db.Statement.Table != "" {
		fallback = db.Statement.Table
	}

	out := make([]resolvedKey, len(keys))
	for i, k := range keys {
		f := model.Field(s, k.Name)
		if f == nil {
			return nil, errf(CodeUnsortableField, "",
				"sort column %q is not a column of %s; keyset paging reads the column's value "+
					"out of the returned row, so it has to be selected into the model",
				k.Name, s.Name)
		}
		if n := model.NullabilityOf(f); requireNotNull && n != model.NotNullable {
			return nil, errf(CodeUnsortableField, "",
				"sort column %q is %s and so may be NULL; a NULL sort key silently drops rows "+
					"from every page, because SQL comparisons against NULL are neither true nor false",
				k.Name, n)
		}
		sqlTable := k.Table
		if sqlTable == "" {
			sqlTable = fallback
		}
		out[i] = resolvedKey{sortKey: k, sqlTable: sqlTable, field: f}
	}
	return out, nil
}

// applyOrder appends ORDER BY for every sort key. Identifiers go through
// clause.OrderByColumn so the dialector quotes them; no part of a sort ever
// reaches SQL as concatenated text.
func applyOrder(db *gorm.DB, keys []resolvedKey, flip bool) *gorm.DB {
	for _, k := range keys {
		db = db.Order(clause.OrderByColumn{
			Column: k.column(),
			Desc:   k.desc != flip,
		})
	}
	return db
}

// seekPredicate builds the WHERE that skips everything up to and including the
// cursor's row.
//
// When every key runs the same way the predicate is a row-value comparison,
// which PostgreSQL can answer straight from a matching index. The OR-chain is
// only correct-but-slow: measured on 200k rows it read 1012 buffers where the
// row-value form read 4, because the planner applies it as a filter after the
// scan rather than as an index condition.
func seekPredicate(db *gorm.DB, keys []resolvedKey, values []any, flip bool) (string, []any) {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = db.Statement.Quote(k.column())
	}

	op := func(i int) string {
		if keys[i].desc != flip {
			return "<"
		}
		return ">"
	}

	if uniform(keys) {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(keys)), ", ")
		return "(" + strings.Join(quoted, ", ") + ") " + op(0) + " (" + placeholders + ")", values
	}

	// Mixed directions: (a > x) OR (a = x AND b < y) OR ...
	terms := make([]string, len(keys))
	args := make([]any, 0, len(keys)*(len(keys)+1)/2)
	for i := range keys {
		var b strings.Builder
		b.WriteByte('(')
		for j := 0; j < i; j++ {
			b.WriteString(quoted[j])
			b.WriteString(" = ? AND ")
			args = append(args, values[j])
		}
		b.WriteString(quoted[i])
		b.WriteByte(' ')
		b.WriteString(op(i))
		b.WriteString(" ?")
		b.WriteByte(')')
		args = append(args, values[i])
		terms[i] = b.String()
	}
	// The outer parentheses matter: without them this ORs with whatever the
	// caller already put in the WHERE clause instead of ANDing with it.
	return "(" + strings.Join(terms, " OR ") + ")", args
}

// uniform reports whether every key sorts the same way, which is the condition
// for using row-value comparison instead of the OR-chain fallback.
func uniform(keys []resolvedKey) bool {
	for _, k := range keys[1:] {
		if k.desc != keys[0].desc {
			return false
		}
	}
	return true
}

// clauseConflict reports a caller-supplied clause that keyset paging cannot
// coexist with. Ordering is the dangerous one: GORM appends our sort keys
// after the caller's, so the rows would come back in an order the seek
// predicate does not describe, and pages would skip and repeat rows.
func clauseConflict(db *gorm.DB) error {
	if db.Statement == nil {
		return nil
	}
	for _, name := range []string{"ORDER BY", "LIMIT"} {
		if _, ok := db.Statement.Clauses[name]; ok {
			return errf(CodeConfig, "",
				"the query already has a %s clause; paginate owns ordering and limiting, "+
					"so pass a query without them and declare the sort in the allowlist instead", name)
		}
	}
	return nil
}
