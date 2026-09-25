"""Observe native iterators without keeping ownership in consumer code."""
import asyncio

from ._native import owner


def observe_iterator(iterator, attempt):
    if attempt is None:
        yield from iterator
        return
    attempt.observe("dispatch_intent")
    last = None
    try:
        while True:
            token = owner.set(attempt)
            try:
                value = next(iterator)
            except StopIteration:
                from ._usage import langchain_usage
                attempt.observe("completed", langchain_usage(last))
                return
            finally:
                owner.reset(token)
            last = getattr(value, "usage_metadata", None) or last
            yield value
    except GeneratorExit:
        attempt.observe("cancelled")
        raise
    except BaseException:
        attempt.observe("failed")
        raise
    finally:
        if hasattr(iterator, "close"):
            iterator.close()


async def observe_async_iterator(iterator, attempt):
    if attempt is None:
        try:
            async for value in iterator:
                yield value
        finally:
            if hasattr(iterator, "aclose"):
                await iterator.aclose()
        return
    attempt.observe("dispatch_intent")
    last = None
    try:
        while True:
            token = owner.set(attempt)
            try:
                value = await anext(iterator)
            except StopAsyncIteration:
                from ._usage import langchain_usage
                attempt.observe("completed", langchain_usage(last))
                return
            finally:
                owner.reset(token)
            last = getattr(value, "usage_metadata", None) or last
            yield value
    except (asyncio.CancelledError, GeneratorExit):
        attempt.observe("cancelled")
        raise
    except BaseException:
        attempt.observe("failed")
        raise
    finally:
        if hasattr(iterator, "aclose"):
            await iterator.aclose()
