# Seguridad

## Calificación de Alto Riesgo de Snyk

`caveman-compress` recibe una calificación de Alto Riesgo de Snyk debido a heurísticas de análisis estático. Este documento explica lo que el skill hace y no hace.

### Qué activa la calificación

1. **Uso de subprocess**: El skill llama a la CLI `claude` vía `subprocess.run()` como fallback cuando `ANTHROPIC_API_KEY` no está establecida. La llamada al subprocess usa una lista de argumentos fija — no ocurre interpolación de shell. El contenido del archivo de usuario se pasa vía stdin, no como argumento de shell.

2. **Lectura/escritura de archivos**: El skill lee el archivo al que el usuario lo apunta explícitamente, lo comprime, y escribe el resultado de vuelta en la misma ruta. Se guarda un backup `.original.md` junto a él. No se leen ni escriben archivos fuera de la ruta especificada por el usuario.

### Lo que el skill NO hace

- No ejecuta el contenido del archivo de usuario como código
- No hace peticiones de red excepto a la API de Anthropic (vía SDK o CLI)
- No accede a archivos fuera de la ruta que el usuario proporciona
- No usa `shell=True` ni interpolación de cadenas en llamadas a subprocess
- No recopila ni transmite ningún dato más allá del archivo que se está comprimiendo

### Comportamiento de autenticación

Si `ANTHROPIC_API_KEY` está establecida, el skill usa el SDK de Python de Anthropic directamente (sin subprocess). Si no está establecida, recurre a la CLI `claude`, que usa la autenticación de escritorio de Claude existente del usuario.

### Límite de tamaño de archivo

Los archivos mayores de 500KB son rechazados antes de que se haga cualquier llamada a la API.

### Reportar una vulnerabilidad

Si crees que has encontrado un problema de seguridad genuino, abre un issue en GitHub con la etiqueta `security`.
