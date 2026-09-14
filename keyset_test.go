package paginate

import "testing"

func TestUniform(t *testing.T) {
	key := func(desc bool) resolvedKey {
		return resolvedKey{sortKey: sortKey{desc: desc}}
	}
	tests := []struct {
		name string
		keys []resolvedKey
		want bool
	}{
		{"single key", []resolvedKey{key(true)}, true},
		{"all descending", []resolvedKey{key(true), key(true)}, true},
		{"all ascending", []resolvedKey{key(false), key(false)}, true},
		{"mixed", []resolvedKey{key(true), key(false)}, false},
		{"mixed, differing late", []resolvedKey{key(true), key(true), key(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uniform(tt.keys); got != tt.want {
				t.Errorf("uniform() = %v, want %v", got, tt.want)
			}
		})
	}
}
