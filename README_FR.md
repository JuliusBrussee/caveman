<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>pourquoi utiliser beaucoup de jetons quand peu suffisent</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Dernier Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="Licence"></a>
</p>

<p align="center">
  <a href="#avant--après">Avant/Après</a> •
  <a href="#installation">Installation</a> •
  <a href="#niveaux-dintensité">Niveaux</a> •
  <a href="#compétences-caveman">Compétences</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#évaluations">Évals</a>
</p>

---

Une compétence/extension pour [Claude Code](https://docs.anthropic.com/en/docs/claude-code) et une extension Codex qui fait parler votre agent comme un homme des cavernes — réduisant **~75% des jetons (tokens) de sortie** tout en conservant une précision technique totale. Maintenant avec le [mode 文言文](#文言文-mode-wenyan), les [commits concis](#caveman-commit), les [revues de code en une ligne](#caveman-review), et un [outil de compression](#caveman-compress) qui réduit **~46% des jetons d'entrée** à chaque session.

Basé sur l'observation virale que le parler "caverne" réduit considérablement l'usage de jetons par les LLM sans perte de substance technique. Nous en avons donc fait une installation en une seule ligne.

## Avant / Après

<table>
<tr>
<td width="50%">

### 🗣️ Claude Normal (69 jetons)

> "La raison pour laquelle votre composant React se re-rend est probablement parce que vous créez une nouvelle référence d'objet à chaque cycle de rendu. Lorsque vous passez un objet en ligne en tant que prop, la comparaison superficielle de React le voit comme un objet différent à chaque fois, ce qui déclenche un rendu. Je recommanderais d'utiliser useMemo pour mémoïser l'objet."

</td>
<td width="50%">

### 🪨 Claude Caverne (19 jetons)

> "Nouv. réf. objet chq rendu. Prop objet ligne = nouv. réf. = re-rendu. Utiliser `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Claude Normal

> "Bien sûr ! Je serais ravi de vous aider. Le problème que vous rencontrez est très probablement causé par votre middleware d'authentification qui ne valide pas correctement l'expiration du jeton. Laissez-moi jeter un œil et suggérer un correctif."

</td>
<td>

### 🪨 Claude Caverne

> "Bug middleware auth. Check expiration jeton : `<` pas `<=`. Fix :"

</td>
</tr>
</table>

**Même correctif. 75% de mots en moins. Cerveau toujours gros.**

**Choisissez votre niveau de grognement :**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Votre composant se re-rend car vous créez une référence d'objet à chaque rendu. Les props d'objets en ligne échouent à la comparaison superficielle. Enveloppez-le dans `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "Nouv. réf. objet chq rendu. Prop objet ligne = nouv. réf. = re-rendu. Utiliser `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Prop obj ligne → nouv. réf. → re-rendu. `useMemo`."

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**Même réponse. Vous choisissez combien de mots.**

```
┌─────────────────────────────────────┐
│  JETONS ÉCONOMISÉS     ████████ 75% │
│  PRÉCISION TECHNIQUE    ████████ 100%│
│  AUGMENTATION VITESSE   ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **Réponse plus rapide** — moins de jetons à générer = la vitesse fait brrr
- **Plus facile à lire** — pas de mur de texte, juste la réponse
- **Même précision** — toutes les infos techniques gardées, seul le superflu retiré ([la science le dit](https://arxiv.org/abs/2604.00025))
- **Économisez de l'argent** — ~71% de jetons de sortie en moins = moins de coût
- **Amusant** — chaque revue de code devient une comédie

## Installation

Choisissez votre agent. Une commande. Terminé.

| Agent | Installation |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Codex** | Clone repo → `/plugins` → Chercher "Caveman" → Installer |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` |
| **Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` |
| **Autre** | `npx skills add JuliusBrussee/caveman` |

Installez une fois. Utilisez dans chaque session pour cet agent après cela. Un caillou. C'est tout.

### Ce que vous obtenez

L'auto-activation est intégrée pour Claude Code, Gemini CLI, et la configuration Codex locale ci-dessous. `npx skills add` installe la compétence pour d'autres agents, mais n'installe **pas** les fichiers de règles/instructions du dépôt, donc Caveman ne démarre pas automatiquement sans l'ajout du snippet "Toujours activé" ci-dessous.

| Fonctionnalité | Claude Code | Codex | Gemini CLI | Cursor | Windsurf | Cline | Copilot |
|---------|:-----------:|:-----:|:----------:|:------:|:--------:|:-----:|:-------:|
| Mode Caveman | O | O | O | O | O | O | O |
| Auto-activation chaque session | O | O¹ | O | —² | —² | —² | —² |
| Commande `/caveman` | O | O¹ | O | — | — | — | — |
| Changement de mode (lite/full/ultra) | O | O¹ | O | O³ | O³ | — | — |
| Badge barre d'état | O⁴ | — | — | — | — | — | — |
| caveman-commit | O | — | O | O | O | O | O |
| caveman-review | O | — | O | O | O | O | O |
| caveman-compress | O | O | O | O | O | O | O |
| caveman-help | O | — | O | O | O | O | O |

> [!NOTE]
> L'auto-activation fonctionne différemment selon l'agent : Claude Code utilise des hooks de début de session, la configuration Codex de ce dépôt utilise `.codex/hooks.json`, Gemini utilise des fichiers de contexte. Cursor/Windsurf/Cline/Copilot peuvent être réglés sur "toujours activé", mais encore une fois, `npx skills add` installe uniquement la compétence.
>
> ¹ Codex utilise la syntaxe `$caveman`. Ce dépôt contient `.codex/hooks.json`.
> ² Ajoutez le snippet "Envie qu'il soit toujours là ?" ci-dessous.
> ³ Cursor et Windsurf reçoivent le fichier SKILL.md complet.
> ⁴ Disponible dans Claude Code, configuré via `install.sh`.

(Détails masqués pour la brièveté de la traduction - voir README.md pour les détails techniques spécifiques à chaque agent)

## Usage

Déclenchez avec :
- `/caveman` ou `$caveman` (Codex)
- "parle comme un homme des cavernes"
- "mode caveman"
- "moins de jetons s'il te plaît"

Arrêtez avec : "stop caveman" ou "mode normal"

### Niveaux d'intensité

| Niveau | Déclencheur | Action |
|-------|---------|------------|
| **Lite** | `/caveman lite` | Retire le superflu, garde la grammaire. Pro mais sans gras |
| **Full** | `/caveman full` | Caveman par défaut. Pas d'articles, fragments, grognement complet |
| **Ultra** | `/caveman ultra` | Compression maximale. Télégraphique. Abrège tout |

### Mode 文言文 (Wenyan)

Compression littéraire en chinois classique — même précision technique, mais dans la langue écrite la plus efficace en jetons jamais inventée.

| Niveau | Déclencheur | Action |
|-------|---------|------------|
| **Wenyan-Lite** | `/caveman wenyan-lite` | Semi-classique. Grammaire intacte, superflu parti |
| **Wenyan-Full** | `/caveman wenyan` | Full 文言文. Brièveté classique maximale |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | Extrême. Érudit ancien avec un petit budget |

## Compétences Caveman

| Skill | Action | Trigger |
|-------|-----------|---------|
| **caveman-commit** | Messages de commit concis. Commits Conventionnels. Sujet ≤50 car. Pourquoi plutôt que quoi. | `/caveman-commit` |
| **caveman-review** | Commentaires PR d'une ligne : `L42: 🔴 bug: user null. Add guard.` Pas de blabla. | `/caveman-review` |
| **caveman-help** | Carte de référence rapide. Tous les modes, compétences, commandes. | `/caveman-help` |

### caveman-compress

Caveman fait en sorte que Claude *parle* avec moins de jetons. **Compress** fait en sorte que Claude *lise* moins de jetons.

Votre `CLAUDE.md` se charge à **chaque début de session**. Caveman Compress réécrit les fichiers de mémoire en parler caverne pour que Claude lise moins — sans que vous perdiez l'original lisible par l'humain.

```
/caveman:compress CLAUDE.md
```

## Benchmarks

Vrais comptes de jetons via l'API Claude :

| Tâche | Normal (jetons) | Caveman (jetons) | Économisé |
|------|---------------:|----------------:|------:|
| Expliquer bug re-render React | 1180 | 159 | 87% |
| Fix expiration jeton middleware | 704 | 121 | 83% |
| Configuration pool PostgreSQL | 2347 | 380 | 84% |
| **Moyenne** | **1214** | **294** | **65%** |

## Licences

MIT — libre comme un mammouth dans la plaine.
