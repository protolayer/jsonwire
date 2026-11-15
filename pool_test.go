package jsonwire

import (
	"bytes"
	"encoding/json/jsontext"
	"runtime"
	"strings"
	"sync"
	"testing"
	"weak"

	"google.golang.org/protobuf/types/known/anypb"

	testpb "github.com/protolayer/jsonwire/internal/gen/google/protobuf"
	"github.com/protolayer/jsonwire/internal/testutil"
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
	// A well-used call resets the count of poorly used calls.
	use(1 << 20)
	large = state.buf.Cap()
	for range 3 {
		use(100)
	}
	use(1 << 19)
	for i := range 4 {
		use(100)
		if state.buf.Cap() != large {
			t.Fatalf("buffer dropped after a well-used call and %d poorly used calls", i+1)
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

// TestMaxValue checks that a large bytes value drops the encoder, and a small
// one keeps it.
func TestMaxValue(t *testing.T) {
	for _, c := range []struct {
		size int
		keep bool
	}{
		{size: 1 << 10, keep: true},
		{size: maxPooledValue, keep: false},
	} {
		state := newTestEncodeState(t)
		// The int64 field makes the encoder use its scratch buffer again
		// after the bytes value.
		m := &testpb.TestAllTypesProto3{OptionalBytes: make([]byte, c.size), OptionalInt64: 1}
		if err := (MarshalOptions{}).marshalEncode(state.enc, m, state.scratch, false); err != nil {
			t.Fatal(err)
		}
		state.release()
		if (state.enc != nil) != c.keep {
			t.Errorf("bytes value of %d bytes: encoder kept is %v, want %v", c.size, state.enc != nil, c.keep)
		}
	}
}

// TestReleaseScratch checks that a large scratch stack does not go back into
// the pool.
func TestReleaseScratch(t *testing.T) {
	state := newTestEncodeState(t)
	state.scratch.fields = make([]fieldValue, 0, maxPooledEntries+1)
	state.release()
	if cap(state.scratch.fields) > maxPooledEntries {
		t.Error("encode state kept a large scratch stack")
	}

	// Not parallel: a Get right after a Put usually returns the same value.
	large := newEncodeScratch()
	large.entries = make([]mapEntry, 0, maxPooledEntries+1)
	putEncodeScratch(large)
	if got := encodeScratchPool.Get().(*encodeScratch); got == large {
		t.Error("putEncodeScratch pooled a large scratch stack")
	}
}

// TestIndentCache checks that only short indents go into the options cache.
func TestIndentCache(t *testing.T) {
	for _, n := range []int{maxCachedIndent, maxCachedIndent + 1} {
		indent := strings.Repeat("\t", n)
		if _, err := (MarshalOptions{Indent: indent}).encoderOptions(); err != nil {
			t.Fatal(err)
		}
		_, cached := multilineOptions.Load(indent)
		if want := n <= maxCachedIndent; cached != want {
			t.Errorf("indent of %d characters: cached is %v, want %v", n, cached, want)
		}
	}
}

// TestReleaseDecodeState checks that a pooled decoder does not keep the input
// of the caller alive.
func TestReleaseDecodeState(t *testing.T) {
	input := []byte(`{"optionalInt32":1}`)
	ref := weak.Make(&input[0])
	state := getDecodeState(input)
	if _, err := state.dec.ReadToken(); err != nil {
		t.Fatal(err)
	}
	state.release()
	input = nil
	runtime.GC()
	if ref.Value() != nil {
		t.Error("decode state keeps the input after release")
	}
	runtime.KeepAlive(state)
}

// TestSubDecoderPool checks that only sub-decoders that allow duplicate names
// go into the pool. A decoder that checks for duplicate names keeps the names
// it read, also after Reset.
func TestSubDecoderPool(t *testing.T) {
	// Not parallel: the test replaces the pool.
	defer func() { subDecoderPool = sync.Pool{} }()
	input := `{"@type":"type.googleapis.com/google.protobuf.Struct","value":{"` +
		strings.Repeat("k", 1<<20) + `":null}}`

	subDecoderPool = sync.Pool{}
	if err := UnmarshalDecode(jsontext.NewDecoder(strings.NewReader(input)), new(anypb.Any)); err != nil {
		t.Fatal(err)
	}
	if subDecoderPool.Get() != nil {
		t.Error("a sub-decoder that checks for duplicate names went into the pool")
	}

	// The race detector drops some values that go into a pool.
	if testutil.RaceEnabled {
		return
	}
	subDecoderPool = sync.Pool{}
	if err := Unmarshal([]byte(input), new(anypb.Any)); err != nil {
		t.Fatal(err)
	}
	if subDecoderPool.Get() == nil {
		t.Error("a sub-decoder that allows duplicate names did not go into the pool")
	}
}
