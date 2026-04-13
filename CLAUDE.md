# CLAUDE.md — caveman

## El README es un artefacto de producto

README = puerta de entrada al producto. Lo leen personas no técnicas para decidir si vale la pena instalar caveman. Trátalo como copy de UI.

**Reglas para cualquier cambio en el README:**

- Legible por usuarios no-IA. Si escribes "el hook SessionStart inyecta contexto de sistema", es invisible para la mayoría — tradúcelo a lenguaje normal.
- Mantén los ejemplos Antes/Después primero. Ese es el argumento de venta.
- La tabla de instalación siempre completa y exacta. Un comando de instalación roto cuesta un usuario real.
- La tabla "Qué obtienes" debe sincronizarse con el código real. Una función sale o se elimina → actualiza la tabla.
- Preserva la voz. El estilo caveman en el README es intencionado. "Cerebro sigue grande." "Coste baja para siempre." "Una piedra. Eso es todo." — marca intencional. No lo normalices.
- Números de benchmark de ejecuciones reales en `benchmarks/` y `evals/`. Nunca inventar ni redondear. Reejecutar si hay duda.
- Añadir nuevo agente a la tabla de instalación → añadir bloque de detalle en la sección `<details>` más abajo.
- Verificar legibilidad antes de cualquier commit al README: ¿podría un no-programador entender e instalar en 60 segundos?

---

## Visión general del proyecto

Caveman hace que los agentes de codificación IA respondan en prosa comprimida estilo caveman — recorta ~65-75% de tokens de salida, con plena precisión técnica. Se distribuye como plugin de Claude Code, plugin de Codex, extensión de Gemini CLI, archivos de reglas para Cursor, Windsurf, Cline, Copilot y más de 40 agentes vía `npx skills`.

---

## Estructura de archivos y responsabilidades

### Archivos fuente de verdad — editar solo estos

| Archivo | Qué controla |
|---------|--------------|
| `skills/caveman/SKILL.md` | Comportamiento caveman: niveles de intensidad, reglas, modo wenyan, auto-claridad, persistencia. Único archivo a editar para cambios de comportamiento. |
| `rules/caveman-activate.md` | Cuerpo de regla de auto-activación siempre activa. CI lo inyecta en archivos de reglas de Cursor, Windsurf, Cline, Copilot. Editar aquí, no en copias específicas de cada agente. |
| `skills/caveman-commit/SKILL.md` | Comportamiento de mensajes de commit caveman. Skill completamente independiente. |
| `skills/caveman-review/SKILL.md` | Comportamiento de revisión de código caveman. Skill completamente independiente. |
| `skills/caveman-help/SKILL.md` | Tarjeta de referencia rápida. Visualización única, no es un modo persistente. |
| `caveman-compress/SKILL.md` | Comportamiento del sub-skill compress. |

### Auto-generados / auto-sincronizados — no editar directamente

CI los sobreescribe al hacer push a main cuando cambian las fuentes. Las ediciones aquí se pierden.

| Archivo | Sincronizado desde |
|---------|-------------------|
| `caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `plugins/caveman/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `.cursor/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `.windsurf/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `caveman.skill` | ZIP del directorio `skills/caveman/` |
| `.clinerules/caveman.md` | `rules/caveman-activate.md` |
| `.github/copilot-instructions.md` | `rules/caveman-activate.md` |
| `.cursor/rules/caveman.mdc` | `rules/caveman-activate.md` + frontmatter de Cursor |
| `.windsurf/rules/caveman.md` | `rules/caveman-activate.md` + frontmatter de Windsurf |

---

## Flujo de trabajo de sincronización CI

`.github/workflows/sync-skill.yml` se dispara al hacer push a main cuando cambian `skills/caveman/SKILL.md` o `rules/caveman-activate.md`.

Qué hace:
1. Copia `skills/caveman/SKILL.md` a todas las ubicaciones SKILL.md de cada agente
2. Reconstruye `caveman.skill` como ZIP de `skills/caveman/`
3. Reconstruye todos los archivos de reglas de agentes desde `rules/caveman-activate.md`, añadiendo frontmatter específico de cada agente (Cursor necesita `alwaysApply: true`, Windsurf necesita `trigger: always_on`)
4. Hace commit y push con `[skip ci]` para evitar bucles

