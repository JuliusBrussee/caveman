---
name: caveman-test
description: >
  Ultra-compressed test-case generator. Cuts noise from test names and setup
  while preserving full coverage intent. One assertion focus per test, terse
  arrange/act/assert. Matches repo's existing test framework and style. Use
  when user says "write tests", "add test coverage", "test this function",
  "/test", or invokes /caveman-test. Auto-triggers when new function/module
  lacks tests.
---

Write tests terse and exact. One behavior per test. No filler setup, no redundant assertions.

## Rules

**Test name:** `<unit>_<condition>_<expected>` or framework's native `describe/it` nesting — match whatever convention the repo already uses. State condition and expected outcome, nothing else.

- ❌ `test_that_the_function_works_correctly()`
- ✅ `parseAmount_negativeInput_throwsRangeError()`

**Structure:** Arrange / Act / Assert, each on its own line(s), no blank-line ceremony beyond what improves readability. No comments labeling "// arrange" / "// act" / "// assert" unless repo convention already uses them.

**Coverage, not padding:**
- One logical assertion focus per test — split multi-behavior tests
- Cover: happy path, boundary values, error/invalid input, and any branch visible in the diff
- Skip redundant permutations that don't exercise new logic
- Reuse existing fixtures/factories/mocks already in the repo — don't reinvent them

**Drop:**
- Explanatory comments restating the assertion ("// check that result is 5")
- Mock setup for dependencies the test doesn't touch
- `console.log` / debug prints
- Snapshot tests for logic that has a clear expected value — assert the value directly

**Keep:**
- Exact expected values, not `toBeTruthy()` when the value is knowable
- Edge cases implied by the code's own branches (null, empty, zero, max, off-by-one)
- Async/error assertions in the framework's idiomatic form (`assertRaises`, `expect().rejects`, `try/fail/catch`)

## Examples

Function: `divide(a, b)` throws on `b === 0`.

❌
```python
def test_divide():
    # test that divide works
    result = divide(10, 2)
    assert result == 5
    # test division by zero
    try:
        divide(10, 0)
        assert False, "should have raised"
    except Exception as e:
        assert True
```

✅
```python
def test_divide_normal_returnsQuotient():
    assert divide(10, 2) == 5

def test_divide_byZero_raisesValueError():
    with pytest.raises(ValueError):
        divide(10, 0)
```

## Auto-Clarity

Drop terse mode for: security-sensitive logic (auth, crypto, payment) — write full docstring explaining the threat model each test guards against; flaky-prone async/timing tests — explain the race being tested, not just assert it; and any test whose purpose isn't obvious from its name alone.

## Boundaries

Writes test code only — does not modify the implementation under test, does not run the test suite, does not install test frameworks or fixtures not already in the repo. Output the test file/block ready to paste. "stop caveman-test" or "normal mode": revert to verbose test-writing style.
