package paginate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testKeys = []sortKey{
	{Column: Column{Name: "created_at"}, desc: true},
	{Column: Column{Name: "id"}, desc: true},
}

func TestCursorRoundTrip(t *testing.T) {
	ts := time.Date(2024, 1, 1, 10, 0, 0, 123456000, time.UTC)

	tests := []struct {
		name   string
		values map[string]any
	}{
		{
			name:   "timestamp and int",
			values: map[string]any{"created_at": ts, "id": int64(4711)},
		},
		{
			name: "int64 beyond float64 precision",
			// 2^62+1 survives only because values decode into the field's own
			// type rather than into any.
			values: map[string]any{"created_at": ts, "id": int64(1)<<62 + 1},
		},
		{
			name:   "negative int",
			values: map[string]any{"created_at": ts, "id": int64(-9223372036854775808)},
		},
		{
			name:   "string key",
			values: map[string]any{"created_at": "2024-01-01", "id": int64(1)},
		},
		{
			name:   "uuid-shaped string",
			values: map[string]any{"created_at": ts, "id": "3f2b8c1e-0000-4a0b-9c3d-000000000001"},
		},
		{
			name:   "empty string",
			values: map[string]any{"created_at": "", "id": int64(0)},
		},
		{
			name:   "string needing escapes",
			values: map[string]any{"created_at": `he said "hi"/\` + "\n", "id": int64(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := encodeCursor(testKeys, forward, tt.values)
			if err != nil {
				t.Fatalf("encodeCursor(): %v", err)
			}
			if strings.ContainsAny(enc, "+/=") {
				t.Errorf("cursor %q contains characters that are unsafe in a query string", enc)
			}

			dir, fields, err := decodeCursor(enc, testKeys)
			if err != nil {
				t.Fatalf("decodeCursor(): %v", err)
			}
			if dir != forward {
				t.Errorf("direction = %q, want %q", dir, forward)
			}

			for key, want := range tt.values {
				got, err := decodeValue(fields[key], reflect.TypeOf(want))
				if err != nil {
					t.Fatalf("decodeValue(%q): %v", key, err)
				}
				if wantTime, ok := want.(time.Time); ok {
					if !got.(time.Time).Equal(wantTime) {
						t.Errorf("%s = %v, want %v", key, got, wantTime)
					}
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func TestCursorPreservesInt64Precision(t *testing.T) {
	const big = int64(1)<<62 + 1
	if int64(float64(big)) == big {
		t.Skip("float64 represents this value exactly; the test would prove nothing")
	}

	enc, err := encodeCursor(testKeys, forward, map[string]any{"created_at": "x", "id": big})
	if err != nil {
		t.Fatalf("encodeCursor(): %v", err)
	}
	_, fields, err := decodeCursor(enc, testKeys)
	if err != nil {
		t.Fatalf("decodeCursor(): %v", err)
	}

	typed, err := decodeValue(fields["id"], reflect.TypeOf(int64(0)))
	if err != nil {
		t.Fatalf("decodeValue(): %v", err)
	}
	if typed.(int64) != big {
		t.Errorf("decoding into int64 = %d, want %d", typed, big)
	}

	// The same bytes read into `any` round to the nearest float64. That is the
	// bug decodeValue exists to avoid, so assert it still bites.
	var loose any
	if err := json.Unmarshal(fields["id"], &loose); err != nil {
		t.Fatalf("unmarshalling into any: %v", err)
	}
	if got := int64(loose.(float64)); got == big {
		t.Errorf("decoding into any preserved %d; decodeValue is no longer load-bearing", big)
	}
}

func TestCursorDirectionRoundTrip(t *testing.T) {
	for _, dir := range []direction{forward, backward} {
		enc, err := encodeCursor(testKeys, dir, map[string]any{"created_at": "x", "id": int64(1)})
		if err != nil {
			t.Fatalf("encodeCursor(): %v", err)
		}
		got, _, err := decodeCursor(enc, testKeys)
		if err != nil {
			t.Fatalf("decodeCursor(): %v", err)
		}
		if got != dir {
			t.Errorf("direction = %q, want %q", got, dir)
		}
	}
}

// encodeRaw builds a cursor from an arbitrary payload, to construct inputs the
// encoder would never produce.
func encodeRaw(t *testing.T, p cursorPayload) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshalling test payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func validPayload() cursorPayload {
	return cursorPayload{
		V: cursorVersion,
		K: fingerprint(testKeys),
		D: forward,
		F: map[string]json.RawMessage{
			"created_at": json.RawMessage(`"2024-01-01T10:00:00Z"`),
			"id":         json.RawMessage(`4711`),
		},
	}
}

func TestDecodeCursorRejects(t *testing.T) {
	tests := []struct {
		name    string
		cursor  func(t *testing.T) string
		wantErr error
	}{
		{
			name:    "not base64",
			cursor:  func(*testing.T) string { return "!!!not base64!!!" },
			wantErr: ErrInvalidCursor,
		},
		{
			name: "standard base64 with padding",
			cursor: func(*testing.T) string {
				b, _ := json.Marshal(validPayload())
				return base64.StdEncoding.EncodeToString(b)
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "truncated",
			cursor: func(t *testing.T) string {
				full := encodeRaw(t, validPayload())
				return full[:len(full)/2]
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "valid base64, not JSON",
			cursor: func(*testing.T) string {
				return base64.RawURLEncoding.EncodeToString([]byte("definitely not json"))
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "unknown format version",
			cursor: func(t *testing.T) string {
				p := validPayload()
				p.V = cursorVersion + 1
				return encodeRaw(t, p)
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "missing version",
			cursor: func(t *testing.T) string {
				p := validPayload()
				p.V = 0
				return encodeRaw(t, p)
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "unknown direction",
			cursor: func(t *testing.T) string {
				p := validPayload()
				p.D = "sideways"
				return encodeRaw(t, p)
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "missing a sort value",
			cursor: func(t *testing.T) string {
				p := validPayload()
				delete(p.F, "id")
				return encodeRaw(t, p)
			},
			wantErr: ErrInvalidCursor,
		},
		{
			name: "issued for a different sort order",
			cursor: func(t *testing.T) string {
				other := []sortKey{
					{Column: Column{Name: "created_at"}, desc: false},
					{Column: Column{Name: "id"}, desc: false},
				}
				enc, err := encodeCursor(other, forward, map[string]any{"created_at": "x", "id": int64(1)})
				if err != nil {
					t.Fatalf("encodeCursor(): %v", err)
				}
				return enc
			},
			wantErr: ErrCursorMismatch,
		},
		{
			name: "oversized",
			cursor: func(*testing.T) string {
				return strings.Repeat("A", maxCursorBytes+1)
			},
			wantErr: ErrInvalidCursor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := decodeCursor(tt.cursor(t), testKeys)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("decodeCursor() error = %v, want %v", err, tt.wantErr)
			}
			var perr *Error
			if errors.As(err, &perr) && perr.Param != "cursor" {
				t.Errorf("error Param = %q, want %q", perr.Param, "cursor")
			}
		})
	}
}

// A cursor must survive the payload gaining fields it did not know about, in
// both directions: an old cursor read by new code, and a new cursor read by
// code that predates the addition.
func TestCursorToleratesUnknownMembers(t *testing.T) {
	t.Run("unknown members are ignored", func(t *testing.T) {
		b, err := json.Marshal(validPayload())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(b, &generic); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		generic["issued_at"] = json.RawMessage(`"2026-09-14T00:00:00Z"`)
		generic["tenant"] = json.RawMessage(`"acme"`)
		withExtras, _ := json.Marshal(generic)

		if _, _, err := decodeCursor(base64.RawURLEncoding.EncodeToString(withExtras), testKeys); err != nil {
			t.Errorf("decodeCursor() rejected a cursor carrying unknown members: %v", err)
		}
	})

	t.Run("unknown sort values are ignored", func(t *testing.T) {
		p := validPayload()
		p.F["some_future_column"] = json.RawMessage(`"whatever"`)
		if _, _, err := decodeCursor(encodeRaw(t, p), testKeys); err != nil {
			t.Errorf("decodeCursor() rejected a cursor carrying an extra sort value: %v", err)
		}
	})
}

func TestDecodeValueRejectsWrongType(t *testing.T) {
	if _, err := decodeValue(json.RawMessage(`"not a number"`), reflect.TypeOf(int64(0))); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("decodeValue() error = %v, want %v", err, ErrInvalidCursor)
	}
}

func TestEncodeCursorRejectsUnencodableValue(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
	}{
		{
			name:   "missing value for a sort column",
			values: map[string]any{"created_at": "x"},
		},
		{
			name:   "value that cannot be marshalled",
			values: map[string]any{"created_at": math.NaN(), "id": int64(1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := encodeCursor(testKeys, forward, tt.values); !errors.Is(err, ErrUnsortableField) {
				t.Errorf("encodeCursor() error = %v, want %v", err, ErrUnsortableField)
			}
		})
	}
}
