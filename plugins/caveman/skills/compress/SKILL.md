---
name: compress
description: >
  Comprime archivos de memoria en lenguaje natural (CLAUDE.md, todos, preferencias) al formato
  caveman para ahorrar tokens de entrada. Preserva toda la sustancia técnica, código, URLs y estructura.
  La versión comprimida sobreescribe el archivo original. El backup legible se guarda como FILE.original.md.
  Activar: /caveman:compress <ruta_archivo> o "comprimir archivo de memoria"
---

# Caveman Compress

## Propósito

Comprimir archivos en lenguaje natural (CLAUDE.md, todos, preferencias) a estilo caveman para reducir tokens de entrada. La versión comprimida sobreescribe el original. El backup legible se guarda como `<nombre_archivo>.original.md`.

## Activación

`/caveman:compress <ruta_archivo>` o cuando el usuario pida comprimir un archivo de memoria.

## Proceso

1. Los scripts de compresión están en `caveman-compress/scripts/` (junto a este SKILL.md). Si la ruta no está disponible inmediatamente, buscar `caveman-compress/scripts/__main__.py`.

2. Ejecutar:

cd caveman-compress && python3 -m scripts <ruta_absoluta_archivo>

3. La CLI:
- detecta el tipo de archivo (sin tokens)
- llama a Claude para comprimir
- valida la salida (sin tokens)
- si hay errores: aplica fix específico con Claude (solo parches concretos, sin recompresión)
- reintenta hasta 2 veces
- si sigue fallando tras 2 reintentos: reportar error al usuario, dejar archivo original sin tocar

4. Devolver resultado al usuario

## Reglas de Compresión

### Eliminar
- Artículos: un, una, el, la, los, las
- Relleno: just, really, básicamente, realmente, simplemente, esencialmente, generalmente
- Formalidades: "claro", "por supuesto", "encantado de", "recomendaría"
- Vacilaciones: "podría valer la pena", "podrías considerar", "sería bueno"
- Frases redundantes: "con el fin de" → "para", "asegurarse de" → "asegurar", "la razón es porque" → "porque"
- Relleno conectivo: "sin embargo", "además", "adicionalmente", "por otro lado"

### Preservar EXACTAMENTE (nunca modificar)
- Bloques de código (cercados con ``` e indentados)
- Código inline (contenido en `backticks`)
- URLs y enlaces (URLs completas, enlaces markdown)
- Rutas de archivo (`/src/components/...`, `./config.yaml`)
- Comandos (`npm install`, `git commit`, `docker build`)
- Términos técnicos (nombres de librerías, APIs, protocolos, algoritmos)
- Nombres propios (nombres de proyectos, personas, empresas)
- Fechas, números de versión, valores numéricos
- Variables de entorno (`$HOME`, `NODE_ENV`)

### Preservar Estructura
- Todos los headings markdown (mantener texto exacto del heading, comprimir cuerpo debajo)
- Jerarquía de viñetas (mantener nivel de anidamiento)
- Listas numeradas (mantener numeración)
- Tablas (comprimir texto de celdas, mantener estructura)
- Frontmatter/cabeceras YAML en archivos markdown

### Comprimir
- Usar sinónimos cortos: "grande" no "extenso", "fix" no "implementar una solución para", "usar" no "utilizar"
- Fragmentos OK: "Ejecutar tests antes de commit" no "Siempre deberías ejecutar los tests antes de hacer commit"
- Eliminar "deberías", "asegúrate de", "recuerda" — solo enunciar la acción
- Fusionar viñetas redundantes que dicen lo mismo de forma diferente
- Mantener un ejemplo donde múltiples ejemplos muestran el mismo patrón

REGLA CRÍTICA:
Cualquier cosa dentro de ``` ... ``` debe copiarse EXACTAMENTE.
No:
- eliminar comentarios
- eliminar espacios
- reordenar líneas
- acortar comandos
- simplificar nada

El código inline (`...`) debe preservarse EXACTAMENTE.
No modificar nada dentro de backticks.

Si el archivo contiene bloques de código:
- Tratar los bloques de código como regiones de solo lectura
- Solo comprimir texto fuera de ellos
- No fusionar secciones alrededor de código

## Límites

- SOLO comprimir archivos en lenguaje natural (.md, .txt, sin extensión)
- NUNCA modificar: .py, .js, .ts, .json, .yaml, .yml, .toml, .env, .lock, .css, .html, .xml, .sql, .sh
- Si el archivo tiene contenido mixto (prosa + código), comprimir SOLO las secciones de prosa
- Si hay duda sobre si algo es código o prosa, dejarlo sin cambios
- El archivo original se respalda como FILE.original.md antes de sobreescribir
- Nunca comprimir FILE.original.md (ignorarlo)
