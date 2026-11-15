package jsonwire_test

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"bytes"
	"fmt"
	"github.com/protolayer/jsonwire"
	testpb "github.com/protolayer/jsonwire/internal/gen/google/protobuf"
	"github.com/protolayer/jsonwire/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
)

// benchMessages returns the benchmark messages in testdata/benchmark. They are
// TestAllTypesProto3 messages from
// proto/google/protobuf/test_messages_proto3.proto.
func benchMessages(b *testing.B) map[string]*testpb.TestAllTypesProto3 {
	b.Helper()
	messages := make(map[string]*testpb.TestAllTypesProto3)
	for _, name := range []string{"small", "large"} {
		data, err := os.ReadFile(filepath.Join("testdata", "benchmark", name+".json"))
		if err != nil {
			b.Fatal(err)
		}
		m := new(testpb.TestAllTypesProto3)
		if err := protojson.Unmarshal(data, m); err != nil {
			b.Fatalf("%s: %v", name, err)
		}
		messages[name] = m
	}
	return messages
}

// protojsonMessage connects a message to encoding/json the way generated code
// does today: MarshalJSON and UnmarshalJSON with protojson.
type protojsonMessage struct {
	msg *testpb.TestAllTypesProto3
}

func (p *protojsonMessage) MarshalJSON() ([]byte, error) { return protojson.Marshal(p.msg) }

func (p *protojsonMessage) UnmarshalJSON(b []byte) error {
	p.msg = new(testpb.TestAllTypesProto3)
	return protojson.Unmarshal(b, p.msg)
}

type protojsonContainer struct {
	ID      string            `json:"id"`
	Message *protojsonMessage `json:"message"`
}

// jsonwireMessage connects a message to encoding/json the way generated code
// does with jsonwire: MarshalJSONTo and UnmarshalJSONFrom. Unlike the test
// type streamMessage, it does not count calls, so that the benchmark measures
// only jsonwire.
type jsonwireMessage struct {
	msg *testpb.TestAllTypesProto3
}

func (s *jsonwireMessage) MarshalJSONTo(enc *jsontext.Encoder) error {
	return jsonwire.MarshalEncode(enc, s.msg)
}

func (s *jsonwireMessage) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	s.msg = new(testpb.TestAllTypesProto3)
	return jsonwire.UnmarshalDecode(dec, s.msg)
}

type jsonwireContainer struct {
	ID      string           `json:"id"`
	Message *jsonwireMessage `json:"message"`
}

