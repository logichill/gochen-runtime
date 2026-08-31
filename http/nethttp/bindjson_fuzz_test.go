package nethttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzBindJSONStrict(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"a":1,"b":"x"}`),
		[]byte(`{"a":1,"unknown":2}`),       // unknown field
		[]byte(`{"a":1}{"a":2}`),            // trailing data (multi json)
		[]byte(`{"a":1}   `),                // ok (trailing whitespace)
		[]byte(`null`),                      // invalid for struct
		[]byte(`[]`),                        // invalid for struct
		[]byte(`{"a":9223372036854775808}`), // out of range int
	}
	for _, s := range seeds {
		f.Add(s)
	}

	type Payload struct {
		A int    `json:"a"`
		B string `json:"b"`
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		// Keep inputs bounded. BindJSON reads the full body (with a 10MB default limit),
		// but fuzzing should stay cheap.
		if len(body) > 1<<20 { // 1 MiB
			return
		}

		req := httptest.NewRequest(http.MethodPost, "http://example.com/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		ctx, err := NewBaseContext(rec, req)
		if err != nil {
			// Treat context construction issues as non-actionable fuzz noise.
			return
		}

		var p1 Payload
		err1 := ctx.BindJSON(&p1)

		// BindJSON caches body; a second bind should be deterministic.
		var p2 Payload
		err2 := ctx.BindJSON(&p2)

		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("BindJSON not deterministic: err1=%v err2=%v", err1, err2)
		}
		if err1 == nil && p1 != p2 {
			t.Fatalf("BindJSON inconsistent: p1=%+v p2=%+v", p1, p2)
		}
	})
}
