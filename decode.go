// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-protobuf-go file.
//
// This file is a port of google.golang.org/protobuf/encoding/protojson to
// encoding/json/jsontext.

package jsonwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// JSON token kinds, as returned by jsontext.Token.Kind and
// jsontext.Decoder.PeekKind.
const (
	kindNull        jsontext.Kind = 'n'
	kindFalse       jsontext.Kind = 'f'
	kindTrue        jsontext.Kind = 't'
	kindString      jsontext.Kind = '"'
	kindNumber      jsontext.Kind = '0'
	kindObjectOpen  jsontext.Kind = '{'
	kindObjectClose jsontext.Kind = '}'
	kindArrayOpen   jsontext.Kind = '['
	kindArrayClose  jsontext.Kind = ']'
)

// Unmarshal reads the given []byte into the given [proto.Message]. The
// provided message must be mutable (for example, a non-nil pointer to a
// message).
func Unmarshal(b []byte, m proto.Message) error {
	return UnmarshalOptions{}.Unmarshal(b, m)
}

// UnmarshalRead reads one JSON value from r into the given [proto.Message].
// It reads r until EOF, and it is an error if r contains more than one JSON
// value.
func UnmarshalRead(r io.Reader, m proto.Message) error {
	return UnmarshalOptions{}.UnmarshalRead(r, m)
}

// UnmarshalDecode reads the next JSON value from dec into the given
// [proto.Message].
func UnmarshalDecode(dec *jsontext.Decoder, m proto.Message) error {
	return UnmarshalOptions{}.UnmarshalDecode(dec, m)
}

// UnmarshalOptions is a configurable JSON format parser. The fields have the
// same meaning as the fields of protojson.UnmarshalOptions.
type UnmarshalOptions struct {
	_ noUnkeyedLiterals

	// AllowPartial accepts input for messages that will result in missing
	// required fields. If AllowPartial is false (the default), Unmarshal will
	// return error if there are any missing required fields.
	AllowPartial bool

	// DiscardUnknown specifies whether to ignore unknown fields when parsing.
	// An unknown field is any field whose field name or field number does not
	// resolve to any known or extension field in the message. By default,
	// unmarshal rejects unknown fields as an error.
	DiscardUnknown bool

	// Resolver is used for looking up types when unmarshaling
	// google.protobuf.Any messages or extension fields. If nil, this defaults
	// to using protoregistry.GlobalTypes.
	Resolver interface {
		protoregistry.MessageTypeResolver
		protoregistry.ExtensionTypeResolver
	}

	// RecursionLimit limits how deeply messages may be nested. If zero, a
	// default limit is applied.
	RecursionLimit int
}

// Unmarshal reads the given []byte and populates the given [proto.Message]
// using options in the UnmarshalOptions object. If Unmarshal returns an error,
// the given message may be partially set.
func (o UnmarshalOptions) Unmarshal(b []byte, m proto.Message) error {
	state := getDecodeState(b)
	defer putDecodeState(state)
	return o.unmarshalAll(state.dec, m)
}

// UnmarshalRead reads one JSON value from r and populates the given
// [proto.Message] using options in the UnmarshalOptions object. It reads r
// until EOF, and it is an error if r contains more than one JSON value.
//
// UnmarshalRead does not limit the size of the input. For input from an
// untrusted source, limit r, for example with [io.LimitReader]. Memory use
// grows with the input: each google.protobuf.Any value is read completely
// before it is decoded, because its "@type" field can come last.
func (o UnmarshalOptions) UnmarshalRead(r io.Reader, m proto.Message) error {
	return o.unmarshalAll(jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)), m)
}

