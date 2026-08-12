# Caveman demo — reproducible hero gif for the README.
# Render:  cd <repo root> && PATH=/opt/homebrew/bin:$PATH vhs docs/assets/caveman-demo.tape
# Produces: docs/assets/caveman-demo.gif  (runs the REAL engine on the committed sample)

Output docs/assets/caveman-demo.gif
Require go

Set Shell bash
Set FontFamily "Menlo"
Set FontSize 24
Set Width 1180
Set Height 420
Set Padding 36
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms

# --- off-screen setup: build the real engine, stage the sample as orders.json,
#     alias `caveman` to the reproducible wrapper ---
Hide
Type "go build -o /tmp/caveman-engine ./public/engine/cmd/caveman-engine" Enter
Sleep 5s
Type "cp docs/assets/caveman-demo.sample.json orders.json" Enter
Type "caveman() { CAVEMAN_BIN=/tmp/caveman-engine docs/assets/caveman-demo.sh; }" Enter
Type "clear" Enter
Show

Sleep 800ms
Type "caveman compress < orders.json"
Sleep 700ms
Enter
Sleep 3s
Sleep 1500ms
