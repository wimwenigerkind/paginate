package model

import (
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils/tests"
)

// upperString is a custom column type that is a Valuer but cannot hold NULL.
type upperString string

func (u upperString) Value() (driver.Value, error) { return string(u), nil }

// nullableThing is Valuer-shaped and carries a Valid flag, the way the
// database/sql null wrappers do.
type nullableThing struct {
	Thing string
	Valid bool
}

func (n nullableThing) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Thing, nil
}

type row struct {
	ID           int64 `gorm:"primaryKey"`
	Title        string
	CreatedAt    time.Time
	Count        int
	PublishedAt  *time.Time
	OptionalName *string
	NullString   sql.NullString
	NullTime     sql.NullTime
	NullInt      sql.NullInt64
	Custom       upperString
	CustomNull   nullableThing
}

func TestNullabilityOf(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true, Logger: logger.Discard})
	if err != nil {
		t.Fatalf("opening dry-run database: %v", err)
	}
	schema, err := Parse(db, &row{})
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}

	tests := []struct {
		column string
		want   Nullability
	}{
		{"id", NotNullable},
		{"title", NotNullable},
		{"created_at", NotNullable},
		{"count", NotNullable},
		// A value type cannot receive a NULL from a scan at all, so declaring
		// one already asserts the column is NOT NULL.
		{"custom", NotNullable},

		{"published_at", NullablePointer},
		{"optional_name", NullablePointer},

		{"null_string", NullableWrapper},
		{"null_time", NullableWrapper},
		{"null_int", NullableWrapper},
		{"custom_null", NullableWrapper},
	}

	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			f := Field(schema, tt.column)
			if f == nil {
				t.Fatalf("column %q is not on the model", tt.column)
			}
			if got := NullabilityOf(f); got != tt.want {
				t.Errorf("NullabilityOf(%s) = %v, want %v", tt.column, got, tt.want)
			}
		})
	}
}

func TestFieldReturnsNilForAnUnknownColumn(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true, Logger: logger.Discard})
	if err != nil {
		t.Fatalf("opening dry-run database: %v", err)
	}
	schema, err := Parse(db, &row{})
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	if f := Field(schema, "does_not_exist"); f != nil {
		t.Errorf("Field() = %v, want nil", f)
	}
}

func TestParseRejectsANonStruct(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true, Logger: logger.Discard})
	if err != nil {
		t.Fatalf("opening dry-run database: %v", err)
	}
	if _, err := Parse(db, "not a model"); err == nil {
		t.Error("Parse() accepted a string as a model")
	}
}
