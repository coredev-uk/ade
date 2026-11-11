package gitlab

import (
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadTail(t *testing.T) {
	big := strings.Repeat("0123456789", 20_000) // spans several read chunks
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"whole when n is 0", "abc", 0, "abc"},
		{"shorter than n", "abc", 10, "abc"},
		{"tail", "abcdef", 4, "cdef"},
		{"tail of a stream larger than the buffer", big, 15, big[len(big)-15:]},
		{"starts on a character boundary", "ab€cd", 4, "cd"}, // 4 bytes = 2 of € + "cd"
		{"empty", "", 5, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// OneByteReader also covers short reads.
			got, err := readTail(iotest.OneByteReader(strings.NewReader(tc.in)), tc.n)
			if err != nil || got != tc.want {
				t.Errorf("readTail = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReadyTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Draft: Add x":          "Add x",
		"draft:Add x":           "Add x",
		"[Draft] Add x":         "Add x",
		"(DRAFT) Add x":         "Add x",
		"Draft - Add x":         "Add x",
		"Draft: [Draft] Add x":  "Add x",
		"Add x":                 "Add x",
		"Drafting the schema":   "Drafting the schema",
		"Reword the Draft docs": "Reword the Draft docs",
	} {
		if got := readyTitle(in); got != want {
			t.Errorf("readyTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
