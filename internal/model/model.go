package model

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Parse returns the GORM schema for dest. GORM caches parsed schemas on the
// connection, so this is a map lookup after the first call and needs no cache
// of our own.
func Parse(db *gorm.DB, dest any) (*schema.Schema, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(dest); err != nil {
		return nil, fmt.Errorf("parsing model schema: %w", err)
	}
	return stmt.Schema, nil
}

// Field returns the field stored in the given database column, or nil.
func Field(s *schema.Schema, column string) *schema.Field {
	return s.FieldsByDBName[column]
}

// Value reads a column's value out of a row.
func Value(ctx context.Context, f *schema.Field, row reflect.Value) any {
	v, _ := f.ValueOf(ctx, row)
	return v
}

// Nullability is what we can tell about a column without asking the database.
type Nullability int

const (
	// NotNullable means the Go type cannot hold NULL. Scanning a NULL into it
	// would fail in database/sql before it ever reached us, so the column is
	// NOT NULL in every schema this model can actually be used against.
	NotNullable Nullability = iota
	// NullablePointer means the field is a pointer and can hold NULL.
	NullablePointer
	// NullableWrapper means the field is a sql.NullXxx-shaped value.
	NullableWrapper
)

func (n Nullability) String() string {
	switch n {
	case NullablePointer:
		return "a pointer"
	case NullableWrapper:
		return "a nullable wrapper type"
	default:
		return "not nullable"
	}
}

var valuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// NullabilityOf classifies a field by its Go type.
//
// GORM only sets Field.NotNull from an explicit `not null` struct tag, so
// absence of the tag says nothing. The Go type says more: a non-pointer,
// non-wrapper field cannot receive a NULL from a scan at all, so a model that
// declares one is already asserting the column is NOT NULL.
func NullabilityOf(f *schema.Field) Nullability {
	if f.FieldType.Kind() == reflect.Ptr {
		return NullablePointer
	}
	if f.FieldType.Kind() == reflect.Struct &&
		reflect.PointerTo(f.FieldType).Implements(valuerType) {
		if valid, ok := f.FieldType.FieldByName("Valid"); ok && valid.Type.Kind() == reflect.Bool {
			return NullableWrapper
		}
	}
	return NotNullable
}

// ColumnInfo is what the live database reports about a column.
type ColumnInfo struct {
	Exists   bool
	Nullable bool
	// NullableKnown is false when the driver cannot report nullability, in
	// which case Nullable must not be trusted.
	NullableKnown bool
	Unique        bool
	UniqueKnown   bool
	PrimaryKey    bool
}

// Describe asks the database about the columns of dest's table. It is meant
// for a boot-time check, not for the request path.
func Describe(db *gorm.DB, dest any) (map[string]ColumnInfo, error) {
	types, err := db.Migrator().ColumnTypes(dest)
	if err != nil {
		return nil, fmt.Errorf("reading column types: %w", err)
	}

	out := make(map[string]ColumnInfo, len(types))
	for _, ct := range types {
		info := ColumnInfo{Exists: true}
		info.Nullable, info.NullableKnown = ct.Nullable()
		info.Unique, info.UniqueKnown = ct.Unique()
		info.PrimaryKey, _ = ct.PrimaryKey()
		out[ct.Name()] = info
	}
	return out, nil
}