// unmarshalAll reads exactly one JSON value from dec, followed by EOF.
func (o UnmarshalOptions) unmarshalAll(dec *jsontext.Decoder, m proto.Message) error {
	if err := o.UnmarshalDecode(dec, m); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty input. Do not return io.EOF, which callers can take as a
			// clean end of input.
			return errorf("%w", io.ErrUnexpectedEOF)
		}
		return err
	}
	switch _, err := dec.ReadToken(); {
	case err == io.EOF:
		return nil
	case err != nil:
		return wrapError(err)
	default:
		return errorf("unexpected data after top-level value at offset %d", dec.InputOffset())
	}
}

// UnmarshalDecode reads the next JSON value from dec and populates the given
// [proto.Message] using options in the UnmarshalOptions object. It does not
// read past the end of that value, so the caller can read more values from
// dec.
//
// The syntax options of dec apply, for example whether duplicate JSON object
// names are allowed. The ProtoJSON rules for string values always apply: a
// string that is not valid UTF-8 causes an error even if dec allows invalid
// UTF-8. Unmarshal and UnmarshalRead allow duplicate object names, which is
// the same as protojson. Duplicate fields and duplicate map keys are always an
// error. For the best performance, create dec with
// jsontext.AllowDuplicateNames(true).
func (o UnmarshalOptions) UnmarshalDecode(dec *jsontext.Decoder, m proto.Message) error {
	proto.Reset(m)

	if o.Resolver == nil {
		o.Resolver = protoregistry.GlobalTypes
	}
	if o.RecursionLimit == 0 {
		o.RecursionLimit = protowire.DefaultRecursionLimit
	}

	decOptions := dec.Options()
	allowInvalidUTF8, _ := jsonv2.GetOption(decOptions, jsontext.AllowInvalidUTF8)
	d := decoder{
		dec:        dec,
		opts:       o,
		checkUTF8:  allowInvalidUTF8,
		subOptions: subDecoderOptions[boolIndex(allowInvalidUTF8)],
	}
	if err := d.unmarshalMessage(m.ProtoReflect(), false); err != nil {
		return wrapError(err)
	}
	if o.AllowPartial {
		return nil
	}
	return proto.CheckInitialized(m)
}

// decodeState is a reusable decoder for Unmarshal.
//
// The decoder reads b through a bytes.Buffer, which jsontext parses in place
// without a copy. So decoder memory is the memory of b, and the pooled decoder
// keeps no large buffer.
type decodeState struct {
	dec *jsontext.Decoder
}

var (
	decodeStatePool sync.Pool
	emptyBuffer     = new(bytes.Buffer)
)

func getDecodeState(b []byte) *decodeState {
	state, _ := decodeStatePool.Get().(*decodeState)
	if state == nil {
		state = &decodeState{dec: jsontext.NewDecoder(bytes.NewBuffer(b), jsontext.AllowDuplicateNames(true))}
		return state
	}
	state.dec.Reset(bytes.NewBuffer(b), jsontext.AllowDuplicateNames(true))
	return state
}

func putDecodeState(state *decodeState) {
	state.release()
	decodeStatePool.Put(state)
}

// release drops the reference to the caller's input.
func (state *decodeState) release() {
	state.dec.Reset(emptyBuffer)
}

// decoder holds the state for one call. Methods use a value receiver, so that
// opts.RecursionLimit counts down for each nested message, the same as in
// protojson.
type decoder struct {
	dec  *jsontext.Decoder
	opts UnmarshalOptions
	// checkUTF8 is true when the jsontext.Decoder allows invalid UTF-8. Then
	// the decoder must check strings itself to keep the ProtoJSON rules.
	checkUTF8 bool
	// subOptions are the options for sub-decoders. They copy only the syntax
	// options that matter, not internal options of encoding/json.
	subOptions jsontext.Options
}

