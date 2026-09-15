"""Bounded Google JSON string offsets; untouched serialized text stays exact."""
import json


def parse(text):
    if len(text) > 2 << 20:
        return None
    decoder, strings, index, nodes = json.JSONDecoder(), {}, 0, 0

    def white():
        nonlocal index
        while index < len(text) and text[index] in " \t\r\n":
            index += 1

    def walk(path, depth=0):
        nonlocal index, nodes
        nodes += 1
        if depth > 64 or nodes > 65536:
            raise ValueError("bounded")
        white()
        start = index
        if text[index] == '"':
            value, index = decoder.raw_decode(text, index)
            strings[path] = (start, index, value)
            return value
        if text[index] == "{":
            index += 1
            result = {}
            white()
            if text[index] == "}":
                index += 1
                return result
            while True:
                white()
                key, index = decoder.raw_decode(text, index)
                if type(key) is not str or key in result:
                    raise ValueError("duplicate")
                white()
                if text[index] != ":":
                    raise ValueError("colon")
                index += 1
                result[key] = walk((*path, key), depth + 1)
                white()
                end = text[index]
                index += 1
                if end == "}":
                    return result
                if end != ",":
                    raise ValueError("comma")
        if text[index] == "[":
            index += 1
            result = []
            white()
            if text[index] == "]":
                index += 1
                return result
            while True:
                result.append(walk((*path, len(result)), depth + 1))
                white()
                end = text[index]
                index += 1
                if end == "]":
                    return result
                if end != ",":
                    raise ValueError("comma")
        value, index = decoder.raw_decode(text, index)
        return value

    try:
        value = walk(())
        white()
        return (value, strings) if index == len(text) else None
    except (ValueError, IndexError, RecursionError):
        return None


def patch(text, replacements):
    end = len(text)
    for (start, stop, original), replacement in sorted(replacements, reverse=True):
        if start < 0 or stop > end or start >= stop or json.loads(text[start:stop]) != original:
            return None
        text = text[:start] + json.dumps(replacement, ensure_ascii=False) + text[stop:]
        end = start
    return text
