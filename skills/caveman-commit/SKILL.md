---
name: caveman-commit
description: >
  Generador de mensajes de commit ultra-comprimidos. Elimina ruido de los mensajes de commit
  preservando la intención y el razonamiento. Formato Conventional Commits. Sujeto ≤50 chars,
  cuerpo solo cuando el "por qué" no es obvio. Usar cuando el usuario diga "escribe un commit",
  "mensaje de commit", "generar commit", "/commit", o invoque /caveman-commit.
  Se activa automáticamente al preparar cambios para staging.
---

Escribir mensajes de commit concisos y exactos. Formato Conventional Commits. Sin relleno. Por qué antes que qué.

## Reglas

**Línea de asunto:**
- `<tipo>(<alcance>): <resumen imperativo>` — `<alcance>` opcional
- Tipos: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `build`, `ci`, `style`, `revert`
- Modo imperativo: "add", "fix", "remove" — no "added", "adds", "adding"
- ≤50 chars cuando sea posible, máximo absoluto 72
- Sin punto final
- Seguir la convención del proyecto para mayúsculas tras los dos puntos

**Cuerpo (solo si es necesario):**
- Omitir completamente cuando el asunto se explica solo
- Añadir cuerpo solo para: *por qué* no obvio, cambios breaking, notas de migración, issues enlazados
- Ajustar a 72 chars
- Viñetas `-` no `*`
- Referencias a issues/PRs al final: `Closes #42`, `Refs #17`

**Lo que NUNCA va:**
- "This commit does X", "I", "we", "now", "currently" — el diff dice el qué
- "As requested by..." — usar trailer Co-authored-by
- "Generated with Claude Code" o cualquier atribución a IA
- Emoji (a menos que la convención del proyecto lo requiera)
- Repetir el nombre del archivo cuando el alcance ya lo dice

## Ejemplos

Diff: nuevo endpoint de perfil de usuario con cuerpo explicando el por qué
- ❌ "feat: add a new endpoint to get user profile information from the database"
- ✅
  ```
  feat(api): add GET /users/:id/profile

  Mobile client needs profile data without the full user payload
  to reduce LTE bandwidth on cold-launch screens.

  Closes #128
  ```

Diff: cambio breaking de API
- ✅
  ```
  feat(api)!: rename /v1/orders to /v1/checkout

  BREAKING CHANGE: clients on /v1/orders must migrate to /v1/checkout
  before 2026-06-01. Old route returns 410 after that date.
  ```

## Auto-Claridad

Incluir siempre cuerpo para: cambios breaking, fixes de seguridad, migraciones de datos, cualquier commit que revierta uno anterior. Nunca comprimir estos en solo asunto — los futuros depuradores necesitan el contexto.

## Límites

Solo genera el mensaje de commit. No ejecuta `git commit`, no hace staging de archivos, no amend. Devolver el mensaje como bloque de código listo para pegar. "stop caveman-commit" o "modo normal": revertir al estilo de commit verboso.
