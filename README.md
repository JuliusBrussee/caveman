<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>por qué usar muchos token cuando pocos hacer el truco</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#antes--después">Antes/Después</a> •
  <a href="#instalar">Instalar</a> •
  <a href="#niveles-de-intensidad">Niveles</a> •
  <a href="#skills-caveman">Skills</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#evals">Evals</a>
</p>

---

Un skill/plugin para [Claude Code](https://docs.anthropic.com/en/docs/claude-code) y plugin para Codex que hace que el agente hable como cavernícola — recortando **~75% de los tokens de salida** manteniendo plena precisión técnica. Ahora con [modo 文言文](#modo-文言文-wenyan), [commits concisos](#caveman-commit), [revisiones de código de una línea](#caveman-review), y una [herramienta de compresión](#caveman-compress) que recorta **~46% de los tokens de entrada** en cada sesión.

Basado en la observación viral de que hablar como cavernícola reduce drásticamente el uso de tokens de LLM sin perder sustancia técnica. Así que lo convertimos en instalación de un solo comando.

## Antes / Después

<table>
<tr>
<td width="50%">

### 🗣️ Claude Normal (69 tokens)

> "The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object."

</td>
<td width="50%">

### 🪨 Claude Cavernícola (19 tokens)

> "Nueva ref objeto en cada render. Prop objeto inline = nueva ref = re-render. Envolver en `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Claude Normal

> "Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix."

</td>
<td>

### 🪨 Claude Cavernícola

> "Bug en auth middleware. Token expiry check usa `<` no `<=`. Fix:"

</td>
</tr>
</table>

**Mismo fix. 75% menos palabras. Cerebro sigue grande.**

**Elige tu nivel de gruñido:**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Tu componente hace re-render porque creas una nueva referencia de objeto en cada render. Las props de objeto inline fallan la comparación superficial cada vez. Envuélvelo en `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "Nueva ref objeto en cada render. Prop objeto inline = nueva ref = re-render. Envolver en `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Prop obj inline → nueva ref → re-render. `useMemo`."

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**Misma respuesta. Tú eliges cuántas palabras.**

```
┌─────────────────────────────────────┐
│  TOKENS AHORRADOS      ████████ 75% │
│  PRECISIÓN TÉCNICA     ████████ 100%│
│  AUMENTO DE VELOCIDAD  ████████ ~3x │
│  VIBRAS                ████████ OOG │
└─────────────────────────────────────┘
```

- **Respuesta más rápida** — menos tokens generar = velocidad ir brrrr
- **Más fácil de leer** — sin pared de texto, solo la respuesta
- **Misma precisión** — toda info técnica guardada, solo paja eliminada ([ciencia lo dice](https://arxiv.org/abs/2604.00025))
- **Ahorrar dinero** — ~71% menos tokens de salida = menos coste
- **Divertido** — cada revisión de código se vuelve comedia

## Instalar

Elige tu agente. Un comando. Listo.

| Agente | Instalación |
|--------|-------------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | Clonar repo → `/plugins` → Buscar "Caveman" → Instalar |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |
| **Cualquier otro** | `npx skills add JuliusBrussee/caveman` |

Instalar una vez. Usar en cada sesión para ese destino de instalación. Una piedra. Eso es todo.

### Qué obtienes

La auto-activación está integrada para Claude Code, Gemini CLI y la configuración local de Codex indicada abajo. `npx skills add` instala el skill para otros agentes, pero **no** instala archivos de reglas/instrucciones del repo, así que Caveman no arranca solo allí a menos que añadas el fragmento siempre activo de abajo.

| Función | Claude Code | Codex | Gemini CLI | Cursor | Windsurf | Cline | Copilot |
|---------|:-----------:|:-----:|:----------:|:------:|:--------:|:-----:|:-------:|
| Modo caveman | S | S | S | S | S | S | S |
| Auto-activar cada sesión | S | S¹ | S | —² | —² | —² | —² |
| Comando `/caveman` | S | S¹ | S | — | — | — | — |
| Cambio de modo (lite/full/ultra) | S | S¹ | S | S³ | S³ | — | — |
| Insignia statusline | S⁴ | — | — | — | — | — | — |
| caveman-commit | S | — | S | S | S | S | S |
| caveman-review | S | — | S | S | S | S | S |
| caveman-compress | S | S | S | S | S | S | S |
| caveman-help | S | — | S | S | S | S | S |

> [!NOTE]
> La auto-activación funciona diferente por agente: Claude Code usa hooks SessionStart, la configuración dogfood de Codex en este repo usa `.codex/hooks.json`, Gemini usa archivos de contexto. Cursor/Windsurf/Cline/Copilot pueden configurarse siempre activos, pero `npx skills add` instala solo el skill, no los archivos de reglas/instrucciones del repo.
>
> ¹ Codex usa sintaxis `$caveman`, no `/caveman`. Este repo incluye `.codex/hooks.json`, así que caveman arranca automáticamente al ejecutar Codex dentro del repo. El plugin instalado te da `$caveman`; copia el mismo hook en otro repo si quieres comportamiento siempre activo allí también. caveman-commit y caveman-review no están en el paquete del plugin de Codex — usa los archivos SKILL.md directamente.
> ² Añade el fragmento "¿Quieres que esté siempre activo?" de abajo al system prompt o archivo de reglas de esos agentes si quieres activación al inicio de sesión.
> ³ Cursor y Windsurf reciben el SKILL.md completo con todos los niveles de intensidad. El cambio de modo funciona bajo demanda vía el skill; sin comando slash.
> ⁴ Disponible en Claude Code, pero la instalación vía plugin solo sugiere la configuración. `install.sh` / `install.ps1` standalone lo configura automáticamente si no hay `statusLine` personalizada.

<details>
<summary><strong>Claude Code — detalles completos</strong></summary>

La instalación del plugin te da skills + hooks de carga automática. Si no hay `statusLine` personalizada configurada, Caveman anima a Claude a ofrecer configuración de la insignia en la primera sesión.

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin install caveman@caveman
```

**Hooks standalone (sin plugin):** Si prefieres no usar el sistema de plugins:
```bash
# macOS / Linux / WSL
bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.sh)

# Windows (PowerShell)
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.ps1 | iex
```

O desde un clon local: `bash hooks/install.sh` / `powershell -File hooks\install.ps1`

Desinstalar: `bash hooks/uninstall.sh` o `powershell -File hooks\uninstall.ps1`

**Insignia statusline:** Muestra `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, etc. en la barra de estado de Claude Code.

- **Instalación con plugin:** Si no tienes una `statusLine` personalizada, Claude debería ofrecer configurarla en la primera sesión
- **Instalación standalone:** Configurada automáticamente por `install.sh` / `install.ps1` a menos que ya tengas una statusline personalizada
- **Statusline personalizada:** El instalador deja tu statusline existente intacta. Ver [`hooks/README.md`](hooks/README.md) para el fragmento de fusión

</details>

<details>
<summary><strong>Codex — detalles completos</strong></summary>

**macOS / Linux:**
1. Clonar repo → Abrir Codex en el directorio del repo → `/plugins` → Buscar "Caveman" → Instalar

**Windows:**
1. Habilitar symlinks primero: `git config --global core.symlinks true` (requiere Modo Desarrollador o admin)
2. Clonar repo → Abrir VS Code → Configuración Codex → Plugins → encontrar "Caveman" en el marketplace local → Instalar → Recargar ventana

Este repo también incluye `.codex/hooks.json`, así que caveman se auto-activa mientras ejecutas Codex dentro del repo. El plugin instalado te da `$caveman`; si quieres comportamiento siempre activo en otros repos también, añade el mismo hook SessionStart allí.

</details>

<details>
<summary><strong>Gemini CLI — detalles completos</strong></summary>

```bash
gemini extensions install https://github.com/JuliusBrussee/caveman
```

Actualizar: `gemini extensions update caveman` · Desinstalar: `gemini extensions uninstall caveman`

Se auto-activa vía archivo de contexto `GEMINI.md`. También incluye comandos Gemini personalizados:
- `/caveman` — cambiar nivel de intensidad (lite/full/ultra/wenyan)
- `/caveman-commit` — generar mensaje de commit conciso
- `/caveman-review` — revisión de código de una línea

</details>

<details>
<summary><strong>Cursor / Windsurf / Cline / Copilot — detalles completos</strong></summary>

`npx skills add` instala solo el archivo del skill — **no** instala el archivo de reglas/instrucciones del agente, así que caveman no arranca solo. Para siempre activo, añade el fragmento "¿Quieres que esté siempre activo?" de abajo a las reglas o system prompt de tu agente.

| Agente | Comando | No instalado | Cambio de modo | Ubicación siempre activo |
|--------|---------|--------------|:--------------:|--------------------------|
| Cursor | `npx skills add JuliusBrussee/caveman -a cursor` | `.cursor/rules/caveman.mdc` | S | Reglas de Cursor |
| Windsurf | `npx skills add JuliusBrussee/caveman -a windsurf` | `.windsurf/rules/caveman.md` | S | Reglas de Windsurf |
| Cline | `npx skills add JuliusBrussee/caveman -a cline` | `.clinerules/caveman.md` | — | Reglas de Cline o system prompt |
| Copilot | `npx skills add JuliusBrussee/caveman -a github-copilot` | `.github/copilot-instructions.md` + `AGENTS.md` | — | Instrucciones personalizadas de Copilot |

Desinstalar: `npx skills remove caveman`

Copilot funciona con Chat, Edits y Coding Agent.

</details>

<details>
<summary><strong>Cualquier otro agente (opencode, Roo, Amp, Goose, Kiro, y más de 40 más)</strong></summary>

[npx skills](https://github.com/vercel-labs/skills) soporta más de 40 agentes:

```bash
npx skills add JuliusBrussee/caveman           # auto-detectar agente
npx skills add JuliusBrussee/caveman -a amp
npx skills add JuliusBrussee/caveman -a augment
npx skills add JuliusBrussee/caveman -a goose
npx skills add JuliusBrussee/caveman -a kiro-cli
npx skills add JuliusBrussee/caveman -a roo
# ... y muchos más
```

Desinstalar: `npx skills remove caveman`

> **Nota Windows:** `npx skills` usa symlinks por defecto. Si los symlinks fallan, añade `--copy`: `npx skills add JuliusBrussee/caveman --copy`

**Importante:** Estos agentes no tienen sistema de hooks, así que caveman no arrancará solo. Di `/caveman` o "habla como cavernícola" para activar en cada sesión.

**¿Quieres que esté siempre activo?** Pega esto en el system prompt o archivo de reglas de tu agente — caveman estará activo desde el primer mensaje, en cada sesión:

```
Conciso como cavernícola. Sustancia técnica exacta. Solo paja muere.
Eliminar: artículos, relleno (just/really/basically), formalidades, vacilaciones.
Fragmentos OK. Sinónimos cortos. Código sin cambios.
Patrón: [cosa] [acción] [razón]. [siguiente paso].
ACTIVO EN CADA RESPUESTA. No revertir tras muchos turnos. Sin deriva de relleno.
Código/commits/PRs: normal. Desactivar: "stop caveman" / "modo normal".
```

Dónde ponerlo:
| Agente | Archivo |
|--------|---------|
| opencode | `.config/opencode/AGENTS.md` |
| Roo | `.roo/rules/caveman.md` |
| Amp | system prompt de tu workspace |
| Otros | system prompt o archivo de reglas de tu agente |

</details>

## Uso

Activar con:
- `/caveman` o `$caveman` en Codex
- "habla como cavernícola"
- "modo caveman"
- "menos tokens por favor"

Desactivar con: "stop caveman" o "modo normal"

### Niveles de Intensidad

| Nivel | Activar | Qué hace |
|-------|---------|----------|
| **Lite** | `/caveman lite` | Elimina relleno, mantiene gramática. Profesional pero sin paja |
| **Full** | `/caveman full` | Caveman predeterminado. Elimina artículos, fragmentos, gruñido completo |
| **Ultra** | `/caveman ultra` | Compresión máxima. Telegráfico. Abreviar todo |

### Modo 文言文 (Wenyan)

Compresión literaria en chino clásico — misma precisión técnica, pero en el lenguaje escrito más eficiente en tokens que los humanos han inventado.

| Nivel | Activar | Qué hace |
|-------|---------|----------|
| **Wenyan-Lite** | `/caveman wenyan-lite` | Semi-clásico. Gramática intacta, relleno eliminado |
| **Wenyan-Full** | `/caveman wenyan` | 文言文 completo. Máxima tersura clásica |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | Extremo. Erudito antiguo con presupuesto ajustado |

Nivel persiste hasta que lo cambies o finalice la sesión.

## Skills Caveman

| Skill | Qué hace | Activar |
|-------|----------|---------|
| **caveman-commit** | Mensajes de commit concisos. Conventional Commits. Sujeto ≤50 chars. Por qué antes que qué. | `/caveman-commit` |
| **caveman-review** | Comentarios de PR de una línea: `L42: 🔴 bug: user nulo. Añadir guard.` Sin preámbulos. | `/caveman-review` |
| **caveman-help** | Tarjeta de referencia rápida. Todos los modos, skills, comandos, a un comando de distancia. | `/caveman-help` |

### caveman-compress

Caveman hace que Claude *hable* con menos tokens. **Compress** hace que Claude *lea* menos tokens.

Tu `CLAUDE.md` carga en **cada inicio de sesión**. Caveman Compress reescribe los archivos de memoria en estilo caveman para que Claude lea menos — sin que pierdas el original legible para humanos.

```
/caveman:compress CLAUDE.md
```

```
CLAUDE.md          ← comprimido (Claude lee esto en cada sesión — menos tokens)
CLAUDE.original.md ← backup legible (tú lees y editas esto)
```

| Archivo | Original | Comprimido | Ahorro |
|---------|----------:|----------:|-------:|
| `claude-md-preferences.md` | 706 | 285 | **59,6%** |
| `project-notes.md` | 1145 | 535 | **53,3%** |
| `claude-md-project.md` | 1122 | 636 | **43,3%** |
| `todo-list.md` | 627 | 388 | **38,1%** |
| `mixed-with-code.md` | 888 | 560 | **36,9%** |
| **Media** | **898** | **481** | **46%** |

Bloques de código, URLs, rutas de archivo, comandos, headings, fechas, números de versión — todo lo técnico pasa sin tocar. Solo la prosa se comprime. Ver el [README completo de caveman-compress](caveman-compress/README.md) para más detalles. [Nota de seguridad](./caveman-compress/SECURITY.md): Snyk marca esto como Alto Riesgo por patrones de subprocess/archivos — es un falso positivo.

## Benchmarks

Recuentos de tokens reales de la API de Claude ([reprodúcelo tú mismo](benchmarks/)):

<!-- BENCHMARK-TABLE-START -->
| Tarea | Normal (tokens) | Caveman (tokens) | Ahorro |
|-------|---------------:|----------------:|-------:|
| Explicar bug de re-render en React | 1180 | 159 | 87% |
| Arreglar expiración de token en auth middleware | 704 | 121 | 83% |
| Configurar pool de conexiones PostgreSQL | 2347 | 380 | 84% |
| Explicar git rebase vs merge | 702 | 292 | 58% |
| Refactorizar callback a async/await | 387 | 301 | 22% |
| Arquitectura: microservicios vs monolito | 446 | 310 | 30% |
| Revisar PR por problemas de seguridad | 678 | 398 | 41% |
| Build multi-stage de Docker | 1042 | 290 | 72% |
| Depurar race condition en PostgreSQL | 1200 | 232 | 81% |
| Implementar error boundary en React | 3454 | 456 | 87% |
| **Media** | **1214** | **294** | **65%** |

*Rango: 22%–87% de ahorro según el prompt.*
<!-- BENCHMARK-TABLE-END -->

> [!IMPORTANT]
> Caveman solo afecta a tokens de salida — los tokens de razonamiento/thinking no se tocan. Caveman no hace cerebro más pequeño. Caveman hace *boca* más pequeña. La mayor ganancia es **legibilidad y velocidad**, el ahorro de coste es un bonus.

Un paper de marzo de 2026 ["Brevity Constraints Reverse Performance Hierarchies in Language Models"](https://arxiv.org/abs/2604.00025) encontró que limitar a los modelos grandes a respuestas breves **mejoró la precisión en 26 puntos porcentuales** en ciertos benchmarks e invirtió completamente las jerarquías de rendimiento. Verboso no siempre mejor. A veces menos palabra = más correcto.

## Evals

Caveman no solo afirma 75%. Caveman lo **demuestra**.

El directorio `evals/` tiene un harness de evaluación de tres ramas que mide la compresión real de tokens frente a un control adecuado — no solo "verboso vs skill" sino "conciso vs skill". Porque comparar caveman con Claude verboso confunde el skill con tersura genérica. Eso es hacer trampa. Caveman no hace trampa.

```bash
# Ejecutar eval (necesita CLI de claude)
uv run python evals/llm_run.py

# Leer resultados (sin API key, ejecuta offline)
uv run --with tiktoken python evals/measure.py
```

## Dale una estrella al repo

Si caveman ahorra muchos tokens, muchos euros — dejar muchas estrellas. ⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

## También por Julius Brussee

- **[Cavekit](https://github.com/JuliusBrussee/cavekit)** — desarrollo guiado por especificaciones para Claude Code. Lenguaje caveman → specs → builds paralelos → software funcionando.
- **[Revu](https://github.com/JuliusBrussee/revu-swift)** — app de estudio local-first para macOS con repetición espaciada FSRS, mazos, exámenes y guías de estudio. [revu.cards](https://revu.cards)

## Licencia

MIT — libre como mamut en llanura abierta.
