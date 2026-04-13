# Hooks de Caveman

Estos hooks están **incluidos en el plugin caveman** y se activan automáticamente cuando el plugin se instala. No se requiere configuración manual.

Si instalaste caveman de forma standalone (sin el plugin), puedes usar `bash hooks/install.sh` para conectarlos a tu settings.json manualmente.

## Qué se incluye

### `caveman-activate.js` — Hook SessionStart

- Se ejecuta una vez cuando Claude Code arranca
- Escribe `full` en `~/.claude/.caveman-active` (archivo de bandera)
- Emite las reglas caveman como contexto SessionStart oculto
- Detecta configuración de statusline faltante y emite sugerencia de configuración (Claude ofrecerá ayuda)

### `caveman-mode-tracker.js` — Hook UserPromptSubmit

- Se dispara en cada prompt de usuario, comprueba comandos `/caveman`
- Escribe el modo activo en el archivo de bandera cuando detecta un comando caveman
- Soporta: `full`, `lite`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Script de insignia de statusline

- Lee `~/.claude/.caveman-active` y devuelve una insignia coloreada
- Muestra `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc.

## Insignia de Statusline

La insignia de statusline muestra qué modo caveman está activo directamente en la barra de estado de Claude Code.

**Usuarios con plugin:** Si no tienes ya una `statusLine` configurada, Claude lo detectará en tu primera sesión tras la instalación y ofrecerá configurarla. Acepta y listo.

Si ya tienes una statusline personalizada, caveman no la sobreescribe y Claude no dice nada. Añade el fragmento de la insignia a tu script existente en su lugar.

**Usuarios standalone:** `install.sh` / `install.ps1` conecta la statusline automáticamente si no tienes ya una statusline personalizada. Si la tienes, el instalador la deja intacta e imprime la nota de fusión.

**Configuración manual:** Si necesitas configurarla tú mismo, añade uno de estos a `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash /ruta/a/caveman-statusline.sh"
  }
}
```

```json
{
  "statusLine": {
    "type": "command",
    "command": "powershell -ExecutionPolicy Bypass -File C:\\ruta\\a\\caveman-statusline.ps1"
  }
}
```

Reemplaza la ruta con la ubicación real del script (p.ej. `~/.claude/hooks/` para instalaciones standalone, o el directorio de instalación del plugin para instalaciones con plugin).

**Statusline personalizada:** Si ya tienes un script de statusline, añade este fragmento:

```bash
caveman_text=""
caveman_flag="$HOME/.claude/.caveman-active"
if [ -f "$caveman_flag" ]; then
  caveman_mode=$(cat "$caveman_flag" 2>/dev/null)
  if [ "$caveman_mode" = "full" ] || [ -z "$caveman_mode" ]; then
    caveman_text=$'\033[38;5;172m[CAVEMAN]\033[0m'
  else
    caveman_suffix=$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')
    caveman_text=$'\033[38;5;172m[CAVEMAN:'"${caveman_suffix}"$']\033[0m'
  fi
fi
```

Ejemplos de insignia:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## Cómo Funciona

```
Hook SessionStart ──escribe "full"──▶ ~/.claude/.caveman-active ◀──escribe modo── Hook UserPromptSubmit
                                              │
                                           lee
                                              ▼
                                     Script statusline
                                    [CAVEMAN:ULTRA] │ ...
```

El stdout de SessionStart se inyecta como contexto de sistema oculto — Claude lo ve, los usuarios no. La statusline corre como proceso separado. El archivo de bandera es el puente.

## Desinstalar

Si instalado vía plugin: deshabilitar el plugin — los hooks se desactivan automáticamente.

Si instalado vía `install.sh`:
```bash
bash hooks/uninstall.sh
```

O manualmente:
1. Eliminar `~/.claude/hooks/caveman-activate.js`, `~/.claude/hooks/caveman-mode-tracker.js` y el script de statusline correspondiente (`caveman-statusline.sh` en macOS/Linux o `caveman-statusline.ps1` en Windows)
2. Eliminar las entradas SessionStart, UserPromptSubmit y statusLine de `~/.claude/settings.json`
3. Borrar `~/.claude/.caveman-active`