func BenchmarkMarshal(b *testing.B) {
	for name, m := range benchMessages(b) {
		b.Run(name+"/protojson", func(b *testing.B) {
			for b.Loop() {
				if _, err := protojson.Marshal(m); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/jsonwire", func(b *testing.B) {
			for b.Loop() {
				if _, err := jsonwire.Marshal(m); err != nil {
					b.Fatal(err)
				}
			}
		})
		// The main use case: a message inside a larger value that
		// encoding/json/v2 marshals.
		b.Run(name+"/json.Marshal+protojson", func(b *testing.B) {
			v := protojsonContainer{ID: "x", Message: &protojsonMessage{msg: m}}
			for b.Loop() {
				if _, err := jsonv2.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/json.Marshal+jsonwire", func(b *testing.B) {
			v := jsonwireContainer{ID: "x", Message: &jsonwireMessage{msg: m}}
			for b.Loop() {
				if _, err := jsonv2.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkUnmarshal(b *testing.B) {
	for name, m := range benchMessages(b) {
		data, err := protojson.Marshal(m)
		if err != nil {
			b.Fatal(err)
		}
		data = testutil.Compact(b, data)
		wrapped, err := jsonv2.Marshal(jsonwireContainer{ID: "x", Message: &jsonwireMessage{msg: m}})
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name+"/protojson", func(b *testing.B) {
			for b.Loop() {
				var out testpb.TestAllTypesProto3
				if err := protojson.Unmarshal(data, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/jsonwire", func(b *testing.B) {
			for b.Loop() {
				var out testpb.TestAllTypesProto3
				if err := jsonwire.Unmarshal(data, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/json.Unmarshal+protojson", func(b *testing.B) {
			for b.Loop() {
				var out protojsonContainer
				if err := jsonv2.Unmarshal(wrapped, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/json.Unmarshal+jsonwire", func(b *testing.B) {
			for b.Loop() {
				var out jsonwireContainer
				if err := jsonv2.Unmarshal(wrapped, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Ensure that the benchmark types satisfy the json/v2 interfaces.
var (
	_ jsonv2.MarshalerTo     = (*jsonwireMessage)(nil)
	_ jsonv2.UnmarshalerFrom = (*jsonwireMessage)(nil)
)

// benchShapes returns messages of different shapes and sizes, so that the
// results do not depend on one message.
func benchShapes(b *testing.B) map[string]proto.Message {
	b.Helper()
	list := func(n int) *testpb.TestAllTypesProto3 {
		m := new(testpb.TestAllTypesProto3)
		ts := timestamppb.New(time.Date(2026, 11, 15, 12, 0, 0, 123456789, time.UTC))
		for i := range n {
			m.RepeatedNestedMessage = append(m.RepeatedNestedMessage, &testpb.TestAllTypesProto3_NestedMessage{
				A: int32(i),
				Corecursive: &testpb.TestAllTypesProto3{
					OptionalString:    fmt.Sprintf("user-%d@example.com", i),
					OptionalInt64:     int64(i) * 1000003,
					OptionalTimestamp: ts,
					OptionalBool:      i%2 == 0,
					RepeatedString:    []string{"tag-a", "tag-b"},
				},
			})
		}
		return m
	}
	strs := new(testpb.TestAllTypesProto3)
	for i := range 500 {
		strs.RepeatedString = append(strs.RepeatedString, strings.Repeat("abcdefghij", 5)+fmt.Sprint(i)+"\"\\\n\u00e9")
	}
	nums := new(testpb.TestAllTypesProto3)
	for i := range 2000 {
		nums.PackedDouble = append(nums.PackedDouble, float64(i)*1.0000001)
		nums.PackedInt64 = append(nums.PackedInt64, int64(i)*99991)
		nums.PackedInt32 = append(nums.PackedInt32, int32(i))
	}
	blob := func(n int) *testpb.TestAllTypesProto3 {
		return &testpb.TestAllTypesProto3{OptionalBytes: bytes.Repeat([]byte{1, 2, 3, 250}, n/4)}
	}
	large := benchMessages(b)["large"]
	dynLarge := dynamicpb.NewMessage(large.ProtoReflect().Descriptor())
	proto.Merge(dynLarge, large)
	return map[string]proto.Message{
		"list20":       list(20),
		"list5000":     list(5000),
		"strings":      strs,
		"numbers":      nums,
		"bytes64KB":    blob(64 << 10),
		"bytes600KB":   blob(600 << 10),
		"bytes2MB":     blob(2 << 20),
		"dynamicLarge": dynLarge,
	}
}

// BenchmarkShapes compares protojson and jsonwire for each shape. Run it with
// -cpu=1. The Parallel variants use all CPUs; run them without -cpu.
func BenchmarkShapes(b *testing.B) {
	for name, m := range benchShapes(b) {
		data, err := protojson.Marshal(m)
		if err != nil {
			b.Fatal(err)
		}
		data = testutil.Compact(b, data)
		newMessage := func() proto.Message { return m.ProtoReflect().New().Interface() }
		impls := []struct {
			name      string
			marshal   func(proto.Message) ([]byte, error)
			unmarshal func([]byte, proto.Message) error
		}{
			{"protojson", protojson.Marshal, protojson.Unmarshal},
			{"jsonwire", jsonwire.Marshal, jsonwire.Unmarshal},
		}
		for _, impl := range impls {
			b.Run(name+"/Marshal/"+impl.name, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if _, err := impl.marshal(m); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(name+"/Unmarshal/"+impl.name, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if err := impl.unmarshal(data, newMessage()); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(name+"/ParallelMarshal/"+impl.name, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if _, err := impl.marshal(m); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
			b.Run(name+"/ParallelUnmarshal/"+impl.name, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if err := impl.unmarshal(data, newMessage()); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}
