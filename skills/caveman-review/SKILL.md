---
name: caveman-review
description: >
  Comentarios de revisión de código ultra-comprimidos. Elimina ruido del feedback de PR
  preservando la señal accionable. Cada comentario es una línea: ubicación, problema, fix.
  Usar cuando el usuario diga "revisa este PR", "revisión de código", "revisa el diff", "/review",
  o invoque /caveman-review. Se activa automáticamente al revisar pull requests.
---

Escribir comentarios de revisión de código concisos y accionables. Una línea por hallazgo. Ubicación, problema, fix. Sin preámbulos.

## Reglas

**Formato:** `L<línea>: <problema>. <fix>.` — o `<archivo>:L<línea>: ...` al revisar diffs de múltiples archivos.

**Prefijo de severidad (opcional, cuando hay mezcla):**
- `🔴 bug:` — comportamiento roto, causará incidente
- `🟡 risk:` — funciona pero frágil (race condition, null check faltante, error ignorado)
- `🔵 nit:` — estilo, naming, micro-optimización. El autor puede ignorar
- `❓ q:` — pregunta genuina, no una sugerencia

**Eliminar:**
- "Noté que...", "Parece que...", "Quizás quieras considerar..."
- "Esto es solo una sugerencia pero..." — usar `nit:` en su lugar
- "¡Buen trabajo!", "En general se ve bien pero..." — decirlo una vez arriba, no por comentario
- Reexplicar lo que hace la línea — el revisor puede leer el diff
- Vacilaciones ("quizás", "tal vez", "creo que") — si hay duda usar `q:`

**Mantener:**
- Números de línea exactos
- Nombres exactos de símbolo/función/variable en backticks
- Fix concreto, no "considera refactorizar esto"
- El *por qué* si el fix no es obvio a partir del enunciado del problema

## Ejemplos

❌ "Noté que en la línea 42 no estás comprobando si el objeto user es nulo antes de acceder a la propiedad email. Esto podría potencialmente causar un crash si no se encuentra el usuario en la base de datos. Quizás quieras añadir una comprobación de nulo aquí."

✅ `L42: 🔴 bug: user puede ser null tras .find(). Añadir guard antes de .email.`

❌ "Parece que esta función hace muchas cosas y podría beneficiarse de dividirse en funciones más pequeñas para mejorar la legibilidad."

✅ `L88-140: 🔵 nit: función de 50 líneas hace 4 cosas. Extraer validate/normalize/persist.`

❌ "¿Has considerado qué pasa si la API devuelve 429? Creo que deberíamos manejar ese caso."

✅ `L23: 🟡 risk: sin retry en 429. Envolver en withBackoff(3).`

## Auto-Claridad

Abandonar modo conciso para: hallazgos de seguridad (bugs tipo CVE necesitan explicación completa + referencia), desacuerdos arquitectónicos (necesitan justificación, no solo una línea), y contextos de onboarding donde el autor es nuevo y necesita el "por qué". En esos casos escribir un párrafo normal, luego reanudar modo conciso para el resto.

## Límites

Solo revisiones — no escribe el fix del código, no aprueba/solicita-cambios, no ejecuta linters. Devolver el/los comentario(s) listo(s) para pegar en el PR. "stop caveman-review" o "modo normal": revertir al estilo de revisión verboso.
