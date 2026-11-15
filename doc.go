// Package jsonwire marshals and unmarshals protocol buffer messages in the
// ProtoJSON format through encoding/json/jsontext.
//
// The format is the same as the format of
// google.golang.org/protobuf/encoding/protojson. The difference is the I/O:
// jsonwire reads from a [jsontext.Decoder] and writes to a [jsontext.Encoder].
// This lets a message stream inside a larger JSON value that encoding/json/v2
// handles, without an intermediate []byte.
//
// The API follows the proposal in https://github.com/golang/protobuf/issues/1673:
//
//	MarshalEncode(*jsontext.Encoder, proto.Message) error
//	MarshalWrite(io.Writer, proto.Message) error
//	UnmarshalDecode(*jsontext.Decoder, proto.Message) error
//	UnmarshalRead(io.Reader, proto.Message) error
//
// Marshal and Unmarshal work on []byte, the same as protojson.
//
// # Use with encoding/json
//
// To use jsonwire from json.Marshal and json.Unmarshal, implement the
// json.MarshalerTo and json.UnmarshalerFrom interfaces on a message type:
//
//	func (x *MyMessage) MarshalJSONTo(enc *jsontext.Encoder) error {
//		return jsonwire.MarshalEncode(enc, x)
//	}
//
//	func (x *MyMessage) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
//		return jsonwire.UnmarshalDecode(dec, x)
//	}
//
// encoding/json v1 also calls these methods.
//
// # Differences from protojson
//
// The ProtoJSON format is the same, and the conformance suite gives the same
// results. The main differences:
//
//   - With MarshalEncode and UnmarshalDecode, the options of the jsontext
//     Encoder or Decoder control the JSON syntax, for example indentation and
//     duplicate object names. Strings must always be valid UTF-8, even if the
//     Encoder or Decoder allows invalid UTF-8.
//   - Only valid JSON is accepted. protojson accepts some invalid numbers,
//     for example 0E.
//   - The output is compact and deterministic, without random white space.
//   - JSON nesting is limited to 10000 levels for Marshal and Unmarshal, also
//     with a larger RecursionLimit. Lists and maps count as levels.
//
// For the complete list, see
// https://github.com/protolayer/jsonwire/blob/main/docs/compatibility.md.
//
// This package requires Go 1.27 or newer.
package jsonwire