// subDecoderOptions has the sub-decoder options for AllowInvalidUTF8 false and
// true, so that a call does not allocate them.
//
// A sub-decoder reads a value that d.dec.ReadValue already read, and ReadValue
// checks the whole value for duplicate names if the options of d.dec require
// it. So sub-decoders always allow duplicate names. This also keeps pooled
// sub-decoders small: a decoder that checks for duplicate names keeps a copy
// of the names it read, also after Reset.
var subDecoderOptions = [2]jsontext.Options{
	jsonv2.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(false)),
	jsonv2.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)),
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// newError returns an error with the location of the decoder.
func (d decoder) newError(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if ptr := d.dec.StackPointer(); ptr != "" {
		msg += " (at " + string(ptr) + ")"
	}
	return &wireError{err: errors.New("jsonwire: " + msg)}
}

// unexpectedTokenError returns a syntax error for the given unexpected token.
func (d decoder) unexpectedTokenError(tok jsontext.Token) error {
	return d.newError("unexpected token %s", tokenString(tok))
}

// tokenString formats a token for an error message.
func tokenString(tok jsontext.Token) string {
	if tok.Kind() == kindString {
		return strconv.Quote(tok.String())
	}
	return tok.String()
}

// scalar is a JSON scalar value. For a string, s is the unquoted value. For a
// number, s is the raw JSON number. For other kinds, s is not used.
//
// Unless owned is true, s refers to memory of the jsontext.Decoder and is
// valid only until the next read from the decoder. Use it, or copy it, before
// the next read.
type scalar struct {
	kind  jsontext.Kind
	s     string
	owned bool
}

func (v scalar) String() string {
	switch v.kind {
	case kindString:
		return strconv.Quote(v.s)
	case kindNumber:
		return v.s
	case kindNull:
		return "null"
	case kindTrue:
		return "true"
	case kindFalse:
		return "false"
	default:
		return v.kind.String()
	}
}

// ownedString returns a copy of s that does not refer to decoder memory.
func (v scalar) ownedString() string {
	if v.owned {
		return v.s
	}
	return strings.Clone(v.s)
}

// readScalar reads the next JSON value. If the value is not a scalar, the
// returned kind tells what it is, and the caller reports an error.
func (d decoder) readScalar() (scalar, error) {
	// Do not read a whole object or array, which cannot be a scalar, before
	// the caller reports the error.
	if kind := d.dec.PeekKind(); kind == kindObjectOpen || kind == kindArrayOpen {
		tok, err := d.dec.ReadToken()
		if err != nil {
			return scalar{}, err
		}
		return scalar{kind: tok.Kind()}, nil
	}
	// ReadValue is faster than ReadToken, and it does not allocate.
	raw, err := d.dec.ReadValue()
	if err != nil {
		return scalar{}, err
	}
	switch kind := raw.Kind(); kind {
	case kindString:
		b, owned, err := d.stringContent(raw)
		if err != nil {
			return scalar{}, err
		}
		return scalar{kind: kindString, s: unsafeString(b), owned: owned}, nil
	case kindNumber:
		return scalar{kind: kindNumber, s: unsafeString(raw)}, nil
	default:
		return scalar{kind: kind}, nil
	}
}

// stringContent returns the unquoted content of the raw JSON string. If the
// string has no escape sequences, the result refers to raw and owned is false.
// It always rejects invalid UTF-8.
func (d decoder) stringContent(raw jsontext.Value) (b []byte, owned bool, err error) {
	content := raw[1 : len(raw)-1]
	if bytes.IndexByte(content, '\\') < 0 {
		// The decoder checks UTF-8, unless its options allow invalid UTF-8.
		if d.checkUTF8 && !utf8.Valid(content) {
			return nil, false, d.newError("invalid UTF-8 in string")
		}
		return content, false, nil
	}
	b, err = jsontext.AppendUnquote(nil, raw)
	if err != nil {
		return nil, false, d.newError("invalid string: %v", err)
	}
	return b, true, nil
}

// readStringValue reads the next JSON value, which must be a string. It does
// not read a whole object or array before it reports the error, so that an
// object or array that never ends cannot make it read without a limit.
func (d decoder) readStringValue() (jsontext.Value, error) {
	if kind := d.dec.PeekKind(); kind == kindObjectOpen || kind == kindArrayOpen {
		tok, err := d.dec.ReadToken()
		if err != nil {
			return nil, err
		}
		return nil, d.unexpectedTokenError(tok)
	}
	return d.dec.ReadValue()
}

