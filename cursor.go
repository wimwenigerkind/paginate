package paginate

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
)

// cursorVersion is the wire format version carried in every cursor. Bumping it
// is a breaking change for clients holding cursors, so a bump must ship with
// support for reading the previous version.
const cursorVersion = 1

// maxCursorBytes caps how much base64 we are willing to decode. Cursors this
// package issues are well under 200 bytes; the cap only exists so that a
// hostile request cannot make us allocate.
const maxCursorBytes = 4096

// direction records which way a cursor pages. It lives inside the cursor so
// that next_cursor and prev_cursor are interchangeable to a client: both go
// back as ?cursor=, and neither needs a second parameter to say what it means.
type direction string

const (
	forward  direction = "f"
	backward direction = "b"
)

func (d direction) valid() bool { return d == forward || d == backward }

func (d direction) flipped() direction {
	if d == forward {
		return backward
	}
	return forward
}

// cursorPayload is the JSON inside a cursor. Fields are keyed by column, not
// positional, so that adding a key later leaves already-issued cursors
// decodable, and so that a value can never be silently read as belonging to a
// different column.
//
// Decoding ignores unknown members, which is what lets a future version add
// metadata without invalidating outstanding cursors.
type cursorPayload struct {
	V int                        `json:"v"`
	K string                     `json:"k"`
	D direction                  `json:"d"`
	F map[string]json.RawMessage `json:"f"`
}

// encodeCursor renders the boundary row's sort values as an opaque cursor.
// values is keyed by Column.qualified().
func encodeCursor(keys []sortKey, dir direction, values map[string]any) (string, error) {
	fields := make(map[string]json.RawMessage, len(keys))
	for _, k := range keys {
		v, ok := values[k.qualified()]
		if !ok {
			return "", errf(CodeUnsortableField, "",
				"no value for sort column %q on the row being encoded", k.qualified())
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return "", wrapf(CodeUnsortableField, "", err,
				"sort column %q holds a value that cannot be put in a cursor", k.qualified())
		}
		fields[k.qualified()] = raw
	}

	body, err := json.Marshal(cursorPayload{
		V: cursorVersion,
		K: fingerprint(keys),
		D: dir,
		F: fields,
	})
	if err != nil {
		return "", wrapf(CodeUnsortableField, "", err, "encoding cursor")
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

// decodeCursor parses a cursor and checks it belongs to the given sort order.
// It returns the direction the cursor pages in and the still-encoded boundary
// values, which are decoded later against the model's own field types.
func decodeCursor(s string, keys []sortKey) (direction, map[string]json.RawMessage, error) {
	if len(s) > maxCursorBytes {
		return "", nil, errf(CodeInvalidCursor, "cursor", "cursor is too long")
	}

	body, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", nil, wrapf(CodeInvalidCursor, "cursor", err, "cursor is not valid base64url")
	}

	var p cursorPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", nil, wrapf(CodeInvalidCursor, "cursor", err, "cursor does not contain valid JSON")
	}
	if p.V != cursorVersion {
		return "", nil, errf(CodeInvalidCursor, "cursor",
			"cursor has format version %d, this server issues version %d", p.V, cursorVersion)
	}
	if !p.D.valid() {
		return "", nil, errf(CodeInvalidCursor, "cursor", "cursor has an unknown direction")
	}
	if p.K != fingerprint(keys) {
		return "", nil, errf(CodeCursorMismatch, "cursor",
			"cursor was issued for a different sort order; restart paging from the first page")
	}
	for _, k := range keys {
		if _, ok := p.F[k.qualified()]; !ok {
			return "", nil, errf(CodeInvalidCursor, "cursor",
				"cursor is missing a value for sort column %q", k.qualified())
		}
	}
	return p.D, p.F, nil
}

// decodeValue turns a cursor's stored JSON back into the concrete type of the
// struct field it came from.
//
// Decoding into `any` instead would be a quiet correctness bug: JSON has one
// number type, so an int64 past 2^53 would come back as a rounded float64 and
// the paging predicate would compare against the wrong value.
func decodeValue(raw json.RawMessage, t reflect.Type) (any, error) {
	ptr := reflect.New(t)
	if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
		return nil, wrapf(CodeInvalidCursor, "cursor", err,
			"cursor holds a value that is not a valid %s", t)
	}
	return ptr.Elem().Interface(), nil
}
