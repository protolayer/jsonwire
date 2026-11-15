# Performance and tuning

## Summary

Compared with `protojson`:

- **Unmarshal:** 31% to 37% faster, with 25% fewer allocations.
- **Marshal:** 9% to 26% faster, with 2x to 4x fewer allocations.
- **Faster for most message shapes tested.** Long text that is not ASCII is slower. See [Other message shapes](#other-message-shapes).
- **With one option, a further 28% to 38%.** See [Tuning](#tuning).

## Why it is faster

- **One pass.** Inside `encoding/json`, `protojson` makes a separate `[]byte` that `encoding/json` then checks and copies. To unmarshal, the JSON is parsed two times. `jsonwire` writes straight into the encoder and parses only once.
- **A faster JSON engine.** `jsonwire` uses `jsontext`, the optimized engine of `encoding/json/v2`. `protojson` uses its own, older JSON code.
- **Fewer allocations.** Field names and numbers are read directly from the decoder's buffer. Numbers, bytes, and timestamps are written directly into the encoder's buffer. Memory is reused between calls.

## Benchmarks

The benchmarks use `TestAllTypesProto3` from the protobuf conformance suite ([proto](../proto/google/protobuf/test_messages_proto3.proto)):

- **small**: 8 fields set, 266 bytes of JSON ([small.json](../testdata/benchmark/small.json))
- **large**: 73 fields set, with nested messages and well-known types, 3.7 KB of JSON ([large.json](../testdata/benchmark/large.json))

"In a struct" means the message is a field of a struct that `encoding/json/v2` handles.

| Benchmark | `protojson` | `jsonwire` | Change | Allocations |
| --- | ---: | ---: | ---: | ---: |
| Marshal, small | 3.06 us | 2.77 us | -9% | 32 -> 8 |
| Marshal, large | 23.5 us | 17.4 us | -26% | 269 -> 92 |
| Marshal in a struct, small | 3.69 us | 3.06 us | -17% | 35 -> 10 |
| Marshal in a struct, large | 34.4 us | 28.1 us | -18% | 371 -> 192 |
| Unmarshal, small | 3.12 us | 2.16 us | -31% | 56 -> 43 |
| Unmarshal, large | 43.4 us | 27.5 us | -37% | 707 -> 502 |
| Unmarshal in a struct, small | 3.66 us | 2.35 us | -36% | 58 -> 44 |
| Unmarshal in a struct, large | 54.9 us | 36.2 us | -34% | 807 -> 602 |

Medians of 10 runs on an Apple M4 Pro, Go 1.27.4, `-cpu=1`. The spread between runs is 2% to 7%. To reproduce, run `make bench`, and `make bench-parallel` for the parallel results.

Most of the remaining cost is protobuf reflection.

### Other message shapes

To check that the results are not specific to one message, `BenchmarkShapes` measures other shapes and sizes (medians of 6 runs, change compared with `protojson`):

| Shape | Marshal | Unmarshal |
| --- | ---: | ---: |
| A list of 20 records (strings, numbers, a timestamp) | -15% | -35% |
| A list of 5000 records (3.5 MB of JSON) | -18% | -36% |
| 500 strings with escape characters | -27% | -5% |
| 6000 numbers in packed fields | -36% | -42% |
| One 64 KB bytes field | -31% | -25% |
| One 600 KB bytes field | -32% | -28% |
| One 2 MB bytes field | -34% | -28% |
| The large message as a dynamic message (`dynamicpb`) | -22% | -33% |
| 1000 `Any` values that hold a `Duration` | -31% | -15% |
| 10000 integers written as `123.0` (unmarshal only) | n/a | -39% |
| One 1 MB string with many escape characters | -36% | **+11%** |
| One 1 MB string of CJK text | **+37%** | **+35%** |

`jsonwire` allocates fewer bytes for each of these shapes, except 2% more for unmarshal of the `Any` values. With all CPUs at the same time (`make bench-parallel`), the gains for the first eight shapes are 25% to 64% for marshal and 22% to 41% for unmarshal.

Two shapes are slower:

- **Text that is not ASCII.** `jsontext` checks UTF-8 one character at a time for characters outside ASCII. For short strings, the gains on the rest of the message hide this cost. For long text, `protojson` is faster. The cost is inside `jsontext`, and `jsonwire` cannot skip the check.
- **Unmarshal of long strings with many escape characters.** Both packages spend most of the time on the same unescaping work, and `jsontext` is a little slower at it.

These are edge cases. Most messages have many fields with short strings, numbers, and nested messages, and for these `jsonwire` is faster. The slowdown matters only when long text strings make up most of the message, for example messages that carry whole documents in CJK text. For such messages, measure both packages with real data.

## Tuning

### Turn off the duplicate name check

`encoding/json/v2` checks every JSON object for duplicate names by default. For large messages, this check is expensive. `jsonwire` already rejects duplicate fields and map keys, so the check is not necessary for messages:

```go
b, err := json.Marshal(v, jsontext.AllowDuplicateNames(true))
```

For the large message, marshal in a struct is 38% faster and unmarshal is 28% faster. The option applies to the whole call, so other types in the same value lose the check too.