// readStringView reads the next JSON value, which must be a string, and
// returns its unquoted value. The result can refer to decoder memory, so it is
// valid only until the next read from the decoder. Use it for object names
// and other short-lived values.
func (d decoder) readStringView() (string, error) {
	raw, err := d.readStringValue()
	if err != nil {
		return "", err
	}
	if raw.Kind() != kindString {
		return "", d.unexpectedValueError(raw)
	}
	b, _, err := d.stringContent(raw)
	return unsafeString(b), err
}

// readString reads the next JSON value, which must be a string, and returns
// its unquoted value. The result does not refer to decoder memory.
func (d decoder) readString() (string, error) {
	raw, err := d.readStringValue()
	if err != nil {
		return "", err
	}
	if raw.Kind() != kindString {
		return "", d.unexpectedValueError(raw)
	}
	b, owned, err := d.stringContent(raw)
	if err != nil {
		return "", err
	}
	if owned {
		return unsafeString(b), nil
	}
	return string(b), nil
}

// unexpectedValueError returns a syntax error for the given unexpected value.
func (d decoder) unexpectedValueError(raw jsontext.Value) error {
	const maxLen = 64
	if len(raw) > maxLen {
		return d.newError("unexpected token %s...", raw[:maxLen])
	}
	return d.newError("unexpected token %s", raw)
}

// unsafeString returns a string that shares memory with b. The caller must
// not change b while the string is in use.
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// subDecoder returns a decoder that reads the given JSON value with the same
// options and recursion limit as d.
//
// The sub-decoder parses raw in place, without a copy. The caller must not
// read from d.dec while it uses the sub-decoder, because raw can refer to the
// memory of d.dec. The caller must call putSubDecoder when it is done.
//
// The jsontext.Decoder comes from a pool. This is safe because no string view
// lives longer than the sub-decoder: a value that is kept is always a copy.
func (d decoder) subDecoder(raw []byte) decoder {
	sub := d
	dec, _ := subDecoderPool.Get().(*jsontext.Decoder)
	if dec == nil {
		dec = jsontext.NewDecoder(bytes.NewBuffer(raw), d.subOptions)
	} else {
		dec.Reset(bytes.NewBuffer(raw), d.subOptions)
	}
	sub.dec = dec
	return sub
}

var subDecoderPool sync.Pool

// putSubDecoder returns the jsontext.Decoder of sub to the pool, without the
// reference to its input.
func putSubDecoder(sub decoder) {
	sub.dec.Reset(emptyBuffer)
	subDecoderPool.Put(sub.dec)
}

// reset makes the sub-decoder read raw again from the start.
func (d decoder) reset(raw []byte) {
	d.dec.Reset(bytes.NewBuffer(raw), d.subOptions)
}

