# Evals

Mide la compresión real de tokens de los skills caveman ejecutando los mismos
prompts a través de Claude Code bajo tres condiciones y comparando los
recuentos de tokens de salida generados.

## Las tres ramas

| Rama | System prompt |
|------|--------------|
| `__baseline__` | ninguno |
| `__terse__` | `Answer concisely.` |
| `<skill>` | `Answer concisely.\n\n{SKILL.md}` |

El delta honesto para cualquier skill es **`<skill>` vs `__terse__`** — es decir,
cuánto añade el skill por encima de una instrucción simple de "sé conciso".
Comparar un skill con el baseline sin system prompt confunde el skill
con la petición de tersura genérica, que es lo que hacía una versión anterior
de este harness y por eso sus números estaban inflados.

## Por qué este diseño

- **Salida LLM real**, no ejemplos escritos a mano (sin circularidad).
- **El mismo Claude Code** que los skills tienen como objetivo — sin API key separada.
- **Snapshot guardado en git** para que las ejecuciones de CI sean deterministas y gratuitas,
  y para que cualquier cambio en los números sea revisable como diff.
- **Rama de control** aísla la contribución del skill del efecto genérico
  "sé conciso".

## Archivos

- `prompts/en.txt` — lista fija de preguntas de desarrollo, una por línea.
- `llm_run.py` — ejecuta `claude -p --system-prompt …` por (prompt, rama),
  captura salida LLM real, escribe `snapshots/results.json` junto con
  metadatos (modelo, versión CLI, timestamp de generación).
- `measure.py` — lee el snapshot, cuenta tokens con tiktoken
  `o200k_base`, imprime tabla markdown con mediana / media / mín / máx /
  desviación estándar por prompts.
- `snapshots/results.json` — fuente de verdad guardada, regenerada solo
  cuando cambian los archivos SKILL.md o los prompts.

## Regenerar el snapshot (requiere CLI `claude` con sesión activa)

```bash
uv run python evals/llm_run.py
```

Esto llama a Claude una vez por prompt × (N skills + 2 ramas de control). Usar
un modelo pequeño para mantener el coste bajo:

```bash
CAVEMAN_EVAL_MODEL=claude-haiku-4-5 uv run python evals/llm_run.py
```

## Leer el snapshot (sin LLM, sin API key, ejecuta en CI)

```bash
uv run --with tiktoken python evals/measure.py
```

## Añadir un prompt

Añadir una línea a `prompts/en.txt`, luego regenerar el snapshot.

## Añadir un skill

Depositar un `skills/<nombre>/SKILL.md`, luego regenerar el snapshot. `llm_run.py`
detecta automáticamente todos los directorios de skills.

## Qué NO mide esto

- **Fidelidad** — ¿preserva la respuesta comprimida las afirmaciones técnicas?
  Un skill que responda `k` a todo puntuaría −99% y "ganaría". Una v2 futura
  podría añadir una rúbrica de modelo-juez.
- **Latencia o coste** — fuera de alcance. Nótese que los skills añaden tokens de
  entrada en cada llamada, así que el ahorro en salida no es la imagen económica completa.
- **Comportamiento entre modelos** — solo se mide el modelo usado para generar el snapshot.
- **Tokens exactos de Claude** — `tiktoken o200k_base` es el BPE de OpenAI y es
  solo una aproximación del tokenizador de Claude. Las ratios entre ramas son
  significativas; los números absolutos son aproximados.
- **Significancia estadística** — ejecución única por (prompt, rama) a temperatura
  predeterminada. Las columnas mín/máx/desviación permiten estimar si un número
  es sólido o ruidoso, pero esto no es un experimento con potencia estadística.
