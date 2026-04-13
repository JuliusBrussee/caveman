---
name: caveman-help
description: >
  Tarjeta de referencia rápida para todos los modos, skills y comandos de caveman.
  Visualización única, no es un modo persistente. Activar: /caveman-help,
  "caveman help", "qué comandos tiene caveman", "cómo uso caveman".
---

# Ayuda Caveman

Mostrar esta tarjeta de referencia al ser invocado. Una sola vez — NO cambiar modo, escribir archivos de bandera, ni persistir nada. Devolver en estilo caveman.

## Modos

| Modo | Activar | Qué cambia |
|------|---------|------------|
| **Lite** | `/caveman lite` | Elimina relleno. Mantiene estructura de frases. |
| **Full** | `/caveman` | Elimina artículos, relleno, formalidades, vacilaciones. Fragmentos OK. Predeterminado. |
| **Ultra** | `/caveman ultra` | Compresión extrema. Fragmentos mínimos. Tablas sobre prosa. |
| **Wenyan-Lite** | `/caveman wenyan-lite` | Estilo chino clásico, compresión ligera. |
| **Wenyan-Full** | `/caveman wenyan` | 文言文 completo. Máxima tersura clásica. |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | Extremo. Erudito antiguo con presupuesto ajustado. |

Modo persiste hasta que se cambie o finalice la sesión.

## Skills

| Skill | Activar | Qué hace |
|-------|---------|----------|
| **caveman-commit** | `/caveman-commit` | Mensajes de commit concisos. Conventional Commits. Sujeto ≤50 chars. |
| **caveman-review** | `/caveman-review` | Comentarios de PR de una línea: `L42: bug: user nulo. Añadir guard.` |
| **caveman-compress** | `/caveman:compress <archivo>` | Comprimir archivos .md a prosa caveman. Ahorra ~46% tokens de entrada. |
| **caveman-help** | `/caveman-help` | Esta tarjeta. |

## Desactivar

Di "stop caveman" o "modo normal". Reanudar en cualquier momento con `/caveman`.

## Configurar Modo Predeterminado

Modo predeterminado = `full`. Cambiarlo:

**Variable de entorno** (máxima prioridad):
```bash
export CAVEMAN_DEFAULT_MODE=ultra
```

**Archivo de configuración** (`~/.config/caveman/config.json`):
```json
{ "defaultMode": "lite" }
```

Poner `"off"` para desactivar la auto-activación al inicio de sesión. El usuario puede activar manualmente con `/caveman`.

Resolución: var entorno > archivo config > `full`.

## Más

Documentación completa: https://github.com/JuliusBrussee/caveman