// unmarshalMessage unmarshals a message into the given protoreflect.Message.
func (d decoder) unmarshalMessage(m protoreflect.Message, skipTypeURL bool) error {
	d.opts.RecursionLimit--
	if d.opts.RecursionLimit < 0 {
		return errorf("exceeded max recursion depth")
	}
	if unmarshal := wellKnownTypeUnmarshaler(m.Descriptor().FullName()); unmarshal != nil {
		return unmarshal(d, m)
	}

	tok, err := d.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != kindObjectOpen {
		return d.unexpectedTokenError(tok)
	}

	messageDesc := m.Descriptor()
	if isMessageSet(messageDesc) {
		return errorf("no support for proto1 MessageSets")
	}

	var seenFields, seenOneofs fieldSet
	fieldDescs := messageDesc.Fields()
	for {
		// Read field name.
		if d.dec.PeekKind() == kindObjectClose {
			_, err := d.dec.ReadToken()
			return err
		}
		// The name refers to decoder memory. It is valid only until the next
		// read.
		name, err := d.readStringView()
		if err != nil {
			return err
		}

		// Unmarshaling a non-custom embedded message in Any will contain the
		// JSON field "@type" which should be skipped because it is not a field
		// of the embedded message, but simply an artifact of the Any format.
		if skipTypeURL && name == "@type" {
			if err := d.dec.SkipValue(); err != nil {
				return err
			}
			continue
		}

		// Get the FieldDescriptor.
		var fd protoreflect.FieldDescriptor
		if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
			// Only extension names are in [name] format.
			// The resolver is caller code that can keep its argument, so it
			// gets a copy, not a view into decoder memory.
			extName := protoreflect.FullName(strings.Clone(name[1 : len(name)-1]))
			extType, err := d.opts.Resolver.FindExtensionByName(extName)
			// protoregistry requires resolvers to return exactly NotFound,
			// not a wrapped error. Compare the same way as protojson.
			if err != nil && err != protoregistry.NotFound {
				return d.newError("unable to resolve %q: %v", extName, err)
			}
			if extType != nil {
				fd = extType.TypeDescriptor()
				if !messageDesc.ExtensionRanges().Has(fd.Number()) || fd.ContainingMessage().FullName() != messageDesc.FullName() {
					return d.newError("message %v cannot be extended by %v", messageDesc.FullName(), fd.FullName())
				}
			}
		} else {
			// The name can either be the JSON name or the proto field name.
			fd = fieldDescs.ByJSONName(name)
			if fd == nil {
				fd = fieldDescs.ByTextName(name)
			}
		}

		if fd == nil {
			// Field is unknown.
			if d.opts.DiscardUnknown {
				if err := d.skipJSONValue(); err != nil {
					return err
				}
				continue
			}
			return d.newError("unknown field %q", name)
		}

		// Do not allow duplicate fields. Fields are identified by index, and
		// extensions by number.
		key := uint64(fd.Index())
		if fd.IsExtension() {
			key = 1<<32 | uint64(fd.Number())
		}
		if seenFields.has(key) {
			return d.newError("duplicate field %q", name)
		}
		seenFields.set(key)

		// No need to set values for JSON null unless the field type is
		// google.protobuf.Value or google.protobuf.NullValue.
		if d.dec.PeekKind() == kindNull && !isKnownValue(fd) && !isNullValue(fd) {
			if _, err := d.dec.ReadToken(); err != nil {
				return err
			}
			continue
		}

		switch {
		case fd.IsList():
			list := m.Mutable(fd).List()
			if err := d.unmarshalList(list, fd); err != nil {
				return err
			}
		case fd.IsMap():
			mmap := m.Mutable(fd).Map()
			if err := d.unmarshalMap(mmap, fd); err != nil {
				return err
			}
		default:
			// If field is a oneof, check if it has already been set.
			if od := fd.ContainingOneof(); od != nil {
				idx := uint64(od.Index())
				if seenOneofs.has(idx) {
					return d.newError("error parsing %q, oneof %v is already set", fd.TextName(), od.FullName())
				}
				seenOneofs.set(idx)
			}

			// Required or optional fields.
			if err := d.unmarshalSingular(m, fd); err != nil {
				return err
			}
		}
	}
}

func isKnownValue(fd protoreflect.FieldDescriptor) bool {
	md := fd.Message()
	return md != nil && md.FullName() == valueFullName
}

func isNullValue(fd protoreflect.FieldDescriptor) bool {
	ed := fd.Enum()
	return ed != nil && ed.FullName() == nullValueFullName
}

// unmarshalSingular unmarshals to the non-repeated field specified by the
// given FieldDescriptor.
func (d decoder) unmarshalSingular(m protoreflect.Message, fd protoreflect.FieldDescriptor) error {
	var val protoreflect.Value
	var err error
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		val = m.NewField(fd)
		err = d.unmarshalMessage(val.Message(), false)
	default:
		val, err = d.unmarshalScalar(fd)
	}

	if err != nil {
		return err
	}
	if val.IsValid() {
		m.Set(fd, val)
	}
	return nil
}

