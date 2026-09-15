# TOON and pixel context

TOON and pixel are optional context encodings. Both change model-visible input,
so neither is byte-safe. Use them only when their input shape and receiving
model are suitable.

## TOON

Token-Oriented Object Notation re-encodes structured data into a compact text
form. It is strongest on uniform tabular JSON, such as an array whose objects
share field names.

```json
[
  {"name":"Ada","role":"engineer"},
  {"name":"Lin","role":"designer"}
]
```

TOON can move repeated keys into a shared header and keep row values compact.
Exact syntax is defined by implementation and its test fixtures; callers should
use encoder and decoder instead of constructing TOON by hand.

### Selection rules

TOON runs only when explicitly requested or enabled through a feature gate. It
is not selected by Engine's general `Detect` function. Result must be smaller
than original representation.

TOON is not used for tool-call arguments. Changing tool arguments can alter
program behavior even when data appears structurally similar.

### CLI

```bash
caveman tools toon encode < data.json
caveman tools toon decode < data.toon
```

Decoder rejects malformed input rather than inventing missing structure.

### Suitable inputs

- uniform arrays of objects;
- repeated field names;
- scalar cell values;
- data consumed as context rather than executable arguments.

Avoid TOON for irregular nested objects, already compact data, inputs where key
order or byte representation matters, and tool-call arguments.

## Pixel context

Pixel converts text into a PNG image that a vision-capable model can read. It
can reduce text-token input for dense source material, but introduces optical
recognition and visual-layout risk.

Enable for one session:

```bash
caveman wrap --pixel <agent>
```

Pixel requires explicit model allowlisting:

```json
{
  "think": {
    "pixel": {
      "models": ["model-name"],
      "density": "balanced"
    }
  }
}
```

Supported density values are `conservative`, `balanced`, and `max`. Higher
density places more text into an image and can make small characters harder for
a model to read.

### Model compatibility

Vision capability alone is insufficient because image dimensions, detail
settings, provider token accounting, and text-reading quality differ. Caveman
does not infer support from model name.

### Recovery

Original text is stored through CCR before pixel output is emitted. If recovery
storage fails, original text remains in request. Image context should include a
clear recovery reference so tools can fetch exact source when character-level
detail matters.

## Evidence boundary

Smaller local representation does not establish lower provider cost because
providers count image and structured-text inputs differently. A valid claim
needs provider usage or documented benchmark for exact model; quality
equivalence also needs task evaluation beyond recovery availability.
