# caveman-test

Terse, high-coverage test generation. One behavior per test, no filler.

## What it does

Generates test cases matching the repo's existing test framework and conventions. Each test targets one logical behavior — happy path, boundary value, error case, or a branch visible in the diff. Test names state the condition and expected outcome directly. No redundant setup, no comments restating the assertion, no debug prints.

Writes test code only. Does not modify the implementation, does not run the suite, does not install new test dependencies.

## How to invoke

```
/caveman-test
```

Also triggers on phrases like "write tests", "add test coverage", "test this function".

## Example output

Function: `divide(a, b)` throws on `b === 0`.

```python
def test_divide_normal_returnsQuotient():
    assert divide(10, 2) == 5

def test_divide_byZero_raisesValueError():
    with pytest.raises(ValueError):
        divide(10, 0)
```

## See also

- [`SKILL.md`](./SKILL.md) — full LLM-facing instructions
- [Caveman README](../../README.md) — repo overview
