<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>perché usare molti token quando pochi bastano</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Ultimo Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="Licenza"></a>
</p>

<p align="center">
  <a href="#prima--dopo">Prima/Dopo</a> •
  <a href="#installazione">Installazione</a> •
  <a href="#livelli-di-intensità">Livelli</a> •
  <a href="#abilità-caveman">Abilità</a> •
  <a href="#benchmark">Benchmark</a>
</p>

---

Una competenza/plugin per [Claude Code](https://docs.anthropic.com/en/docs/claude-code) e Codex che fa parlare l'agente come un uomo delle caverne — tagliando il **~75% dei token di output** mantenendo la piena precisione tecnica. Ora con [modalità 文言文](#文言文-wenyan-mode), [commit brevi](#caveman-commit), [code review su una riga](#caveman-review) e uno [strumento di compressione](#caveman-compress) che taglia il **~46% dei token di input** ogni sessione.

Basato sull'osservazione virale che il parlare "cavernicolo" riduce drasticamente l'uso dei token LLM senza perdere sostanza tecnica. Quindi ne abbiamo fatto un'installazione con un solo comando.

## Prima / Dopo

<table>
<tr>
<td width="50%">

### 🗣️ Claude Normale (69 token)

> "Il motivo per cui il tuo componente React si sta ri-renderizzando è probabilmente perché stai creando un nuovo riferimento a un oggetto a ogni ciclo di rendering. Quando passi un oggetto inline come prop, il confronto superficiale di React lo vede come un oggetto diverso ogni volta, il che innesca un re-render. Ti consiglierei di usare useMemo per memorizzare l'oggetto."

</td>
<td width="50%">

### 🪨 Claude Cavernicolo (19 token)

> "Nuova rif ogg ogni render. Prop ogg inline = nuova rif = re-render. Usa `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Claude Normale

> "Certo! Sarei felice di aiutarti con questo. Il problema che stai riscontrando è molto probabilmente causato dal tuo middleware di autenticazione che non convalida correttamente la scadenza del token. Lasciami dare un'occhiata e suggerire una correzione."

</td>
<td>

### 🪨 Claude Cavernicolo

> "Bug in middleware auth. Check scadenza token: `<` non `<=`. Fix:"

</td>
</tr>
</table>

**Stessa soluzione. 75% di parole in meno. Cervello ancora grande.**

**Scegli il tuo livello di grugnito:**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Il tuo componente si ri-renderizza perché crei un nuovo riferimento olistico a ogni render. Le prop oggetto inline falliscono il confronto superficiale. Avvolgi in `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "Nuova rif ogg ogni render. Prop ogg inline = nuova rif = re-render. Usa `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Prop ogg inline → nuova rif → re-render. `useMemo`."

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**Stessa risposta. Tu scegli quante parole.**

```
┌─────────────────────────────────────┐
│  TOKEN RISPARMIATI     ████████ 75% │
│  PRECISIONE TECNICA     ████████ 100%│
│  AUMENTO VELOCITÀ       ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **Risposta più veloce** — meno token da generare = velocità brrr
- **Più facile da leggere** — nessun muro di testo, solo la risposta
- **Stessa precisione** — tutte le info tecniche mantenute, rimosso solo il superfluo ([lo dice la scienza](https://arxiv.org/abs/2604.00025))
- **Risparmia soldi** — ~71% in meno di token in uscita = meno costi
- **Divertente** — ogni code review diventa una commedia

## Installazione

Scegli il tuo agente. Un comando. Fatto.

| Agente | Installazione |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | Clona repo → `/plugins` → Cerca "Caveman" → Installa |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |

Installa una volta. Usa in ogni sessione. Una roccia. Tutto qui.

## Licenza

MIT — libero come un mammut nella prateria.
