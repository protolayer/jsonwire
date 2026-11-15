# Compatibility with protojson

`jsonwire` is a port of `protojson` to `jsontext`. The output and the accepted input are the same, except for the differences below.

## How it is tested

- **Conformance:** the protobuf conformance suite gives the same result as `protojson`. Run `make conformance`.
- **Differential tests:** random proto2, proto3, and editions messages, with all option combinations. The output must match `protojson` byte for byte, without white space.
- **Edge cases:** more than 400 inputs for numbers, strings, enums, maps, oneofs, null, extensions, and well-known types.
- **Fuzzing:** both directions. Run `make fuzz`.

## Differences

### Encoder and decoder options apply

`MarshalEncode` and `UnmarshalDecode` use the given `jsontext.Encoder` or `jsontext.Decoder`, so its options control the JSON syntax. For example, `encoding/json/v2` rejects duplicate object names by default, but `protojson` accepts them in unknown fields:

```go
input := `{"unknown":1,"unknown":2}`
opts := jsonwire.UnmarshalOptions{DiscardUnknown: true}

opts.Unmarshal([]byte(input), msg)                                       // ok, like protojson
opts.UnmarshalDecode(jsontext.NewDecoder(strings.NewReader(input)), msg) // error: duplicate name
```

One rule does not change: strings must be valid UTF-8, even if the options allow invalid UTF-8, as `encoding/json` v1 does.

### Only valid JSON

`protojson` accepts some numbers that are not valid JSON. `jsonwire` rejects them:

```
{"count":1E}    protojson: count = 1    jsonwire: error
```

### Deterministic output

`protojson` adds random white space to its output, so that code cannot depend on the exact bytes. `jsonwire` output is compact and deterministic: the same message and options give the same bytes. Fields are in declaration order, extensions are sorted by name, and map entries are sorted by key.

```
protojson:  {"a":1,  "b":2}    (changes between builds)
jsonwire:   {"a":1,"b":2}
```

### No partial output for missing required fields

For a message with a missing required field, `protojson.Marshal` returns the JSON and an error. `jsonwire` returns only the error, and `MarshalEncode` writes nothing to the encoder.

### Conflicting JSON names

A descriptor can give two fields the same JSON name, for example with the editions feature `legacy_json_field_conflicts`. `protojson` writes both keys. `jsonwire.MarshalEncode` fails if the encoder checks for duplicate names, which is the `jsontext` default. `Marshal` writes both keys, like `protojson`.

### Nesting limit

`jsontext` rejects JSON that is nested more than 10000 levels deep. `protojson` limits only message nesting, with `RecursionLimit`. So a `RecursionLimit` above 10000 has no effect in `jsonwire`:

```go
// A message nested 15000 levels deep.
opts := jsonwire.UnmarshalOptions{RecursionLimit: 20000}
opts.Unmarshal(input, msg) // error: exceeded max depth (protojson: ok)
```
