package paginate

import (
	"gorm.io/gorm"

	"github.com/wimwenigerkind/paginate/internal/model"
)

// Limits applied when Config leaves them at zero.
const (
	DefaultLimit    = 25
	DefaultMaxLimit = 100
)

// Config describes one paginated resource.
type Config struct {
	// Allowlist declares the sort orders clients may ask for. Required.
	Allowlist *Allowlist
	// DefaultLimit is used when a request carries no limit. Zero means
	// DefaultLimit.
	DefaultLimit int
	// MaxLimit is the largest limit a request may ask for. Requests above it
	// are refused rather than quietly clamped, because silently returning
	// fewer rows than asked for makes a client's paging arithmetic wrong in a
	// way that is hard to notice. Zero means DefaultMaxLimit.
	MaxLimit int
}

// Paginator holds the paging policy for one resource. Build it once at wiring
// time and keep it on the handler; it is immutable and safe for concurrent
// use.
type Paginator struct {
	allow        *Allowlist
	defaultLimit int
	maxLimit     int
}

// New validates cfg and returns a Paginator.
func New(cfg Config) (*Paginator, error) {
	if cfg.Allowlist == nil {
		return nil, errf(CodeConfig, "", "Config needs an Allowlist")
	}
	if cfg.DefaultLimit < 0 {
		return nil, errf(CodeConfig, "", "DefaultLimit must not be negative, got %d", cfg.DefaultLimit)
	}
	if cfg.MaxLimit < 0 {
		return nil, errf(CodeConfig, "", "MaxLimit must not be negative, got %d", cfg.MaxLimit)
	}

	p := &Paginator{
		allow:        cfg.Allowlist,
		defaultLimit: cfg.DefaultLimit,
		maxLimit:     cfg.MaxLimit,
	}
	if p.defaultLimit == 0 {
		p.defaultLimit = DefaultLimit
	}
	if p.maxLimit == 0 {
		p.maxLimit = DefaultMaxLimit
	}
	if p.defaultLimit > p.maxLimit {
		return nil, errf(CodeConfig, "",
			"DefaultLimit %d is larger than MaxLimit %d", p.defaultLimit, p.maxLimit)
	}
	return p, nil
}

// Allowlist returns the sort allowlist, for documenting an endpoint.
func (p *Paginator) Allowlist() *Allowlist { return p.allow }

func (p *Paginator) resolveLimit(requested int) (int, error) {
	switch {
	case requested < 0:
		// The value is not echoed: a request whose limit could not be parsed
		// at all arrives here as a sentinel, and repeating it back would
		// describe our binding rather than what the client sent.
		return 0, errf(CodeInvalidLimit, "limit", "limit must be a positive number")
	case requested == 0:
		return p.defaultLimit, nil
	case requested > p.maxLimit:
		return 0, errf(CodeLimitTooLarge, "limit", "limit must be at most %d, got %d", p.maxLimit, requested)
	default:
		return requested, nil
	}
}

// Validate checks the allowlist against T as the database actually defines it:
// that every allowlisted column exists, that none of them is nullable, and
// that the tiebreak column is unique.
//
// Call it once at wiring time. The cheaper checks also run on every request,
// but they can only read the Go types; this one asks the database, and it is
// the only way to catch a tiebreak that is not actually unique or a column
// that is nullable despite a non-pointer field.
func Validate[T any](db *gorm.DB, p *Paginator) error {
	var dest T

	columns, err := model.Describe(db, &dest)
	if err != nil {
		return wrapf(CodeConfig, "", err, "cannot describe the table for %T", dest)
	}

	// Check every sort the allowlist offers, not just the default, so that a
	// broken sort is found at boot rather than when a client first asks for it.
	for _, token := range p.allow.Tokens() {
		for _, prefix := range []string{"", "-"} {
			keys, err := p.allow.resolve(prefix + token)
			if err != nil {
				return err
			}
			bound, err := bind(db, keys, &dest, true)
			if err != nil {
				return err
			}
			if err := validateColumns(bound, columns, p.allow.tiebreak); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateColumns(keys []resolvedKey, columns map[string]model.ColumnInfo, tiebreak Column) error {
	for _, k := range keys {
		info, ok := columns[k.Name]
		if !ok {
			// A qualified column may legitimately live on a joined table that
			// this describe call did not cover. The bind step already proved
			// the model carries the value, which is what paging needs.
			if k.Table != "" {
				continue
			}
			return errf(CodeUnsortableField, "",
				"sort column %q does not exist in the database", k.Name)
		}
		if info.NullableKnown && info.Nullable {
			return errf(CodeUnsortableField, "",
				"sort column %q is nullable in the database; a NULL sort key silently drops rows "+
					"from every page. Make the column NOT NULL, or sort on something else",
				k.Name)
		}
		if k.Name == tiebreak.Name && k.Table == tiebreak.Table {
			if !info.PrimaryKey && info.UniqueKnown && !info.Unique {
				return errf(CodeUnsortableField, "",
					"tiebreak column %q is neither a primary key nor unique; rows sharing a sort "+
						"value would have no defined order, so paging through them would skip and repeat rows",
					k.Name)
			}
		}
	}
	return nil
}
