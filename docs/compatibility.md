# Compatibility with protojson

`jsonwire` is a port of `protojson` to `jsontext`. The output and the accepted input are the same, except for the differences below.

## How it is tested

- **Conformance:** the protobuf conformance suite gives the same result as `protojson`. Run `make conformance`.
- **Differential tests:** random proto2, proto3, and editions messages, with all option combinations. The output must match `protojson` byte for byte, without white space.
- **Edge cases:** more than 400 inputs for numbers, strings, enums, maps, oneofs, null, extensions, and well-known types.
- **Fuzzing:** both directions. Run `make fuzz`.

## Differences

For typical messages and well-formed JSON, none of these differences change the result. Each section says what is different, who is affected, and what to do.

| Difference | Who is affected |
| --- | --- |
| [Encoder and decoder options apply](#encoder-and-decoder-options-apply) | Callers of `MarshalEncode` and `UnmarshalDecode`, for JSON with duplicate names |
| [Only valid JSON](#only-valid-json) | Clients that send invalid numbers such as `1E` |
| [Deterministic output](#deterministic-output) | Nobody negatively. Code can now compare output bytes |
| [No partial output for missing required fields](#no-partial-output-for-missing-required-fields) | Code that reads the output of a failed `Marshal` |
| [Conflicting JSON names](#conflicting-json-names) | Schemas with two fields that have the same JSON name |
| [Nesting limit](#nesting-limit) | Messages nested thousands of levels deep |

### Encoder and decoder options apply

`MarshalEncode` and `UnmarshalDecode` use the given `jsontext.Encoder` or `jsontext.Decoder`, so its options control the JSON syntax. `encoding/json/v2` calls these methods with the options of its own call. For example, `encoding/json/v2` rejects duplicate object names by default, but `protojson` accepts them in unknown fields:

```go
input := `{"unknown":1,"unknown":2}`
opts := jsonwire.UnmarshalOptions{DiscardUnknown: true}

opts.Unmarshal([]byte(input), msg)                                       // ok, like protojson
opts.UnmarshalDecode(jsontext.NewDecoder(strings.NewReader(input)), msg) // error: duplicate name
```

**Why it is different:** a message inside a larger JSON value is part of that value, so the rules of the whole value apply to it. This is also why `Multiline` in a `json.Marshal` call indents the message.

**Who is affected:** only input with duplicate object names, through `UnmarshalDecode` or `encoding/json/v2`. Duplicate names are rare and usually a client bug. `Marshal`, `Unmarshal`, and `encoding/json` v1 accept them, like `protojson`.

**What to do:** to accept duplicate names, set `jsontext.AllowDuplicateNames(true)` on the decoder or on the `json.Unmarshal` call. This also makes decoding faster (see [Tuning](performance.md#tuning)).

One rule does not change: strings in a message must be valid UTF-8, even if the options allow invalid UTF-8, as `encoding/json` v1 does. This is the same as `protojson`.

### Only valid JSON

`protojson` accepts some numbers that are not valid JSON. `jsonwire` rejects them:

```
{"count":1E}    protojson: count = 1    jsonwire: error
```

**Why it is different:** `jsontext` accepts only valid JSON.

**Who is affected:** only clients that send invalid JSON. Other JSON parsers, for example in browsers, reject such input too, so correct clients do not send it.

**What to do:** nothing.

### Deterministic output

`protojson` adds random white space to its output, so that code cannot depend on the exact bytes. `jsonwire` output is compact and deterministic: the same message, options, and `jsonwire` version give the same bytes. Fields are in declaration order, extensions are sorted by name, and map entries are sorted by key.

```
protojson:  {"a":1,  "b":2}    (changes between builds)
jsonwire:   {"a":1,"b":2}
```

**Why it is different:** the random white space in `protojson` prevents code from depending on output that can change. `jsonwire` gives stable output instead.

**Who is affected:** nobody negatively. Code that worked with `protojson` cannot depend on the bytes, so it continues to work. New code can compare the output, for example in golden tests, caches, or content hashes.

**What to do:** nothing.

### No partial output for missing required fields

For a message with a missing required field, `protojson.Marshal` and `MarshalAppend` return the JSON and an error. `jsonwire` returns only the error, and `MarshalEncode` writes nothing to the encoder.

**Why it is different:** `MarshalEncode` writes into a larger JSON value. Partial output followed by an error would leave that value broken.

**Who is affected:** only messages with required fields, and only code that uses the output of a `Marshal` call that failed. That output is not valid for the schema, so code rarely uses it.

**What to do:** to get the output without the required fields, set `AllowPartial`. For debugging, use `Format`.

### Conflicting JSON names

A descriptor can give two fields the same JSON name, for example with the editions feature `legacy_json_field_conflicts`. `protojson` writes both keys. `jsonwire.MarshalEncode` fails if the encoder checks for duplicate names, which is the `jsontext` default. `Marshal` writes both keys, like `protojson`.

**Why it is different:** the same encoder options apply as in [Encoder and decoder options apply](#encoder-and-decoder-options-apply).

**Who is affected:** only schemas with this conflict, and only when both fields are set. `protoc` gives a warning or an error for such a conflict, so it is rare.

**What to do:** set `jsontext.AllowDuplicateNames(true)` on the encoder, or use `Marshal`. A reader cannot tell the two keys apart anyway, so a fix in the schema is better.

### Nesting limit

`jsontext` rejects JSON that is nested more than 10000 levels deep, for `Marshal` and `Unmarshal`, and this limit cannot be changed. `protojson` limits only message nesting, with `RecursionLimit` (default 10000), and it has no limit for `Marshal`. A list or a map adds a JSON level that is not a message level. This gives three differences.

A `RecursionLimit` above 10000 cannot raise the JSON limit:

```go
// A message nested 15000 levels deep.
opts := jsonwire.UnmarshalOptions{RecursionLimit: 20000}
opts.Unmarshal(input, msg) // error: exceeded max depth (protojson: ok)
```

With the default options, a deep message that goes through a list or a map can fail before it gets to 10000 message levels:

```go
// {"children":[{"children":[{ ... }]}]}: each message level adds 2 JSON levels.
jsonwire.Unmarshal(input, msg) // error at about 5000 message levels (protojson: ok)
```

`Marshal` fails if the output is nested more than 10000 JSON levels deep. Through a list or a map, this occurs with fewer message levels:

```go
// A message nested 10001 levels deep, or about 5000 levels through a list.
jsonwire.Marshal(msg) // error: exceeded max depth (protojson: ok)
```

**Why it is different:** the limit is in `jsontext`, and it protects against input that uses too much stack.

**Who is affected:** only messages that are nested thousands of levels deep, for example a long linked list or a deep tree that is stored as nested messages. Typical messages have fewer than 100 levels. `protojson` cannot read a message deeper than 10000 levels back with its default `RecursionLimit` either.

**What to do:** keep the nesting below the limit, for example with a repeated field in place of a linked list.
