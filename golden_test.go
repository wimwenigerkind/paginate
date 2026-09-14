package paginate

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// Clients hold cursors across deploys, so the cursor wire format is part of
// the public API. These fixtures were captured when the format was frozen; if
// this test fails, the format changed and the change needs a version bump and
// dual-read support, not a regenerated fixture.
func TestGoldenCursorsStillDecode(t *testing.T) {
	raw, err := os.ReadFile("testdata/cursors.json")
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}

	var file struct {
		Cursors []struct {
			Name      string    `json:"name"`
			Cursor    string    `json:"cursor"`
			Direction direction `json:"direction"`
			Values    struct {
				CreatedAt time.Time `json:"created_at"`
				ID        int64     `json:"id"`
			} `json:"values"`
		} `json:"cursors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing fixtures: %v", err)
	}
	if len(file.Cursors) == 0 {
		t.Fatal("no fixtures found")
	}

	for _, tc := range file.Cursors {
		t.Run(tc.Name, func(t *testing.T) {
			dir, fields, err := decodeCursor(tc.Cursor, testKeys)
			if err != nil {
				t.Fatalf("a previously issued cursor no longer decodes: %v", err)
			}
			if dir != tc.Direction {
				t.Errorf("direction = %q, want %q", dir, tc.Direction)
			}

			gotAt, err := decodeValue(fields["created_at"], reflect.TypeOf(time.Time{}))
			if err != nil {
				t.Fatalf("decodeValue(created_at): %v", err)
			}
			if !gotAt.(time.Time).Equal(tc.Values.CreatedAt) {
				t.Errorf("created_at = %v, want %v", gotAt, tc.Values.CreatedAt)
			}

			gotID, err := decodeValue(fields["id"], reflect.TypeOf(int64(0)))
			if err != nil {
				t.Fatalf("decodeValue(id): %v", err)
			}
			if gotID.(int64) != tc.Values.ID {
				t.Errorf("id = %v, want %v", gotID, tc.Values.ID)
			}
		})
	}
}

// The fixtures above hardcode a fingerprint. If the fingerprint algorithm
// changes, every outstanding cursor is invalidated, so pin it explicitly
// rather than letting the golden test fail with a confusing message.
func TestFingerprintIsPinned(t *testing.T) {
	const want = "KQBb0ZEL"
	if got := fingerprint(testKeys); got != want {
		t.Errorf("fingerprint(created_at desc, id desc) = %q, want %q: "+
			"changing this invalidates every cursor clients currently hold", got, want)
	}
}
