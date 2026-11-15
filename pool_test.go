package jsonwire

import (
	"bytes"
	"encoding/json/jsontext"
	"testing"
)

// TestPutEncodeStateBuffer checks the buffer policy: a large buffer stays while
// it is well used, and goes after 5 poorly used calls in a row.
func TestReleaseBuffer(t *testing.T) {
	// The state does not come from the pool, so that other tests cannot use
	// it at the same time.
	state := newTestEncodeState(t)
	use := func(n int) {
		t.Helper()
		state.buf.Reset()
		state.buf.Write(bytes.Repeat([]byte{'x'}, n))
		state.release()
		state.buf.Reset()
	}
	use(1 << 20)
	large := state.buf.Cap()
	if large < 1<<20 {
		t.Fatalf("buffer capacity %d after a 1 MB output", large)
	}
	// A well-used buffer stays.
	use(1 << 19)
	if state.buf.Cap() != large {
		t.Fatal("a buffer used to a half was dropped")
	}
	// 4 poorly used calls in a row keep the buffer. The 5th drops it.
	for i := range 4 {
		use(100)
		if state.buf.Cap() != large {
			t.Fatalf("buffer dropped after %d poorly used calls", i+1)
		}
	}
	use(100)
	if state.buf.Cap() != 0 {
		t.Fatalf("buffer capacity %d after 5 poorly used calls, want 0", state.buf.Cap())
	}
	// A small buffer always stays.
	for range 10 {
		use(10)
	}
	if state.buf.Cap() == 0 {
		t.Fatal("small buffer was dropped")
	}
}

// TestPutEncodeStateEncoder checks that the encoder, which keeps a scratch
// buffer as large as its largest value, is dropped after a large value.
func TestReleaseEncoder(t *testing.T) {
	// The state does not come from the pool, so that other tests cannot use
	// it at the same time.
	state := newTestEncodeState(t)
	state.scratch.maxValue = maxPooledValue
	state.release()
	if state.enc == nil {
		t.Fatal("encoder dropped after a small value")
	}
	state.scratch.maxValue = maxPooledValue + 1
	state.release()
	if state.enc != nil {
		t.Fatal("encoder kept after a large value")
	}
	if state.scratch.maxValue != 0 {
		t.Fatal("maxValue not reset")
	}
}

func newTestEncodeState(t *testing.T) *encodeState {
	t.Helper()
	opts, err := MarshalOptions{}.encoderOptions()
	if err != nil {
		t.Fatal(err)
	}
	state := &encodeState{scratch: newEncodeScratch()}
	state.enc = jsontext.NewEncoder(&state.buf, opts)
	return state
}