// unmarshalScalar unmarshals to a scalar or enum protoreflect.Value specified
// by the given FieldDescriptor.
func (d decoder) unmarshalScalar(fd protoreflect.FieldDescriptor) (protoreflect.Value, error) {
	const b32 int = 32
	const b64 int = 64

	tok, err := d.readScalar()
	if err != nil {
		return protoreflect.Value{}, err
	}

	kind := fd.Kind()
	switch kind {
	case protoreflect.BoolKind:
		switch tok.kind {
		case kindTrue:
			return protoreflect.ValueOfBool(true), nil
		case kindFalse:
			return protoreflect.ValueOfBool(false), nil
		}

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if v, ok := unmarshalInt(tok, b32); ok {
			return v, nil
		}

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		if v, ok := unmarshalInt(tok, b64); ok {
			return v, nil
		}

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if v, ok := unmarshalUint(tok, b32); ok {
			return v, nil
		}

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if v, ok := unmarshalUint(tok, b64); ok {
			return v, nil
		}

	case protoreflect.FloatKind:
		if v, ok := unmarshalFloat(tok, b32); ok {
			return v, nil
		}

	case protoreflect.DoubleKind:
		if v, ok := unmarshalFloat(tok, b64); ok {
			return v, nil
		}

	case protoreflect.StringKind:
		if tok.kind == kindString {
			return protoreflect.ValueOfString(tok.ownedString()), nil
		}

	case protoreflect.BytesKind:
		if v, ok := unmarshalBytes(tok); ok {
			return v, nil
		}

	case protoreflect.EnumKind:
		if v, ok := unmarshalEnum(tok, fd, d.opts.DiscardUnknown); ok {
			return v, nil
		}

	default:
		return protoreflect.Value{}, d.newError("invalid scalar kind %v", kind)
	}

	return protoreflect.Value{}, d.newError("invalid value for %v field %v: %v", kind, fd.JSONName(), tok)
}

// numberFromString returns the JSON number in a JSON string. The whole string
// must be one JSON number with no white space.
func numberFromString(s string) (string, bool) {
	if strings.TrimSpace(s) != s {
		return "", false
	}
	if n, ok := parseNumber(s); !ok || n != len(s) {
		return "", false
	}
	return s, true
}

func unmarshalInt(tok scalar, bitSize int) (protoreflect.Value, bool) {
	var raw string
	switch tok.kind {
	case kindNumber:
		raw = tok.s
	case kindString:
		s, ok := numberFromString(tok.s)
		if !ok {
			return protoreflect.Value{}, false
		}
		raw = s
	default:
		return protoreflect.Value{}, false
	}
	n, ok := parseInt(raw, bitSize)
	if !ok {
		return protoreflect.Value{}, false
	}
	if bitSize == 32 {
		return protoreflect.ValueOfInt32(int32(n)), true
	}
	return protoreflect.ValueOfInt64(n), true
}

func unmarshalUint(tok scalar, bitSize int) (protoreflect.Value, bool) {
	var raw string
	switch tok.kind {
	case kindNumber:
		raw = tok.s
	case kindString:
		s, ok := numberFromString(tok.s)
		if !ok {
			return protoreflect.Value{}, false
		}
		raw = s
	default:
		return protoreflect.Value{}, false
	}
	n, ok := parseUint(raw, bitSize)
	if !ok {
		return protoreflect.Value{}, false
	}
	if bitSize == 32 {
		return protoreflect.ValueOfUint32(uint32(n)), true
	}
	return protoreflect.ValueOfUint64(n), true
}

