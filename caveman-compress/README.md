<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="80" />
</p>

<h1 align="center">caveman-compress</h1>

<p align="center">
  <strong>comprimir archivo de memoria. ahorrar token en cada sesión.</strong>
</p>

---

Un skill de Claude Code que comprime tus archivos de memoria del proyecto (`CLAUDE.md`, todos, preferencias) al formato caveman — para que cada sesión cargue menos tokens automáticamente.

Claude lee `CLAUDE.md` en cada inicio de sesión. Si archivo grande, coste grande. Caveman hace archivo pequeño. Coste baja para siempre.

## Qué Hace

```
/caveman:compress CLAUDE.md
```

```
CLAUDE.md          ← comprimido (Claude lee esto — menos tokens en cada sesión)
CLAUDE.original.md ← backup legible (tú editas esto)
```

El original nunca se pierde. Puedes leer y editar `.original.md`. Ejecutar el skill de nuevo para re-comprimir tras las ediciones.

## Benchmarks

Resultados reales en archivos reales de proyectos:

| Archivo | Original | Comprimido | Ahorro |
|---------|----------:|----------:|-------:|
| `claude-md-preferences.md` | 706 | 285 | **59,6%** |
| `project-notes.md` | 1145 | 535 | **53,3%** |
| `claude-md-project.md` | 1122 | 636 | **43,3%** |
| `todo-list.md` | 627 | 388 | **38,1%** |
| `mixed-with-code.md` | 888 | 560 | **36,9%** |
| **Media** | **898** | **481** | **46%** |

Todas las validaciones pasaron ✅ — headings, bloques de código, URLs, rutas de archivo preservados exactamente.

## Antes / Después

<table>
<tr>
<td width="50%">

### 📄 Original (706 tokens)

> "I strongly prefer TypeScript with strict mode enabled for all new code. Please don't use `any` type unless there's genuinely no way around it, and if you do, leave a comment explaining the reasoning. I find that taking the time to properly type things catches a lot of bugs before they ever make it to runtime."

</td>
<td width="50%">

### 🪨 Caveman (285 tokens)

> "Prefer TypeScript strict mode always. No `any` unless unavoidable — comment why if used. Proper types catch bugs early."

</td>
</tr>
</table>

**Mismas instrucciones. 60% menos tokens. Cada. Sesión.**

## Seguridad

`caveman-compress` está marcado como Alto Riesgo por Snyk debido a patrones de subprocess y E/S de archivos detectados por análisis estático. Es un falso positivo — ver [SECURITY.md](./SECURITY.md) para una explicación completa de lo que el skill hace y no hace.

## Instalar

Compress está incluido con el plugin `caveman`. Instala `caveman` una vez, luego usa `/caveman:compress`.

Si necesitas los archivos locales, el skill de compress está en:

```bash
caveman-compress/
```

**Requiere:** Python 3.10+

## Uso

```
/caveman:compress <ruta_archivo>
```

Ejemplos:
```
/caveman:compress CLAUDE.md
/caveman:compress docs/preferences.md
/caveman:compress todos.md
```

### Qué archivos funcionan

| Tipo | ¿Comprimir? |
|------|-------------|
| `.md`, `.txt`, `.rst` | ✅ Sí |
| Lenguaje natural sin extensión | ✅ Sí |
| `.py`, `.js`, `.ts`, `.json`, `.yaml` | ❌ Ignorar (código/config) |
| `*.original.md` | ❌ Ignorar (archivos de backup) |

## Cómo Funciona

```
/caveman:compress CLAUDE.md
        ↓
detectar tipo de archivo        (sin tokens)
        ↓
Claude comprime                 (tokens — una llamada)
        ↓
validar salida                  (sin tokens)
  comprueba: headings, bloques de código, URLs, rutas de archivo, viñetas
        ↓
si errores: Claude fix solo problemas concretos   (tokens — fix específico)
  NO recomprime — solo parchea las partes rotas
        ↓
reintentar hasta 2 veces
        ↓
escribir comprimido → CLAUDE.md
escribir original   → CLAUDE.original.md
```

Solo dos cosas usan tokens: compresión inicial + fix específico si falla la validación. Todo lo demás es Python local.

## Qué se Preserva

Caveman comprime lenguaje natural. Nunca toca:

- Bloques de código (` ``` ` cercados o indentados)
- Código inline (`` `contenido backtick` ``)
- URLs y enlaces
- Rutas de archivo (`/src/components/...`)
- Comandos (`npm install`, `git commit`)
- Términos técnicos, nombres de librerías, nombres de APIs
- Headings (texto exacto preservado)
- Tablas (estructura preservada, texto de celdas comprimido)
- Fechas, números de versión, valores numéricos

## Por Qué Importa

`CLAUDE.md` carga en **cada inicio de sesión**. Un archivo de memoria del proyecto de 1000 tokens cuesta tokens cada vez que abres un proyecto. En 100 sesiones son 100.000 tokens de overhead — solo por contexto que ya escribiste.

Caveman lo reduce ~46% de media. Mismas instrucciones. Misma precisión. Menos desperdicio.

```
┌────────────────────────────────────────────┐
│  AHORRO DE TOKENS POR ARCHIVO  █████   46% │
│  SESIONES QUE SE BENEFICIAN    ██████ 100% │
│  INFORMACIÓN PRESERVADA        ██████ 100% │
│  TIEMPO DE CONFIGURACIÓN       █         1x│
└────────────────────────────────────────────┘
```

## Parte de Caveman

Este skill es parte del toolkit [caveman](https://github.com/JuliusBrussee/caveman) — haciendo que Claude use menos tokens sin perder precisión.

- **caveman** — hace que Claude *hable* como cavernícola (recorta tokens de respuesta ~65%)
- **caveman-compress** — hace que Claude *lea* menos (recorta tokens de contexto ~46%)
