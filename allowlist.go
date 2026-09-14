package paginate

import (
	"crypto/sha256"
	"encoding/base64"
	"sort"
	"strings"
)

// Column is a database column that may be sorted on.
//
// Name and Table are compiled into the service; they are never derived from
// request data. The only thing a client chooses is which allowlisted token to
// ask for, and in which direction.
type Column struct {
	// Name is the database column name, for example "created_at".
	Name string
	// Table optionally qualifies the column for queries with joins. When it is
	// empty the model's own table is used, so an unqualified column stays
	// unambiguous even after a join is added to the query.
	Table string
	// Invert sorts this column opposite to the requested direction. It exists
	// for sorts like "newest first, then alphabetically", and it is not free:
	// a sort whose columns do not all run the same way cannot use row-value
	// comparison and falls back to a slower predicate.
	Invert bool
}

func (c Column) qualified() string {
	if c.Table == "" {
		return c.Name
	}
	return c.Table + "." + c.Name
}

// AllowlistConfig declares which sort orders a client may ask for.
type AllowlistConfig struct {
	// Fields maps a client-facing token to the columns it sorts by. A token
	// may map to several columns, which keeps composite sorts a decision the
	// service makes rather than something a client composes.
	Fields map[string][]Column

	// Default is the sort used when a request carries none. A "-" prefix means
	// descending, for example "-created_at". It must name a key of Fields.
	Default string

	// Tiebreak is appended to every sort so that the total order is unique.
	// It must be a unique, NOT NULL column; usually the primary key. Without
	// it, rows sharing a sort value have no defined order and paging through
	// them skips and repeats rows.
	Tiebreak Column
}

// Allowlist is a validated, immutable set of permitted sort orders. It is safe
// for concurrent use.
type Allowlist struct {
	fields   map[string][]Column
	tokens   []string
	defToken string
	defDesc  bool
	tiebreak Column
}

// NewAllowlist validates cfg and returns an immutable allowlist. It reports a
// CodeConfig error for anything that would be a programming mistake.
func NewAllowlist(cfg AllowlistConfig) (*Allowlist, error) {
	if len(cfg.Fields) == 0 {
		return nil, errf(CodeConfig, "", "allowlist needs at least one sortable field")
	}
	if cfg.Tiebreak.Name == "" {
		return nil, errf(CodeConfig, "", "allowlist needs a Tiebreak column")
	}
	if cfg.Tiebreak.Invert {
		return nil, errf(CodeConfig, "", "Tiebreak column %q must not set Invert: the tiebreak always follows the requested direction", cfg.Tiebreak.Name)
	}
	if cfg.Default == "" {
		return nil, errf(CodeConfig, "", "allowlist needs a Default sort")
	}

	a := &Allowlist{
		fields:   make(map[string][]Column, len(cfg.Fields)),
		tokens:   make([]string, 0, len(cfg.Fields)),
		tiebreak: cfg.Tiebreak,
	}

	for token, cols := range cfg.Fields {
		if err := validateToken(token); err != nil {
			return nil, err
		}
		if len(cols) == 0 {
			return nil, errf(CodeConfig, "", "sort field %q maps to no columns", token)
		}
		seen := make(map[string]bool, len(cols))
		copied := make([]Column, len(cols))
		for i, col := range cols {
			if col.Name == "" {
				return nil, errf(CodeConfig, "", "sort field %q has a column with an empty Name", token)
			}
			if seen[col.qualified()] {
				return nil, errf(CodeConfig, "", "sort field %q lists column %q twice", token, col.qualified())
			}
			seen[col.qualified()] = true
			copied[i] = col
		}
		a.fields[token] = copied
		a.tokens = append(a.tokens, token)
	}
	sort.Strings(a.tokens)

	defToken, defDesc := parseSortToken(cfg.Default)
	if _, ok := a.fields[defToken]; !ok {
		return nil, errf(CodeConfig, "", "Default sort %q is not one of the allowlisted fields %s", cfg.Default, quoteList(a.tokens))
	}
	a.defToken, a.defDesc = defToken, defDesc

	return a, nil
}

// Tokens returns the allowlisted sort tokens in sorted order. Useful for
// documenting an endpoint or rendering a helpful error.
func (a *Allowlist) Tokens() []string {
	out := make([]string, len(a.tokens))
	copy(out, a.tokens)
	return out
}

func validateToken(token string) error {
	switch {
	case token == "":
		return errf(CodeConfig, "", "allowlist has a sort field with an empty name")
	case strings.HasPrefix(token, "-"):
		return errf(CodeConfig, "", "sort field %q must not start with %q: the prefix is how a client asks for descending order", token, "-")
	case strings.ContainsAny(token, " ,\t\n"):
		return errf(CodeConfig, "", "sort field %q must not contain whitespace or commas", token)
	}
	return nil
}

func parseSortToken(s string) (token string, desc bool) {
	if strings.HasPrefix(s, "-") {
		return s[1:], true
	}
	return s, false
}

// sortKey is one column of a resolved sort, with its effective direction.
type sortKey struct {
	Column
	desc bool
}

// resolve turns a client-supplied sort token into the full list of sort keys,
// tiebreak included. An empty token selects the configured default.
func (a *Allowlist) resolve(requested string) ([]sortKey, error) {
	token, desc := a.defToken, a.defDesc
	if requested != "" {
		token, desc = parseSortToken(requested)
	}

	cols, ok := a.fields[token]
	if !ok {
		return nil, errf(CodeUnknownSort, "sort",
			"unknown sort field %q, allowed fields are %s", token, quoteList(a.tokens))
	}

	keys := make([]sortKey, 0, len(cols)+1)
	seen := make(map[string]bool, len(cols)+1)
	for _, col := range cols {
		keys = append(keys, sortKey{Column: col, desc: desc != col.Invert})
		seen[col.qualified()] = true
	}
	// The tiebreak always follows the requested direction, and is skipped when
	// the sort already ends on that column.
	if !seen[a.tiebreak.qualified()] {
		keys = append(keys, sortKey{Column: a.tiebreak, desc: desc})
	}
	return keys, nil
}

// fingerprint identifies a sort order. It travels inside the cursor so that a
// cursor replayed against a different sort order fails loudly instead of
// returning a plausible but wrong page. It is not a signature and not a
// secret: it protects against mistakes, not against tampering.
func fingerprint(keys []sortKey) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.qualified())
		if k.desc {
			b.WriteString(":desc,")
		} else {
			b.WriteString(":asc,")
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = `"` + s + `"`
	}
	return strings.Join(quoted, ", ")
}