func unmarshalFloat(tok scalar, bitSize int) (protoreflect.Value, bool) {
	var raw string
	switch tok.kind {
	case kindNumber:
		raw = tok.s
	case kindString:
		switch tok.s {
		case "NaN":
			return floatValue(math.NaN(), bitSize), true
		case "Infinity":
			return floatValue(math.Inf(+1), bitSize), true
		case "-Infinity":
			return floatValue(math.Inf(-1), bitSize), true
		}
		s, ok := numberFromString(tok.s)
		if !ok {
			return protoreflect.Value{}, false
		}
		raw = s
	default:
		return protoreflect.Value{}, false
	}
	n, err := strconv.ParseFloat(raw, bitSize)
	if err != nil {
		return protoreflect.Value{}, false
	}
	return floatValue(n, bitSize), true
}

func floatValue(n float64, bitSize int) protoreflect.Value {
	if bitSize == 32 {
		return protoreflect.ValueOfFloat32(float32(n))
	}
	return protoreflect.ValueOfFloat64(n)
}

func unmarshalBytes(tok scalar) (protoreflect.Value, bool) {
	if tok.kind != kindString {
		return protoreflect.Value{}, false
	}

	s := tok.s
	enc := base64.StdEncoding
	if strings.ContainsAny(s, "-_") {
		enc = base64.URLEncoding
	}
	if len(s)%4 != 0 {
		enc = enc.WithPadding(base64.NoPadding)
	}
	b, err := enc.DecodeString(s)
	if err != nil {
		return protoreflect.Value{}, false
	}
	return protoreflect.ValueOfBytes(b), true
}

func unmarshalEnum(tok scalar, fd protoreflect.FieldDescriptor, discardUnknown bool) (protoreflect.Value, bool) {
	switch tok.kind {
	case kindString:
		// Lookup EnumNumber based on name.
		if enumVal := fd.Enum().Values().ByName(protoreflect.Name(tok.s)); enumVal != nil {
			return protoreflect.ValueOfEnum(enumVal.Number()), true
		}
		if discardUnknown {
			return protoreflect.Value{}, true
		}

	case kindNumber:
		if n, ok := parseInt(tok.s, 32); ok {
			return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), true
		}

	case kindNull:
		// This is only valid for google.protobuf.NullValue.
		if isNullValue(fd) {
			return protoreflect.ValueOfEnum(0), true
		}
	}

	return protoreflect.Value{}, false
}

func (d decoder) unmarshalList(list protoreflect.List, fd protoreflect.FieldDescriptor) error {
	tok, err := d.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != kindArrayOpen {
		return d.unexpectedTokenError(tok)
	}

	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		for {
			if d.dec.PeekKind() == kindArrayClose {
				_, err := d.dec.ReadToken()
				return err
			}
			val := list.NewElement()
			if err := d.unmarshalMessage(val.Message(), false); err != nil {
				return err
			}
			list.Append(val)
		}
	default:
		for {
			if d.dec.PeekKind() == kindArrayClose {
				_, err := d.dec.ReadToken()
				return err
			}
			val, err := d.unmarshalScalar(fd)
			if err != nil {
				return err
			}
			if val.IsValid() {
				list.Append(val)
			}
		}
	}
}

func (d decoder) unmarshalMap(mmap protoreflect.Map, fd protoreflect.FieldDescriptor) error {
	tok, err := d.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != kindObjectOpen {
		return d.unexpectedTokenError(tok)
	}

	// Determine ahead whether map entry is a scalar type or a message type in
	// order to call the appropriate unmarshalMapValue func inside the for loop
	// below.
	var unmarshalMapValue func() (protoreflect.Value, error)
	switch fd.MapValue().Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		unmarshalMapValue = func() (protoreflect.Value, error) {
			val := mmap.NewValue()
			if err := d.unmarshalMessage(val.Message(), false); err != nil {
				return protoreflect.Value{}, err
			}
			return val, nil
		}
	default:
		unmarshalMapValue = func() (protoreflect.Value, error) {
			return d.unmarshalScalar(fd.MapValue())
		}
	}

	for {
		// Read field name.
		if d.dec.PeekKind() == kindObjectClose {
			_, err := d.dec.ReadToken()
			return err
		}
		// The name refers to decoder memory. unmarshalMapKey copies it if it
		// keeps it.
		name, err := d.readStringView()
		if err != nil {
			return err
		}

		// Unmarshal field name.
		pkey, err := d.unmarshalMapKey(name, fd.MapKey())
		if err != nil {
			return err
		}

		// Check for duplicate field name.
		if mmap.Has(pkey) {
			return d.newError("duplicate map key %q", name)
		}

		// Read and unmarshal field value.
		pval, err := unmarshalMapValue()
		if err != nil {
			return err
		}
		if pval.IsValid() {
			mmap.Set(pkey, pval)
		}
	}
}