CI hace commit como `github-actions[bot]`. Tras fusionar un PR, esperar al workflow antes de declarar la release completa.

---

## Sistema de hooks (Claude Code)

Tres hooks en `hooks/`. Se comunican mediante archivo de bandera en `~/.claude/.caveman-active`.

```
Hook SessionStart ──escribe "full"──▶ ~/.claude/.caveman-active ◀──escribe modo── Hook UserPromptSubmit
                                               │
                                            lee
                                               ▼
                                      caveman-statusline.sh
                                     [CAVEMAN] / [CAVEMAN:ULTRA] / ...
```

### `hooks/caveman-activate.js` — Hook SessionStart

Se ejecuta una vez al inicio de cada sesión de Claude Code. Hace tres cosas:
1. Escribe `"full"` en `~/.claude/.caveman-active` (lo crea si no existe)
2. Emite el conjunto de reglas caveman como stdout oculto — Claude Code inyecta el stdout del hook SessionStart como contexto de sistema, invisible para el usuario
3. Verifica `~/.claude/settings.json` en busca de configuración de statusline; si falta, añade un aviso para ofrecer configuración en la primera interacción

Falla en silencio ante todos los errores del sistema de archivos — nunca bloquea el inicio de sesión.

### `hooks/caveman-mode-tracker.js` — Hook UserPromptSubmit

Lee JSON desde stdin. Comprueba si el prompt empieza con `/caveman`. Si es así, escribe el modo en el archivo de bandera:
- `/caveman` → predeterminado configurado (ver `caveman-config.js`, por defecto `full`)
- `/caveman lite` → `lite`
- `/caveman ultra` → `ultra`
- `/caveman wenyan` o `/caveman wenyan-full` → `wenyan`
- `/caveman wenyan-lite` → `wenyan-lite`
- `/caveman wenyan-ultra` → `wenyan-ultra`
- `/caveman-commit` → `commit`
- `/caveman-review` → `review`
- `/caveman-compress` → `compress`

Detecta "stop caveman" o "modo normal" en el prompt y borra el archivo de bandera.

### `hooks/caveman-statusline.sh` — Insignia de statusline

Lee el archivo de bandera. Devuelve cadena de insignia coloreada para la statusline de Claude Code:
- `full` o vacío → `[CAVEMAN]` (naranja)
- cualquier otro → `[CAVEMAN:<MODO_EN_MAYÚSCULAS>]` (naranja)

Se configura en `~/.claude/settings.json` bajo `statusLine.command`.

### Instalación de hooks

**Instalación vía plugin** — los hooks se conectan automáticamente mediante el sistema de plugins.

**Instalación independiente** — `hooks/install.sh` (macOS/Linux) o `hooks/install.ps1` (Windows) copia los archivos de hooks en `~/.claude/hooks/` y parchea `~/.claude/settings.json` para registrar los hooks SessionStart y UserPromptSubmit además de la statusline.

**Desinstalación** — `hooks/uninstall.sh` / `hooks/uninstall.ps1` elimina los archivos de hooks y parchea settings.json.

---

## Sistema de skills

Skills = archivos Markdown con frontmatter YAML consumidos por el sistema de skills/plugins de Claude Code y por `npx skills` para otros agentes.

### Niveles de intensidad

