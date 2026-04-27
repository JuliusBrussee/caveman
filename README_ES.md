<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>¿por qué usar muchos tokens cuando pocos bastan?</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#antes--después">Antes/Después</a> •
  <a href="#instalación">Instalación</a> •
  <a href="#niveles-de-intensidad">Niveles</a> •
  <a href="#habilidades-caveman">Habilidades</a> •
  <a href="#benchmarks">Benchmarks</a>
</p>

---

Una habilidad/plugin para [Claude Code](https://docs.anthropic.com/en/docs/claude-code) y plugin de Codex que hace que el agente hable como un cavernícola — reduciendo **~75% de los tokens de salida** manteniendo la precisión técnica completa. Ahora con [modo 文言文](#文言文-modo-wenyan), [commits concisos](#caveman-commit), [revisiones de código en una línea](#caveman-review), y una [herramienta de compresión](#caveman-compress) que reduce **~46% de los tokens de entrada** en cada sesión.

Basado en la observación viral de que el habla cavernícola reduce drásticamente el uso de tokens en LLMs sin perder sustancia técnica. Lo convertimos en una instalación de una sola línea.

## Antes / Después

<table>
<tr>
<td width="50%">

### 🗣️ Claude Normal (69 tokens)

> "La razón por la que tu componente React se está volviendo a renderizar es probablemente porque estás creando una nueva referencia de objeto en cada ciclo de renderizado. Cuando pasas un objeto en línea como prop, la comparación superficial de React lo ve como un objeto diferente cada vez, lo que activa un re-renderizado. Recomendaría usar useMemo para memorizar el objeto."

</td>
<td width="50%">

### 🪨 Claude Cavernícola (19 tokens)

> "Nueva ref objeto cada render. Prop objeto línea = nueva ref = re-render. Usar `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Claude Normal

> "¡Claro! Estaré encantado de ayudarte con eso. El problema que estás experimentando es probablemente causado por tu middleware de autenticación que no valida correctamente la expiración del token. Déjame echar un vistazo y sugerir una solución."

</td>
<td>

### 🪨 Claude Cavernícola

> "Error en middleware auth. Verifica expiración: `<` no `<=`. Fix:"

</td>
</tr>
</table>

**Mismo arreglo. 75% menos palabras. Cerebro sigue grande.**

**Elige tu nivel de gruñido:**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Tu componente se re-renderiza porque creas una nueva referencia de objeto en cada render. Props de objeto en línea fallan comparación superficial. Envuelve en `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "Nueva ref objeto cada render. Prop objeto línea = nueva ref = re-render. Usar `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Prop obj línea → nueva ref → re-render. `useMemo`."

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
│  PRECISIÓN TÉCNICA      ████████ 100%│
│  AUMENTO DE VELOCIDAD   ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **Respuesta más rápida** — menos tokens que generar = velocidad brrr
- **Más fácil de leer** — sin muros de texto, solo la respuesta
- **Misma precisión** — toda la info técnica se mantiene, solo se quita el relleno ([la ciencia lo dice](https://arxiv.org/abs/2604.00025))
- **Ahorra dinero** — ~71% menos tokens de salida = menos costo
- **Divertido** — cada revisión de código se vuelve comedia

## Instalación

Elige tu agente. Un comando. Listo.

| Agente | Instalación |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | Clona repo → `/plugins` → Busca "Caveman" → Instalar |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |
| **Cualquier otro** | `npx skills add JuliusBrussee/caveman` |

Instala una vez. Úsalo en cada sesión. Una roca. Eso es todo.

### Qué Obtienes

La autoactivación está integrada para Claude Code, Gemini CLI, y la configuración local de Codex. `npx skills add` instala la habilidad para otros agentes, pero **no** instala los archivos de reglas del repositorio, por lo que Caveman no se inicia automáticamente sin el fragmento de "siempre encendido" a continuación.

> [!NOTE]
> La autoactivación funciona de forma diferente por agente. Consulta el README.md original para detalles profundos.

## Uso

Activa con:
- `/caveman` o `$caveman`
- "habla como cavernícola"
- "modo cavernícola"
- "menos tokens por favor"

Detén con: "stop caveman" o "modo normal"

### Niveles de Intensidad

| Nivel | Gatillo | Qué hace |
|-------|---------|------------|
| **Lite** | `/caveman lite` | Quita relleno, mantiene gramática. Profesional sin adornos |
| **Full** | `/caveman full` | Cavernícola por defecto. Sin artículos, fragmentos, gruñido completo |
| **Ultra** | `/caveman ultra` | Compresión máxima. Telegráfico. Abrevia todo |

## Habilidades Caveman

| Habilidad | Qué hace | Gatillo |
|-------|-----------|---------|
| **caveman-commit** | Mensajes de commit concisos. Conventional Commits. ≤50 car. Por qué sobre qué. | `/caveman-commit` |
| **caveman-review** | Comentarios PR de una línea: `L42: 🔴 bug: usuario null. Añadir guardia.` Sin preámbulos. | `/caveman-review` |
| **caveman-help** | Tarjeta de referencia rápida. Todos los modos y comandos. | `/caveman-help` |

## Licencia

MIT — libre como mamut en la pradera.