// unmarshalMapKey converts the given JSON object name into a
// protoreflect.MapKey. A map key type is any integral or string type.
func (d decoder) unmarshalMapKey(name string, fd protoreflect.FieldDescriptor) (protoreflect.MapKey, error) {
	const b32 = 32
	const b64 = 64
	const base10 = 10

	kind := fd.Kind()
	switch kind {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(strings.Clone(name)).MapKey(), nil

	case protoreflect.BoolKind:
		switch name {
		case "true":
			return protoreflect.ValueOfBool(true).MapKey(), nil
		case "false":
			return protoreflect.ValueOfBool(false).MapKey(), nil
		}

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if n, err := strconv.ParseInt(name, base10, b32); err == nil {
			return protoreflect.ValueOfInt32(int32(n)).MapKey(), nil
		}

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		if n, err := strconv.ParseInt(name, base10, b64); err == nil {
			return protoreflect.ValueOfInt64(n).MapKey(), nil
		}

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if n, err := strconv.ParseUint(name, base10, b32); err == nil {
			return protoreflect.ValueOfUint32(uint32(n)).MapKey(), nil
		}

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if n, err := strconv.ParseUint(name, base10, b64); err == nil {
			return protoreflect.ValueOfUint64(n).MapKey(), nil
		}

	default:
		return protoreflect.MapKey{}, d.newError("invalid kind for map key: %v", kind)
	}

	return protoreflect.MapKey{}, d.newError("invalid value for %v key: %q", kind, name)
}

// skipJSONValue reads one JSON value (null, boolean, string, number, object,
// or array) and drops it. Like protojson, it limits the nesting depth to the
// remaining recursion limit.
func (d decoder) skipJSONValue() error {
	var open int
	for {
		if d.checkUTF8 && d.dec.PeekKind() == kindString {
			// The decoder allows invalid UTF-8, so check the raw string. This
			// is the same rule as for strings that are not skipped.
			raw, err := d.dec.ReadValue()
			if err != nil {
				return err
			}
			if _, _, err := d.stringContent(raw); err != nil {
				return err
			}
			if open == 0 {
				return nil
			}
			continue
		}
		tok, err := d.dec.ReadToken()
		if err != nil {
			return err
		}
		switch tok.Kind() {
		case kindObjectClose, kindArrayClose:
			open--
		case kindObjectOpen, kindArrayOpen:
			open++
			if open > d.opts.RecursionLimit {
				return errorf("exceeded max recursion depth")
			}
		}
		if open == 0 {
			return nil
		}
	}
}

// fieldSet is a set of field keys. Keys below 256 use a bitset with no
// allocation, which covers the fields of almost all messages.
type fieldSet struct {
	lo [4]uint64
	hi map[uint64]struct{}
}

func (s *fieldSet) has(n uint64) bool {
	if n < 256 {
		return s.lo[n/64]&(1<<(n%64)) != 0
	}
	_, ok := s.hi[n]
	return ok
}

func (s *fieldSet) set(n uint64) {
	if n < 256 {
		s.lo[n/64] |= 1 << (n % 64)
		return
	}
	if s.hi == nil {
		s.hi = make(map[uint64]struct{})
	}
	s.hi[n] = struct{}{}
}