Definidos en `skills/caveman/SKILL.md`. Seis niveles: `lite`, `full` (predeterminado), `ultra`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`. Persiste hasta que se cambie o finalice la sesión.

### Regla de auto-claridad

Caveman vuelve a prosa normal para: advertencias de seguridad, confirmaciones de acciones irreversibles, secuencias de múltiples pasos donde la ambigüedad de fragmentos implica riesgo de malinterpretación, usuario confundido o repitiendo pregunta. Reanuda después. Definido en el skill — preservar en cualquier edición de SKILL.md.

### caveman-compress

Sub-skill en `caveman-compress/SKILL.md`. Toma una ruta de archivo, comprime la prosa a estilo caveman, escribe en la ruta original, guarda copia de seguridad en `<filename>.original.md`. Valida que headings, bloques de código, URLs, rutas de archivo y comandos se preserven. Reintenta hasta 2 veces ante fallos con parches específicos únicamente. Requiere Python 3.10+.

### caveman-commit / caveman-review

Skills independientes en `skills/caveman-commit/SKILL.md` y `skills/caveman-review/SKILL.md`. Ambos tienen frontmatter propio con `description` y `name` para cargarse de forma independiente. caveman-commit: Conventional Commits, sujeto ≤50 caracteres. caveman-review: comentarios de una línea en formato `L<línea>: <severidad> <problema>. <solución>.`

---

## Distribución por agente

Cómo llega caveman a cada tipo de agente:

| Agente | Mecanismo | ¿Auto-activa? |
|--------|-----------|---------------|
| Claude Code | Plugin (hooks + skills) o hooks independientes | Sí — hook SessionStart inyecta reglas |
| Codex | Plugin en `plugins/caveman/` con `hooks.json` | Sí — hook SessionStart |
| Gemini CLI | Extensión con archivo de contexto `GEMINI.md` | Sí — el archivo de contexto carga cada sesión |
| Cursor | `.cursor/rules/caveman.mdc` con `alwaysApply: true` | Sí — regla siempre activa |
| Windsurf | `.windsurf/rules/caveman.md` con `trigger: always_on` | Sí — regla siempre activa |
| Cline | `.clinerules/caveman.md` (auto-descubierto) | Sí — Cline inyecta todos los archivos .clinerules |
| Copilot | `.github/copilot-instructions.md` + `AGENTS.md` | Sí — instrucciones para todo el repositorio |
| Otros | `npx skills add JuliusBrussee/caveman` | No — el usuario debe ejecutar `/caveman` cada sesión |

Para agentes sin sistema de hooks, el fragmento mínimo siempre activo vive en el README bajo "¿Quieres que esté siempre activo?" — mantener actualizado con `rules/caveman-activate.md`.

---

## Evaluaciones

`evals/` tiene un harness de tres ramas:
- `__baseline__` — sin system prompt
- `__terse__` — `Answer concisely.`
- `<skill>` — `Answer concisely.\n\n{SKILL.md}`

Delta honesto = **skill vs terse**, no skill vs baseline. La comparación con baseline confunde el skill con terseness genérica — eso es hacer trampa. El harness está diseñado para evitarlo.

`llm_run.py` llama a `claude -p --system-prompt ...` por (prompt, rama), guarda en `evals/snapshots/results.json`. `measure.py` lee el snapshot offline con tiktoken (BPE de OpenAI — aproxima el tokenizador de Claude; las ratios son significativas, los números absolutos son aproximados).

Añadir skill: depositar `skills/<nombre>/SKILL.md`. El harness lo descubre automáticamente. Añadir prompt: añadir línea a `evals/prompts/en.txt`.

Snapshots guardados en git. CI los lee sin llamadas a la API. Solo regenerar cuando cambie SKILL.md o los prompts.

---

## Benchmarks

`benchmarks/` ejecuta prompts reales a través de la API de Claude (no la CLI de Claude Code), registra recuentos de tokens reales. Resultados guardados como JSON en `benchmarks/results/`. La tabla de benchmarks en el README se genera a partir de los resultados — actualizar al regenerar.

Para reproducir: `uv run python benchmarks/run.py` (necesita `ANTHROPIC_API_KEY` en `.env.local`).

---

## Reglas clave para agentes que trabajen aquí

- Editar `skills/caveman/SKILL.md` para cambios de comportamiento. Nunca editar las copias sincronizadas.
- Editar `rules/caveman-activate.md` para cambios en la regla de auto-activación. Nunca editar las copias específicas de cada agente.
- El README es el archivo más importante para el impacto en el usuario. Optimizar para lectores no técnicos. Preservar la voz caveman.
- Los números de benchmark y evaluación deben ser reales. Nunca fabricar ni estimar.
- El workflow de CI hace commit de vuelta a main tras la fusión. Tenerlo en cuenta al verificar el estado de la rama.
- Los archivos de hooks deben fallar en silencio ante todos los errores del sistema de archivos. Nunca dejar que un crash del hook bloquee el inicio de sesión.
