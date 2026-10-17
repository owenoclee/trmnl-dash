package main

import (
	"strings"
	"testing"
)

func tgryBytes(w, h int, focus [4]int) []byte {
	b := make([]byte, tgryHeader+w*h)
	copy(b, "TGRY\x01")
	put := func(off, v int) { b[off], b[off+1] = byte(v), byte(v>>8) }
	for i, v := range []int{w, h, focus[0], focus[1], focus[2], focus[3]} {
		put(8+2*i, v)
	}
	return b
}

func TestParseTGRY(t *testing.T) {
	good := tgryBytes(100, 50, [4]int{10, 5, 60, 30})
	got, err := parseTGRY(good)
	if err != nil || got.W != 100 || got.H != 50 || got.Focus != [4]int{10, 5, 60, 30} || len(got.Pix) != 5000 {
		t.Fatalf("good file: %+v, %v", got, err)
	}

	bumped := func(off int, v byte) []byte {
		b := append([]byte(nil), good...)
		b[off] = v
		return b
	}
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"short":         {good[:10], "too short"},
		"magic":         {bumped(0, 'J'), "header"},
		"version":       {bumped(4, 2), "header"},
		"reserved":      {bumped(6, 1), "header"},
		"truncated":     {good[:len(good)-1], "length"},
		"trailing":      {append(append([]byte(nil), good...), 0), "length"},
		"too many px":   {tgryBytes(4096, 4096, [4]int{0, 0, 16, 16})[:tgryHeader], "dimensions"},
		"tiny":          {tgryBytes(8, 8, [4]int{0, 0, 8, 8}), "dimensions"},
		"focus outside": {tgryBytes(100, 50, [4]int{50, 0, 60, 30}), "focus"},
		"focus tiny":    {tgryBytes(100, 50, [4]int{0, 0, 10, 30}), "focus"},
	} {
		if _, err := parseTGRY(tc.b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
