# Performance and tuning

## Summary

Compared with `protojson`:

- **Unmarshal:** 30% to 39% faster, with 25% fewer allocations.
- **Marshal:** 10% to 23% faster, with 2x to 4x fewer allocations.
- **Faster for most message shapes tested.** On one CPU, long text that is not ASCII is slower, and so is unmarshal of long strings with many escape characters. See [Other message shapes](#other-message-shapes).
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
| Marshal, small | 3.01 us | 2.69 us | -10% | 32 -> 8 |
| Marshal, large | 21.4 us | 16.4 us | -23% | 269 -> 92 |
| Marshal in a struct, small | 3.58 us | 2.98 us | -17% | 35 -> 10 |
| Marshal in a struct, large | 32.8 us | 26.5 us | -19% | 371 -> 192 |
| Unmarshal, small | 3.16 us | 2.22 us | -30% | 56 -> 43 |
| Unmarshal, large | 42.4 us | 25.8 us | -39% | 707 -> 499 |
| Unmarshal in a struct, small | 3.62 us | 2.41 us | -33% | 58 -> 44 |
| Unmarshal in a struct, large | 53.5 us | 35.3 us | -34% | 807 -> 598 |

Medians of 10 runs on an Apple M4 Pro, Go 1.27.4, `-cpu=1`. The spread between runs is 1% to 8%, and up to 22% for one row. To reproduce, run `make bench`, and `make bench-parallel` for the parallel results.

Most of the remaining cost is protobuf reflection.

### Other message shapes

To check that the results are not specific to one message, `BenchmarkShapes` measures other shapes and sizes. The table gives the change compared with `protojson`, on one CPU (`make bench`) and with all 14 CPUs at the same time (`make bench-parallel`), as medians of 6 runs:

| Shape | Marshal, 1 CPU | Unmarshal, 1 CPU | Marshal, all CPUs | Unmarshal, all CPUs |
| --- | ---: | ---: | ---: | ---: |
| A list of 20 records (strings, numbers, a timestamp) | -17% | -37% | -31% | -25% |
| A list of 5000 records (3.5 MB of JSON) | -18% | -37% | -20% | -26% |
| 500 strings with escape characters | -27% | -8% | -62% | -32% |
| 6000 numbers in packed fields | -40% | -49% | -52% | -39% |
| One 64 KB bytes field | -32% | -27% | -38% | -38% |
| One 600 KB bytes field | -33% | -26% | -46% | -29% |
| One 2 MB bytes field | -34% | -27% | -45% | -20% |
| The large message as a dynamic message (`dynamicpb`) | -40% | -30% | -42% | -24% |
| 1000 `Any` values that hold a `Duration` | -29% | -13% | -1% | -3% |
| 10000 integers written as `123.0` | n/a | -38% | n/a | -30% |
| One 1 MB string with many escape characters | -39% | **+11%** | -74% | -52% |
| One 1 MB string of Japanese text | **+33%** | **+22%** | -19% | -2% |

`jsonwire` allocates fewer bytes for each of these shapes, except 2% more for unmarshal of the `Any` values. With all CPUs, fewer allocations count for more, because the garbage collector competes with the work for CPU time.

Two shapes are slower on one CPU:

- **Text that is not ASCII.** `jsontext` checks UTF-8 one character at a time for characters outside ASCII. For short strings, the gains on the rest of the message hide this cost. For long text, `protojson` is faster on one CPU. The cost is inside `jsontext`, and `jsonwire` cannot skip the check.
- **Unmarshal of long strings with many escape characters.** Both packages spend most of the time on the same unescaping work, and `jsontext` is a little slower at it.

These are edge cases. Most messages have many fields with short strings, numbers, and nested messages, and for these `jsonwire` is faster. The slowdown matters only when long text strings make up most of the message, for example messages that carry whole documents in Chinese, Japanese, or Korean. For such messages, measure both packages with real data.

## Tuning

### Turn off the duplicate name check

`encoding/json/v2` checks every JSON object for duplicate names by default. For large messages, this check is expensive. `jsonwire` already rejects duplicate fields and map keys, so the check is not necessary for messages:

```go
b, err := json.Marshal(v, jsontext.AllowDuplicateNames(true))
```

For the large message, marshal in a struct is 38% faster and unmarshal is 28% faster. The option applies to the whole call, so other types in the same value lose the check too.
