# TypeScript Generics

## Task

Write a TypeScript function `groupBy<T, K extends keyof T>(items: T[], key: K): Record<string, T[]>` that groups an array of objects by a given key. It should be fully type-safe — the key parameter should only accept keys of T, and the compiler should catch invalid keys at build time.

Include an example usage with an array of `{ name: string, department: string, level: number }`.
